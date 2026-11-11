package notify

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// maxErrLen caps the error text put in a message.
const maxErrLen = 200

// runText is the Slack message for a finished run, for example
//
//	GoLoad: run FAILED — checkout · 1,234 req · p95 45 ms · errors 2.1% · https://goload.example.com/runs/ID
func (n *Notifier) runText(r model.Run) string {
	parts := []string{"GoLoad: run " + strings.ToUpper(string(r.Status)) + " — " + slackEscape(r.TestName)}
	parts = append(parts, summaryParts(r.Summary)...)
	if r.Error != "" {
		parts = append(parts, "error: "+slackEscape(truncate(r.Error, maxErrLen)))
	}
	if n.base != "" {
		parts = append(parts, n.base+"/runs/"+r.ID)
	}
	return strings.Join(parts, " · ")
}

// suiteText is the Slack message for a finished suite run: a headline and
// one line per item.
func (n *Notifier) suiteText(s model.SuiteRun) string {
	head := fmt.Sprintf("GoLoad: suite %s — %s · %d %s", strings.ToUpper(string(s.Status)), slackEscape(s.SuiteName),
		len(s.Items), plural(len(s.Items), "test", "tests"))
	if s.Error != "" {
		head += " · error: " + slackEscape(truncate(s.Error, maxErrLen))
	}
	if n.base != "" {
		head += " · " + n.base + "/suite-runs/" + s.ID
	}
	lines := []string{head}
	for _, it := range s.Items {
		line := "• " + slackEscape(it.TestName) + ": " + it.Status
		if extra := summaryParts(it.Summary); len(extra) > 0 {
			line += " (" + strings.Join(extra, " · ") + ")"
		}
		if it.Error != "" {
			line += " — " + slackEscape(truncate(it.Error, maxErrLen))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// summaryParts formats the headline numbers of a summary; nil gives none.
func summaryParts(s *model.Summary) []string {
	if s == nil {
		return nil
	}
	return []string{
		formatInt(s.Requests) + " req",
		"p95 " + formatMs(s.P95Ms),
		"errors " + strconv.FormatFloat(s.ErrorRate*100, 'f', 1, 64) + "%",
	}
}

func formatMs(ms float64) string {
	if ms >= 100 {
		return strconv.FormatFloat(ms, 'f', 0, 64) + " ms"
	}
	return strconv.FormatFloat(ms, 'f', 1, 64) + " ms"
}

// formatInt writes n with thousands separators: 1234567 -> "1,234,567".
func formatInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// slackEscape escapes the three characters Slack treats specially.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
