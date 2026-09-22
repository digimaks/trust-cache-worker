// cmd/server/web.go
package main

import (
	"azugo.io/azugo/server"
	"azugo.io/core/cli"
	"github.com/spf13/cobra"

	worker "github.com/digimaks/trust-cache-worker"
	"github.com/digimaks/trust-cache-worker/routes"
)

func runWeb(cmd *cobra.Command, _ []string) error {
	a, err := worker.New(cmd, Version)
	if err != nil {
		return err
	}
	if err = routes.Init(a); err != nil {
		return err
	}
	server.RunContext(cmd.Context(), a)
	return nil
}

func init() {
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Start the trust-cache worker",
		RunE:  runWeb,
	}
	// Startup fails fast on an unreachable trust service unless this flag is
	// set (worker then boots degraded; /readyz 503 until synced).
	cmd.Flags().Bool("allow-cold-start", false, "boot even if the trust service is unreachable at startup")
	cli.Register(cmd, cli.AsDefault())
}
