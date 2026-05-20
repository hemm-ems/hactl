package main

import (
	"errors"
	"os"

	"github.com/hemm-ems/hactl/internal/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		code := 1
		var ec interface{ ExitCode() int }
		if errors.As(err, &ec) {
			code = ec.ExitCode()
		}
		os.Exit(code)
	}
}
