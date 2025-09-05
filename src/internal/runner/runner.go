package runner

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/boasihq/interactive-inputs/internal/config"
	"github.com/boasihq/interactive-inputs/internal/errors"
	"github.com/boasihq/interactive-inputs/internal/notifier"
	"github.com/boasihq/interactive-inputs/internal/portal"
	webui "github.com/boasihq/interactive-inputs/internal/web"
	"github.com/gorilla/mux"
	"go.uber.org/zap"
	"golang.ngrok.com/ngrok"
	nconfig "golang.ngrok.com/ngrok/config"
)

func InvokeAction(ctx context.Context, ctxCancel context.CancelFunc, cfg *config.Config, embeddedContent fs.FS, embeddedContentFilePathPrefix string) error {

	defer ctxCancel()

	var githubActionWorkingDir string = os.Getenv("GITHUB_WORKSPACE")
	var isRunningLocal bool = os.Getenv("IAIP_LOCAL_RUN") != ""
	var isInteractiveInputsCacheDirAvailable bool = false
	var interactiveInputsCacheDir string
	var inputFieldLabelToCacheDirMapping map[string]string = make(map[string]string)

	if githubActionWorkingDir == "" {
		cfg.Action.Errorf("GITHUB_WORKSPACE not found")
		return errors.ErrGitHubWorkspaceEnvVarIsMissing
	}

	// TODO: Get the source job's url that's calling the
	// action so that we can link to it. and send users
	// back to it

	/// Notifiers
	slackNotifier := notifier.NewSlackNotifier(&notifier.NewSlackNotifierRequest{})
	discordNotifier := notifier.NewDiscordNotifier(&notifier.NewDiscordNotifierRequest{})

	if cfg.NotifierSlackEnabled {
		slackNotifier = notifier.NewSlackNotifier(&notifier.NewSlackNotifierRequest{
			Enabled:   cfg.NotifierSlackEnabled,
			Token:     cfg.NotifierSlackToken,
			Channel:   cfg.NotifierSlackChannel,
			BotName:   cfg.NotifierSlackBotName,
			ActionPkg: cfg.Action,
			ThreadTs:  cfg.NotifierSlackThreadTs,
		})

		verifiedSlackNotifierErr := slackNotifier.Verify()
		if verifiedSlackNotifierErr != nil {
			cfg.Action.Errorf("Slack Notifier Verification Failed")
			return verifiedSlackNotifierErr
		}

		cfg.Action.Debugf("Slack Notifier Verification Succeeded")
	}

	if cfg.NotifierDiscordEnabled {
		discordNotifier = notifier.NewDiscordNotifier(&notifier.NewDiscordNotifierRequest{
			Enabled:          cfg.NotifierDiscordEnabled,
			WebhookUrl:       cfg.NotifierDiscordWebhook,
			UsernameOverride: cfg.NotifierDiscordUsernameOverride,
			ActionPkg:        cfg.Action,
			ThreadId:         cfg.NotifierDiscordThreadId,
		})

		verifiedDiscordNotifierErr := discordNotifier.Verify()
		if verifiedDiscordNotifierErr != nil {
			cfg.Action.Errorf("Discord Notifier Verification Failed")
			return verifiedDiscordNotifierErr
		}
		cfg.Action.Debugf("Discord Notifier Verification Succeeded")
	}

	// Create cache directory mapping for all the file and
	// multifile input fields defined in the config. We'll
	// use this hold all the files uploaded by the user
	// during the action run
	if cfg.Fields != nil {
		var err error
		var baseCacheDir string = fmt.Sprintf("%s/.__interactive-inputs-cache", os.Getenv("GITHUB_WORKSPACE"))

		// check fields for file and multifile input fields
		for _, v := range cfg.Fields.Fields {

			// skip non-file and multifile input fields
			if v.Properties.Type != "file" && v.Properties.Type != "multifile" {
				continue
			}

			// create base cache directory for holding uploaded files
			// if it doesn't exist
			if !isInteractiveInputsCacheDirAvailable {
				// create temp directory to hold uploaded files
				err = os.MkdirAll(baseCacheDir, os.ModePerm)
				if err != nil {
					cfg.Action.Errorf("Unable to base cache directory: %v", zap.Error(err))
					return err
				}

				cfg.Action.Debugf("Base cache directory created: %s", interactiveInputsCacheDir)
				isInteractiveInputsCacheDirAvailable = true
			}

			// create sub-directory for holding uploaded files for
			// for the current input field
			cfg.Action.Debugf("Creating cache sub-directory for %s uploads", v.Label)
			inputFieldCacheDir, err := os.MkdirTemp(baseCacheDir, fmt.Sprintf("%s-%d", v.Label, time.Now().UnixNano()))
			if err != nil {
				cfg.Action.Errorf("Unable to create temp directory: %v", zap.Error(err))
				return err
			}

			// add mapping of input field label to cache sub-directory
			inputFieldLabelToCacheDirMapping[v.Label] = inputFieldCacheDir
		}
	}

	/// Handlers
	uiHandler := webui.NewWebAppHandler(&webui.NewWebAppHandlerRequest{
		EmbeddedContent:               embeddedContent,
		EmbeddedContentFilePathPrefix: embeddedContentFilePathPrefix,
		Config:                        cfg,
	})

	portalEventHandler := portal.NewHandler(cfg.Action, isRunningLocal, embeddedContent, embeddedContentFilePathPrefix, cfg.GithubToken, inputFieldLabelToCacheDirMapping)

	/// Routes
	r := mux.NewRouter()

	portal.AttachRoutes(&portal.AttachRoutesRequest{
		Router:                        r,
		PortalEventHandler:            portalEventHandler,
		UiHandler:                     uiHandler,
		EmbeddedContent:               embeddedContent,
		EmbeddedContentFilePathPrefix: embeddedContentFilePathPrefix,
		ActionPkg:                     cfg.Action,
	})

	/// Server
	serverDone := make(chan error, 1)
	serverInitMessageTmpl := "Your Interactive Inputs portal is reachable at: %s"
	notifierSlackEnterInputMessageTmpl := "<%s|*Enter required input*>"
	notifierDiscordEnterInputMessageTmpl := "[**Enter required input**](%s)"
	universalNotifierFailedToSelfHost := "A failure has occurred while starting/running your self-hosted portal: %v"

	// Determine whether to use ngrok, network IP, or localhost
	useNetworkIP := cfg.UseNetworkIP
	var networkIP string
	var err error

	if useNetworkIP {
		if cfg.NetworkIP != "" {
			networkIP = cfg.NetworkIP
		} else {
			networkIP, err = getNetworkIP()
			if err != nil {
				cfg.Action.Errorf("Failed to detect network IP: %v", err)
				return err
			}
		}
		cfg.Action.Debugf("Using network IP: %s", networkIP)
	}

	// TODO: Add a flag to enable/disable the ngrok tunnel respsective
	// of whether the action is running locally or not
	if !isRunningLocal && !useNetworkIP {
		ln, err := ngrok.Listen(ctx,
			nconfig.HTTPEndpoint(),
			ngrok.WithAuthtoken(cfg.NgrokAuthtoken),
		)
		if err != nil {
			return err
		}

		serverInitMessage := fmt.Sprintf(serverInitMessageTmpl, ln.URL())

		cfg.Action.Noticef(serverInitMessage)

		if slackNotifier.Enabled() {
			_, err := slackNotifier.Notify(cfg.Title, fmt.Sprintf(notifierSlackEnterInputMessageTmpl, ln.URL()))
			if err != nil {
				cfg.Action.Errorf("Slack Notifier Notification Failed: %v", err)
				return err
			}
		}

		if discordNotifier.Enabled() {
			_, err := discordNotifier.Notify(cfg.Title, fmt.Sprintf(notifierDiscordEnterInputMessageTmpl, ln.URL()))
			if err != nil {
				cfg.Action.Errorf("Discord Notifier Notification Failed: %v", err)
				return err
			}
		}

		go func() {
			// server logic
			if err := http.Serve(ln, r); err != nil {
				serverErrorMessage := fmt.Sprintf(universalNotifierFailedToSelfHost, err)

				cfg.Action.Errorf(serverErrorMessage)
				if slackNotifier.Enabled() {
					_, err := slackNotifier.Notify(cfg.Title, serverErrorMessage)
					if err != nil {
						cfg.Action.Errorf("Slack Notifier Notification Failed: %v", err)
					}
				}

				if discordNotifier.Enabled() {
					_, err := discordNotifier.Notify(cfg.Title, serverErrorMessage)
					if err != nil {
						cfg.Action.Errorf("Discord Notifier Notification Failed: %v", err)
					}
				}

				serverDone <- err
			}
			serverDone <- ln.CloseWithContext(ctx)
		}()

	} else {
		// Find an available port starting from the configured start port
		if cfg.StartPort == 0 {
			cfg.StartPort = 8080
		}
		availablePort, err := findAvailablePort(cfg.StartPort)
		if err != nil {
			cfg.Action.Errorf("Failed to find available port: %v", err)
			return err
		}
		
		if availablePort != cfg.StartPort {
			cfg.Action.Debugf("Port %d was occupied, using port %d instead", cfg.StartPort, availablePort)
		}
		
		localPort := fmt.Sprintf(":%d", availablePort)
		server := &http.Server{Addr: localPort, Handler: r}
		
		var completeLocalUrl string
		if useNetworkIP {
			completeLocalUrl = fmt.Sprintf("http://%s:%d", networkIP, availablePort)
		} else {
			completeLocalUrl = fmt.Sprintf("http://localhost:%d", availablePort)
		}
		
		cfg.Action.Debugf("Using port: %d", availablePort)
		serverInitMessage := fmt.Sprintf(serverInitMessageTmpl, completeLocalUrl)

		cfg.Action.Noticef(serverInitMessage)
		if slackNotifier.Enabled() {
			_, err := slackNotifier.Notify(cfg.Title, fmt.Sprintf(notifierSlackEnterInputMessageTmpl, completeLocalUrl))
			if err != nil {
				cfg.Action.Errorf("Slack Notifier Notification Failed: %v", err)
				return err
			}
		}

		if discordNotifier.Enabled() {
			_, err := discordNotifier.Notify(cfg.Title, fmt.Sprintf(notifierDiscordEnterInputMessageTmpl, completeLocalUrl))
			if err != nil {
				cfg.Action.Errorf("Discord Notifier Notification Failed: %v", err)
				return err
			}
		}

		go func() {
			// server logic
			if err := server.ListenAndServe(); err != nil {
				serverErrorMessage := fmt.Sprintf(universalNotifierFailedToSelfHost, err)

				cfg.Action.Errorf(serverErrorMessage)
				if slackNotifier.Enabled() {
					_, err := slackNotifier.Notify(cfg.Title, serverErrorMessage)
					if err != nil {
						cfg.Action.Errorf("Slack Notifier Notification Failed: %v", err)
					}
				}

				if discordNotifier.Enabled() {
					_, err := discordNotifier.Notify(cfg.Title, serverErrorMessage)
					if err != nil {
						cfg.Action.Errorf("Discord Notifier Notification Failed: %v", err)
					}
				}

				serverDone <- err
			}
			serverDone <- server.Shutdown(ctx)
		}()
	}

	select {
	case err := <-serverDone:
		return handlePrettierTimeoutErrorMessage(err, cfg.Timeout)
	case <-ctx.Done():
		// Timeout occurred
		ctxCancel() // Ensure all resources are cleaned up

		return handlePrettierTimeoutErrorMessage(ctx.Err(), cfg.Timeout)
	}

}

// handlePrettierTimeoutErrorMessage is a helper function that prints a nicer error message
// when the context deadline is exceeded. Otherwise, it returns the original error.
func handlePrettierTimeoutErrorMessage(err error, timeout int) error {
	if err == context.DeadlineExceeded {
		return fmt.Errorf("the interactive inputs portal timed out after %d seconds", timeout)
	}
	return err
}

// getNetworkIP detects the network IP address of the runner
func getNetworkIP() (string, error) {
	// Try to connect to a remote address to determine the local IP
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", fmt.Errorf("failed to get network IP: %v", err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String(), nil
}

// isPortAvailable checks if a port is available for use
func isPortAvailable(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// findAvailablePort finds an available port starting from the given port
func findAvailablePort(startPort int) (int, error) {
	maxAttempts := 100
	for i := 0; i < maxAttempts; i++ {
		port := startPort + i
		if port > 65535 {
			break
		}
		if isPortAvailable(port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port found starting from %d (checked ports %d-%d)", startPort, startPort, startPort+maxAttempts-1)
}
