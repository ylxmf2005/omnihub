package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ylxmf2005/omnihub/internal/config"
	"github.com/ylxmf2005/omnihub/internal/transport"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: omnihub <schema|paths>")
		os.Exit(3)
	}
	var value any
	switch os.Args[1] {
	case "schema":
		artifacts, err := transport.Generate()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		value = artifacts
	case "paths":
		paths, err := config.Resolve()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		value = paths
	default:
		fmt.Fprintln(os.Stderr, "usage: omnihub <schema|paths>")
		os.Exit(3)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
