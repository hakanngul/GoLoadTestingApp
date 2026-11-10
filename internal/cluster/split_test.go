package cluster

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

func stages(targets ...int) []scenario.Stage {
	out := make([]scenario.Stage, len(targets))
	for i, t := range targets {
		out[i] = scenario.Stage{Duration: scenario.Duration{Duration: time.Duration(i+1) * time.Second}, Target: t}
	}
	return out
}

func TestShareLoad(t *testing.T) {
	tests := []struct {
		name    string
		load    scenario.Load
		count   int
		targets [][]int // per worker, per stage
		maxVUs  []int
	}{
		{
			name:    "even open",
			load:    scenario.Load{Model: scenario.ModelOpen, Stages: stages(300, 600), MaxVUs: 1000},
			count:   3,
			targets: [][]int{{100, 200}, {100, 200}, {100, 200}},
			maxVUs:  []int{334, 333, 333},
		},
		{
			name:    "remainder to lowest indices",
			load:    scenario.Load{Model: scenario.ModelOpen, Stages: stages(10, 0, 5), MaxVUs: 2},
			count:   3,
			targets: [][]int{{4, 0, 2}, {3, 0, 2}, {3, 0, 1}},
			maxVUs:  []int{1, 1, 1}, // min 1
		},
		{
			name:    "tiny target leaves workers at zero",
			load:    scenario.Load{Model: scenario.ModelOpen, Stages: stages(1), MaxVUs: 1000},
			count:   3,
			targets: [][]int{{1}, {0}, {0}},
			maxVUs:  []int{334, 333, 333},
		},
		{
			name:    "closed VUs",
			load:    scenario.Load{Model: scenario.ModelClosed, Stages: stages(7, 20)},
			count:   2,
			targets: [][]int{{4, 10}, {3, 10}},
			maxVUs:  []int{0, 0}, // unset stays unset
		},
		{
			name:    "single worker unchanged",
			load:    scenario.Load{Model: scenario.ModelOpen, Stages: stages(7), MaxVUs: 9},
			count:   1,
			targets: [][]int{{7}},
			maxVUs:  []int{9},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sums := make([]int, len(tt.load.Stages))
			for i := 0; i < tt.count; i++ {
				got := ShareLoad(tt.load, i, tt.count)
				if got.Model != tt.load.Model {
					t.Errorf("worker %d: model %q", i, got.Model)
				}
				if got.MaxVUs != tt.maxVUs[i] {
					t.Errorf("worker %d: MaxVUs = %d, want %d", i, got.MaxVUs, tt.maxVUs[i])
				}
				for s, st := range got.Stages {
					if st.Target != tt.targets[i][s] {
						t.Errorf("worker %d stage %d: target = %d, want %d", i, s, st.Target, tt.targets[i][s])
					}
					if st.Duration != tt.load.Stages[s].Duration {
						t.Errorf("worker %d stage %d: duration changed", i, s)
					}
					sums[s] += st.Target
				}
			}
			for s, sum := range sums {
				if sum != tt.load.Stages[s].Target {
					t.Errorf("stage %d: shares sum to %d, want %d", s, sum, tt.load.Stages[s].Target)
				}
			}
		})
	}
}

func TestShareLoadDoesNotAlias(t *testing.T) {
	l := scenario.Load{Stages: stages(10)}
	got := ShareLoad(l, 0, 2)
	got.Stages[0].Target = 99
	if l.Stages[0].Target != 10 {
		t.Fatal("ShareLoad modified the input stages")
	}
}

func TestShareLoadSumsExactly(t *testing.T) {
	for total := 0; total <= 50; total++ {
		for count := 1; count <= 7; count++ {
			sum := 0
			for i := 0; i < count; i++ {
				sum += ShareLoad(scenario.Load{Stages: stages(total)}, i, count).Stages[0].Target
			}
			if sum != total {
				t.Fatalf("total %d over %d workers sums to %d", total, count, sum)
			}
		}
	}
}

func TestPartitionCSV(t *testing.T) {
	const in = "user,pass\nu0,p0\nu1,p1\nu2,p2\nu3,p3\n\"u,4\",\"multi\nline\"\n"
	tests := []struct {
		index, count int
		rows         [][]string
	}{
		{0, 1, [][]string{{"u0", "p0"}, {"u1", "p1"}, {"u2", "p2"}, {"u3", "p3"}, {"u,4", "multi\nline"}}},
		{0, 2, [][]string{{"u0", "p0"}, {"u2", "p2"}, {"u,4", "multi\nline"}}},
		{1, 2, [][]string{{"u1", "p1"}, {"u3", "p3"}}},
		{2, 3, [][]string{{"u2", "p2"}}},
		{5, 6, nil}, // fewer rows than workers
	}
	for _, tt := range tests {
		out, n, err := PartitionCSV([]byte(in), tt.index, tt.count)
		if err != nil {
			t.Fatalf("%d/%d: %v", tt.index, tt.count, err)
		}
		recs, err := csv.NewReader(bytes.NewReader(out)).ReadAll()
		if err != nil {
			t.Fatalf("%d/%d: output not CSV: %v", tt.index, tt.count, err)
		}
		if strings.Join(recs[0], ",") != "user,pass" {
			t.Errorf("%d/%d: header = %v", tt.index, tt.count, recs[0])
		}
		got := recs[1:]
		if n != len(tt.rows) || len(got) != len(tt.rows) {
			t.Fatalf("%d/%d: %d rows (reported %d), want %d", tt.index, tt.count, len(got), n, len(tt.rows))
		}
		for i := range got {
			if strings.Join(got[i], "|") != strings.Join(tt.rows[i], "|") {
				t.Errorf("%d/%d row %d = %q, want %q", tt.index, tt.count, i, got[i], tt.rows[i])
			}
		}
	}
}

func TestPartitionCSVDisjointAndComplete(t *testing.T) {
	var b strings.Builder
	b.WriteString("id\n")
	const rows = 101
	for i := 0; i < rows; i++ {
		b.WriteString(strings.Repeat("x", i%3) + string(rune('a'+i%26)) + "-" + strconv.Itoa(i) + "\n")
	}
	seen := map[string]int{}
	for idx := 0; idx < 4; idx++ {
		out, _, err := PartitionCSV([]byte(b.String()), idx, 4)
		if err != nil {
			t.Fatal(err)
		}
		recs, _ := csv.NewReader(bytes.NewReader(out)).ReadAll()
		for _, r := range recs[1:] {
			seen[r[0]]++
		}
	}
	if len(seen) != rows {
		t.Fatalf("partitions cover %d distinct rows, want %d", len(seen), rows)
	}
	for k, n := range seen {
		if n != 1 {
			t.Fatalf("row %q appears in %d partitions", k, n)
		}
	}
}

func TestPartitionCSVErrors(t *testing.T) {
	if _, _, err := PartitionCSV(nil, 0, 2); err == nil {
		t.Error("empty input: want error")
	}
	if _, _, err := PartitionCSV([]byte("a\n1\n"), 2, 2); err == nil {
		t.Error("index out of range: want error")
	}
	if _, _, err := PartitionCSV([]byte("a\n\"unterminated\n"), 0, 1); err == nil {
		t.Error("malformed CSV: want error")
	}
}
