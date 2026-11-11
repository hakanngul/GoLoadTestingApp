package main

import (
	"encoding/xml"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// JUnit XML as understood by common CI report publishers.

type junitSuites struct {
	XMLName xml.Name     `xml:"testsuites"`
	Name    string       `xml:"name,attr"`
	Tests   int          `xml:"tests,attr"`
	Fails   int          `xml:"failures,attr"`
	Errors  int          `xml:"errors,attr"`
	Skipped int          `xml:"skipped,attr"`
	Time    string       `xml:"time,attr"`
	Suites  []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name      string      `xml:"name,attr"`
	Tests     int         `xml:"tests,attr"`
	Fails     int         `xml:"failures,attr"`
	Errors    int         `xml:"errors,attr"`
	Skipped   int         `xml:"skipped,attr"`
	Time      string      `xml:"time,attr"`
	Timestamp string      `xml:"timestamp,attr,omitempty"`
	Cases     []junitCase `xml:"testcase"`
	SystemOut string      `xml:"system-out,omitempty"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitProblem `xml:"failure,omitempty"`
	Error     *junitProblem `xml:"error,omitempty"`
	Skipped   *junitSkipped `xml:"skipped,omitempty"`
	SystemOut string        `xml:"system-out,omitempty"`
}

type junitProblem struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

type junitSkipped struct {
	Message string `xml:"message,attr,omitempty"`
}

func junitSecs(d time.Duration) string { return fmt.Sprintf("%.3f", d.Seconds()) }

// buildJUnit converts a final result into JUnit XML structures.
func buildJUnit(r remoteResult) junitSuites {
	suite := junitSuite{Name: r.Name, Timestamp: r.Started.UTC().Format("2006-01-02T15:04:05")}
	var total time.Duration
	if r.Finished != nil {
		total = r.Finished.Sub(r.Started)
	}
	suite.Time = junitSecs(total)
	for _, it := range r.Items {
		name := it.Name
		if name == "" {
			name = "(deleted test)"
		}
		tc := junitCase{Name: name, Classname: r.Name, Time: junitSecs(it.duration()), SystemOut: itemSystemOut(it, r.URL)}
		switch model.RunStatus(it.Status) {
		case model.RunPassed:
		case model.RunFailed:
			ft := it.failedThresholds()
			msg := strings.Join(ft, "; ")
			if msg == "" {
				msg = "run failed"
				if it.Error != "" {
					msg = it.Error
				}
			}
			tc.Failure = &junitProblem{Message: msg, Type: "ThresholdFailed", Text: strings.Join(ft, "\n")}
		case model.RunStopped, model.RunError:
			msg := it.Error
			if msg == "" {
				msg = "run " + it.Status
			}
			tc.Error = &junitProblem{Message: msg, Type: "Run" + strings.ToUpper(it.Status[:1]) + it.Status[1:], Text: msg}
		case model.RunQueued, model.RunRunning:
			// Only after a cancel/timeout: the item never finished.
			tc.Error = &junitProblem{Message: "run did not finish (" + it.Status + ")", Type: "Unfinished", Text: it.Status}
		default: // skipped, pending
			tc.Skipped = &junitSkipped{Message: it.Status}
		}
		suite.Cases = append(suite.Cases, tc)
		suite.Tests++
		switch {
		case tc.Failure != nil:
			suite.Fails++
		case tc.Error != nil:
			suite.Errors++
		case tc.Skipped != nil:
			suite.Skipped++
		}
	}
	// A run-level error with no item to attach it to still has to show up.
	if r.Error != "" && suite.Errors == 0 && suite.Fails == 0 && r.Status != model.RunPassed {
		suite.Cases = append(suite.Cases, junitCase{
			Name: "run", Classname: r.Name, Time: "0.000",
			Error: &junitProblem{Message: r.Error, Type: "RunError", Text: r.Error},
		})
		suite.Tests++
		suite.Errors++
	}
	suite.SystemOut = fmt.Sprintf("%s %q: %s\n%s", r.Kind, r.Name, r.Status, r.URL)
	return junitSuites{
		Name: r.Name, Tests: suite.Tests, Fails: suite.Fails, Errors: suite.Errors,
		Skipped: suite.Skipped, Time: suite.Time, Suites: []junitSuite{suite},
	}
}

func itemSystemOut(it remoteItem, base string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "status: %s\n", it.Status)
	if s := it.Summary; s != nil {
		fmt.Fprintf(&b, "requests: %d\nerrors: %d\nrps: %.1f\nerror_rate: %.2f%%\np50: %s\np95: %s\np99: %s\n",
			s.Requests, s.Errors, s.RPS, s.ErrorRate*100, fmtMs2(s.P50Ms), fmtMs2(s.P95Ms), fmtMs2(s.P99Ms))
	}
	for _, t := range it.Thresholds {
		mark := "PASS"
		if !t.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "threshold %s: %s %s %g (actual %.3g)\n", mark, t.Metric, t.Op, t.Value, t.Actual)
	}
	if it.RunID != "" {
		// base is the suite-run or run URL; item runs live under /runs/.
		fmt.Fprintf(&b, "run: %s\n", runURLFrom(base, it.RunID))
	}
	return b.String()
}

// runURLFrom derives {server}/runs/{id} from a /runs/ or /suite-runs/ URL.
func runURLFrom(url, runID string) string {
	for _, sep := range []string{"/suite-runs/", "/runs/"} {
		if i := strings.LastIndex(url, sep); i >= 0 {
			return url[:i] + "/runs/" + runID
		}
	}
	return url
}

func writeJUnit(path string, r remoteResult) error {
	b, err := xml.MarshalIndent(buildJUnit(r), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte(xml.Header), append(b, '\n')...), 0o644)
}
