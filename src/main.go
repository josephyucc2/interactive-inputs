package main

import (
	"context"
	"embed"
	"os"
	"time"

	"github.com/boasihq/interactive-inputs/internal/config"
	"github.com/boasihq/interactive-inputs/internal/fields"
	"github.com/boasihq/interactive-inputs/internal/runner"
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

func main() {
	err := run()
	if err != nil {
		githubactions.Fatalf("%v", err)
	}
}
