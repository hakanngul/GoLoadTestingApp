package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hakanngul/GoLoadTestingApp/internal/data"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
	"github.com/hakanngul/GoLoadTestingApp/internal/trial"
)

// Try runs one trial iteration of test.YAML (see package trial) with the
// same executors and target allowlist as runs. test.ID may be empty for an
// unsaved test; otherwise the first row of the test's uploaded data file
// supplies the {{vars}}. A data file that is not available is reported in
// TryResult.DataNote and the trial runs without data.
//
// Errors: *ValidationError for an invalid scenario, ErrShuttingDown.
func (m *Manager) Try(ctx context.Context, test model.Test) (model.TryResult, error) {
	if m.isClosed() {
		return model.TryResult{}, ErrShuttingDown
	}
	sc, err := scenario.Parse([]byte(test.YAML))
	if err != nil {
		return model.TryResult{}, newValidationError(err)
	}
	opts := trial.Options{Allow: m.allow, ColumnsKnown: true}
	if sc.Data != nil {
		opts.ColumnsKnown = false
		name := filepath.Base(sc.Data.File)
		if test.ID == "" {
			opts.DataNote = fmt.Sprintf("the test is not saved yet, so its data file %q is not available. Save the test and upload the file to use its first row.", name)
		} else {
			feeder, err := m.loadFeeder(ctx, test, sc)
			var ve *ValidationError
			switch {
			case errors.As(err, &ve):
				opts.DataNote = strings.TrimPrefix(strings.Join(ve.Details, "; "), "data.file: ")
			case err != nil:
				opts.DataNote = fmt.Sprintf("data file %q could not be loaded", name)
				m.log.Warn("try: load data file", "test", test.ID, "error", err)
			default:
				if c, ok := feeder.(*data.CSV); ok {
					opts.Vars, opts.ColumnsKnown = c.First(), true
				}
			}
		}
	}
	return trial.Run(ctx, sc, opts), nil
}
