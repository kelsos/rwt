package main

import (
	"fmt"
	"os"

	"github.com/kelsos/rwt/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		// An empty message means the command already printed everything the user
		// needs and only wants the exit status (a blocked commit, say).
		if msg := err.Error(); msg != "" {
			fmt.Fprintln(os.Stderr, "rwt:", msg)
		}
		os.Exit(1)
	}
}
