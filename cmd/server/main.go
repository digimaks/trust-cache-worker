// cmd/server/main.go
package main

import (
	"os"

	"azugo.io/core/cli"
)

var Version = "1.0.0-dev"

func main() {
	if _, ok := os.LookupEnv("SERVER_URLS"); !ok {
		_ = os.Setenv("SERVER_URLS", "http://0.0.0.0:8080")
	}

	cli.Run(cli.Options{
		Use:     "server",
		Short:   "Trust cache worker",
		Long:    "EUDI verifier trust-cache worker: materializes trust anchors and status lists into Valkey.",
		Version: Version,
	})
}
