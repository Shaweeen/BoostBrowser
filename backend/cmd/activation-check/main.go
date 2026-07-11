package main

import (
	"os"
	"strings"

	"boost-browser/backend/internal/activation"
)

func main() {
	if len(os.Args) != 3 {
		os.Exit(2)
	}
	input, err := os.ReadFile(os.Args[1])
	if err != nil {
		os.Exit(3)
	}
	if !activation.ValidateInstallerSeed(strings.TrimSpace(string(input))) {
		os.Exit(10)
	}
	if activation.WriteInstallerMarker(os.Args[2]) != nil {
		os.Exit(4)
	}
}
