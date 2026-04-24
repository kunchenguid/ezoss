package main

import (
	"fmt"
	"os"

	"github.com/kunchenguid/ezoss/internal/cli"
	"github.com/kunchenguid/ezoss/internal/update"
)

func main() {
	// Intercept the internal --update-check flag before cobra parses argv,
	// so the background process spawned by MaybeNotifyAndCheck never reaches
	// the user-facing CLI.
	if handled, err := update.MaybeHandleBackgroundCheck(os.Args[1:]); handled {
		if err != nil {
			os.Exit(1)
		}
		return
	}

	// Best-effort: notify the user if a new version is cached, and refresh
	// the cache in the background when stale.
	update.MaybeNotifyAndCheck(os.Args[1:], os.Stderr)

	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
