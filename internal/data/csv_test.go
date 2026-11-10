package data

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

func writeCSV(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "d.csv")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const threeRows = "user,pass\na,1\nb,2\nc,3\n"

func TestNewCSVErrors(t *testing.T) {
	tests := []struct {
		name, content string
		mode          scenario.DataMode
		wantErr       string
	}{
		{"empty file", "", scenario.DataSequential, "empty"},
		{"header only", "user,pass\n", scenario.DataSequential, "no data rows"},
		{"ragged row", "user,pass\na,1\nb\n", scenario.DataSequential, "wrong number of fields"},
		{"empty column", "user,\na,1\n", scenario.DataSequential, "is empty"},
		{"duplicate column", "user,user\na,1\n", scenario.DataSequential, "duplicate"},
		{"bad mode", threeRows, "shuffle", "unknown mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewCSV(writeCSV(t, tt.content), tt.mode)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
	if _, err := NewCSV(filepath.Join(t.TempDir(), "missing.csv"), ""); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestCSVHeaderAndBOM(t *testing.T) {
	f, err := NewCSV(writeCSV(t, "\uFEFF user , pass\n\"x,y\",2\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	row, err := f.Next()
	if err != nil {
		t.Fatal(err)
	}
	if row["user"] != "x,y" || row["pass"] != "2" || len(row) != 2 {
		t.Fatalf("row = %v", row)
	}
	c := f.(*CSV)
	if c.Len() != 1 || strings.Join(c.Columns(), "|") != "user|pass" {
		t.Fatalf("Len=%d Columns=%v", c.Len(), c.Columns())
	}
}

func TestCSVSequential(t *testing.T) {
	for _, mode := range []scenario.DataMode{"", scenario.DataSequential} {
		f, err := NewCSV(writeCSV(t, threeRows), mode)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for range 7 {
			row, err := f.Next()
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, row["user"])
		}
		if s := strings.Join(got, ""); s != "abcabca" {
			t.Fatalf("mode %q: order = %s", mode, s)
		}
	}
}

func TestCSVReturnsFreshCopies(t *testing.T) {
	f, err := NewCSV(writeCSV(t, "user\na\n"), scenario.DataSequential)
	if err != nil {
		t.Fatal(err)
	}
	r1, _ := f.Next()
	r1["user"] = "mutated"
	r1["token"] = "t"
	r2, _ := f.Next()
	if r2["user"] != "a" || len(r2) != 1 {
		t.Fatalf("mutation leaked into next row: %v", r2)
	}
}

func TestCSVRandom(t *testing.T) {
	f, err := NewCSV(writeCSV(t, threeRows), scenario.DataRandom)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for range 300 {
		row, err := f.Next()
		if err != nil {
			t.Fatal(err)
		}
		seen[row["user"]]++
	}
	for _, u := range []string{"a", "b", "c"} {
		if seen[u] == 0 {
			t.Fatalf("row %q never chosen in 300 draws: %v", u, seen)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("unexpected rows: %v", seen)
	}
}

func TestCSVUniqueExhaustion(t *testing.T) {
	f, err := NewCSV(writeCSV(t, threeRows), scenario.DataUnique)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a", "b", "c"} {
		row, err := f.Next()
		if err != nil || row["user"] != want {
			t.Fatalf("got %v, %v; want %s", row, err, want)
		}
	}
	for range 3 {
		if _, err := f.Next(); !errors.Is(err, ErrExhausted) {
			t.Fatalf("err = %v, want ErrExhausted", err)
		}
	}
}

// TestCSVConcurrent runs every mode from many goroutines; run with -race.
func TestCSVConcurrent(t *testing.T) {
	var b strings.Builder
	b.WriteString("id\n")
	const n = 1000
	for i := range n {
		b.WriteString("id_" + strconv.Itoa(i) + "\n")
	}
	path := writeCSV(t, b.String())

	for _, mode := range []scenario.DataMode{scenario.DataSequential, scenario.DataRandom, scenario.DataUnique} {
		t.Run(string(mode), func(t *testing.T) {
			f, err := NewCSV(path, mode)
			if err != nil {
				t.Fatal(err)
			}
			const workers, perWorker = 16, 100 // 1600 calls > 1000 rows
			var (
				mu        sync.Mutex
				seen      = map[string]int{}
				exhausted int
				wg        sync.WaitGroup
			)
			for range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range perWorker {
						row, err := f.Next()
						if errors.Is(err, ErrExhausted) {
							mu.Lock()
							exhausted++
							mu.Unlock()
							continue
						}
						if err != nil {
							t.Error(err)
							return
						}
						row["mutate"] = "ok" // callers own the map
						mu.Lock()
						seen[row["id"]]++
						mu.Unlock()
					}
				}()
			}
			wg.Wait()

			total := workers * perWorker
			switch mode {
			case scenario.DataUnique:
				if len(seen) != n || exhausted != total-n {
					t.Fatalf("unique: %d distinct rows, %d exhausted; want %d and %d", len(seen), exhausted, n, total-n)
				}
				for id, c := range seen {
					if c != 1 {
						t.Fatalf("row %s handed out %d times", id, c)
					}
				}
			case scenario.DataSequential:
				// 1600 calls over 1000 rows: every row at least once, at most twice.
				if len(seen) != n {
					t.Fatalf("sequential: %d distinct rows, want %d", len(seen), n)
				}
				for id, c := range seen {
					if c < 1 || c > 2 {
						t.Fatalf("row %s handed out %d times", id, c)
					}
				}
			case scenario.DataRandom:
				if exhausted != 0 {
					t.Fatalf("random mode must never exhaust")
				}
			}
		})
	}
}

func TestExampleUsersCSV(t *testing.T) {
	f, err := NewCSV(filepath.Join("..", "..", "examples", "users.csv"), scenario.DataUnique)
	if err != nil {
		t.Fatal(err)
	}
	row, err := f.Next()
	if err != nil {
		t.Fatal(err)
	}
	if row["username"] == "" || row["password"] == "" {
		t.Fatalf("example row missing username/password: %v", row)
	}
}

func BenchmarkCSVNext(b *testing.B) {
	p := filepath.Join(b.TempDir(), "d.csv")
	if err := os.WriteFile(p, []byte("a,b,c,d\n1,2,3,4\n5,6,7,8\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	f, err := NewCSV(p, scenario.DataSequential)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := f.Next(); err != nil {
				b.Fatal(err)
			}
		}
	})
}
