package main

import (
	"os"

	"github.com/hemm-ems/hactl/internal/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
