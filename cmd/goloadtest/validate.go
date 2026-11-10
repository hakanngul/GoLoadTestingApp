package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/hakanngul/GoLoadTestingApp/internal/data"
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

	// {{var}} references nothing defines (warnings only). The data file's
	// header gives the CSV columns when it can be read.
	var columns []string
	known := sc.Data == nil
	if sc.Data != nil {
		if f, err := os.Open(sc.Data.File); err == nil {
			if h, err := data.Header(f); err == nil {
				columns, known = h, true
			}
			f.Close()
		}
	}
	for _, is := range sc.CheckVars(columns, known) {
		fmt.Fprintf(stdout, "warning: %s\n", is)
	}
	return exitOK
}
