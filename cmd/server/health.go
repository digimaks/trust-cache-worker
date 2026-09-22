// cmd/server/health.go
package main

import (
	"azugo.io/azugo/server"
	"azugo.io/core/cli"

	worker "github.com/digimaks/trust-cache-worker"
)

func init() {
	cli.Register(server.HealthCommand("/healthz", server.Options{
		AppName:       "Trust Cache Worker",
		AppVer:        Version,
		Configuration: worker.NewConfiguration(),
	}))
}
