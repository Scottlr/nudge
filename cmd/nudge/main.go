package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/Scottlr/nudge/internal/cli"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := cli.Execute(ctx, cli.BuildInfo{
		Version: version,
		Commit:  commit,
		Date:    date,
	}); err != nil {
		if code, ok := cli.HealthExitCode(err); ok {
			if message := err.Error(); message != "" {
				fmt.Fprintln(os.Stderr, message)
			}
			os.Exit(code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
