package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/boasihq/interactive-inputs/internal/config"
	"github.com/boasihq/interactive-inputs/internal/fields"
	"github.com/boasihq/interactive-inputs/internal/runner"
	"github.com/boasihq/interactive-inputs/internal/server"
	"github.com/boasihq/interactive-inputs/internal/session"
	"github.com/gorilla/mux"
	githubactions "github.com/sethvargo/go-githubactions"

	_ "embed"
)

// content holds our static web server content.
//
//go:embed internal/web/ui/static/* internal/web/ui/html/*
var content embed.FS

func run() error {

	var (
		ctx    context.Context       = context.Background()
		action *githubactions.Action = githubactions.New()
		cfg    *config.Config
		err    error
	)

	// Added logic to bypass the config parse
	if os.Getenv("IAIP_SKIP_CONFIG_PARSE") == "" {
		cfg, err = config.NewFromInputs(action)
		if err != nil {
			return err
		}
	} else {
		// Parse fields even when skipping config parse
		interactiveInput := action.GetInput("interactive")
		fields, err := fields.MarshalStringIntoValidFieldsStruct(interactiveInput, action)
		if err != nil {
			action.Errorf("Can't convert the 'fields' input to a valid fields config: %s", interactiveInput)
			// Continue with nil fields if parsing fails
		}

		cfg = &config.Config{
			Action:  action,
			Timeout: config.DefaultTimeout,
			Fields:  fields,
		}
	}

	// Add timeout to context
	ctx, ctxCancel := context.WithTimeout(ctx, time.Duration(cfg.Timeout)*time.Second)

	return runner.InvokeAction(ctx, ctxCancel, cfg, &content, "internal/")
}

func runServer(port int, sessionTimeout int) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sm := session.NewManager()
	sm.StartCleanupRoutine(ctx)

	srv := server.New(sm, &content, "internal/", &server.Config{
		Port:           port,
		SessionTimeout: sessionTimeout,
	})

	r := mux.NewRouter()
	srv.AttachRoutes(r)

	addr := fmt.Sprintf(":%d", port)
	httpServer := &http.Server{Addr: addr, Handler: r}

	go func() {
		log.Printf("Interactive Inputs server listening on %s", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sm.Shutdown()
	return httpServer.Shutdown(shutdownCtx)
}

func main() {
	serverMode := flag.Bool("server", false, "Run in persistent server mode")
	port := flag.Int("port", 8080, "Server port (server mode only)")
	sessionTimeout := flag.Int("session-timeout", 300, "Default session timeout in seconds (server mode only)")
	flag.Parse()

	if *serverMode {
		if err := runServer(*port, *sessionTimeout); err != nil {
			log.Fatalf("server error: %v", err)
		}
		return
	}

	// Action mode (default)
	err := run()
	if err != nil {
		githubactions.Fatalf("%v", err)
	}
}
