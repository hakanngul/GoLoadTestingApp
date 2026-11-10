package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

func cmdValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "validate: expected exactly one scenario file")
		return exitUsage
	}
	sc, err := scenario.LoadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "invalid scenario:\n%v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "ok: %q, %s model, %d step(s), %d stage(s), duration %s\n",
		sc.Name, sc.Load.Model, len(sc.Steps), len(sc.Load.Stages), sc.Load.TotalDuration())
	return exitOK
}
