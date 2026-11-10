// Command goloadtest runs YAML-defined load tests.
//
//	goloadtest run [flags] scenario.yaml
//	goloadtest validate scenario.yaml
//	goloadtest version
//
// Exit codes: 0 pass, 1 threshold failed, 2 usage/config error, 3 run error.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const (
	exitOK        = 0
	exitThreshold = 1
	exitUsage     = 2
	exitRun       = 3
)

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "validate":
		return cmdValidate(args[1:], stdout, stderr)
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, version)
		return exitOK
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		usage(stderr)
		return exitUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  goloadtest run [flags] scenario.yaml   run a load test
  goloadtest validate scenario.yaml      check a scenario file
  goloadtest version                     print version

Run 'goloadtest run -h' for run flags.
`)
}
