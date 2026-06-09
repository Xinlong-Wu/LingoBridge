package main

import (
	"errors"
	"fmt"
	"os"

	"lingobridge/internal/app"
)

func main() {
	if err := app.Run(os.Args[1:]); err != nil {
		if !errors.Is(err, app.ErrUsage) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
