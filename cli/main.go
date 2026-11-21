package main

import (
	"os"

	"github.com/duchess-devops/duchess-cli/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
