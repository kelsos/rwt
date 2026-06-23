package main

import (
	"fmt"
	"os"

	"github.com/kelsos/rwt/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "rwt:", err)
		os.Exit(1)
	}
}
