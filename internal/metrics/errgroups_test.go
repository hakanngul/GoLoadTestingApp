package metrics

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

func fixedClock(start time.Time) func() time.Time {
	var mu sync.Mutex
	t := start
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t = t.Add(time.Millisecond)
		return t
	}
}

func TestErrorGrouperGroupsAndSorts(t *testing.T) {
	g := NewErrorGrouper(0, fixedClock(t0))
	g.Record("ok", 200, nil) // successes are ignored
	for i := 0; i < 3; i++ {
		g.Record("login", 500, errors.New("status 500"))
	}
	g.Record("home", 0, errors.New("dial tcp: refused"))
	g.Record("buy", 503, errors.New("status 503"))
	g.Record("buy", 503, errors.New("status 503"))
	g.Record("buy", 404, errors.New("status 404"))

	got := g.Snapshot()
	want := []struct {
		step   string
		status int
		count  int64
	}{{"login", 500, 3}, {"buy", 503, 2}, {"buy", 404, 1}, {"home", 0, 1}}
	if len(got) != len(want) {
		t.Fatalf("groups = %+v", got)
	}
	for i, w := range want {
		if got[i].Step != w.step || got[i].Status != w.status || got[i].Count != w.count {
			t.Fatalf("group %d = %+v, want %+v", i, got[i], w)
		}
	}
	if l := got[0]; !l.First.Equal(t0.Add(time.Millisecond)) || !l.Last.Equal(t0.Add(3*time.Millisecond)) {
		t.Fatalf("first/last = %v / %v", l.First, l.Last)
	}
	if g.Total() != 7 {
		t.Fatalf("total = %d", g.Total())
	}
}

func TestErrorGrouperTruncatesMessages(t *testing.T) {
	g := NewErrorGrouper(0, nil)
	long := strings.Repeat("é", 400) // 2 bytes per rune
	g.Record("s", 500, errors.New(long+"A"))
	g.Record("s", 500, errors.New(long+"B")) // same first 300 runes
	got := g.Snapshot()
	if len(got) != 1 || got[0].Count != 2 {
		t.Fatalf("groups = %d (%+v)", len(got), got)
	}
	if n := utf8.RuneCountInString(got[0].Message); n != ErrorMessageMaxRunes || !utf8.ValidString(got[0].Message) {
		t.Fatalf("message has %d runes", n)
	}
	if ErrorMessage(errors.New("short")) != "short" || ErrorMessage(nil) != "" {
		t.Fatal("ErrorMessage")
	}
}

func TestErrorGrouperCapAndOverflow(t *testing.T) {
	g := NewErrorGrouper(3, nil)
	for i := 0; i < 10; i++ {
		for j := 0; j <= i; j++ {
			g.Record("s", 500, fmt.Errorf("err %d", i))
		}
	}
	got := g.Snapshot()
	if len(got) != 4 {
		t.Fatalf("groups = %+v", got)
	}
	// The first three distinct errors keep their groups (counts 1..3);
	// all later ones go to the overflow group, which is listed last.
	if got[0].Message != "err 2" || got[0].Count != 3 || got[2].Message != "err 0" || got[2].Count != 1 {
		t.Fatalf("groups = %+v", got)
	}
	other := got[3]
	if other.Message != model.ErrorGroupOther || other.Step != "" || other.Status != 0 || other.Count != 55-6 {
		t.Fatalf("overflow = %+v", other)
	}
	if g.Total() != 55 {
		t.Fatalf("total = %d", g.Total())
	}
	// A known group still counts normally once the cap is reached.
	g.Record("s", 500, errors.New("err 0"))
	// err 0 and err 1 now both have 2: ties sort by message.
	if got := g.Snapshot(); got[1].Message != "err 0" || got[1].Count != 2 || len(got) != 4 {
		t.Fatalf("after cap: %+v", got)
	}
}

func TestErrorGrouperMergeAndDrain(t *testing.T) {
	w1 := NewErrorGrouper(2, fixedClock(t0))
	w2 := NewErrorGrouper(2, fixedClock(t0.Add(time.Hour)))
	w1.Record("a", 500, errors.New("x"))
	w1.Record("a", 500, errors.New("x"))
	w1.Record("b", 0, errors.New("y"))
	w1.Record("c", 0, errors.New("z")) // w1 overflow
	w2.Record("a", 500, errors.New("x"))
	w2.Record("d", 1, errors.New("q"))
	w2.Record("e", 1, errors.New("r")) // w2 overflow

	master := NewErrorGrouper(3, nil)
	d1 := w1.Drain()
	master.Merge(d1)
	master.Merge(w2.Drain())
	master.Merge([]model.ErrorGroup{{Step: "a", Status: 500, Message: "x", Count: 0}}) // ignored

	if len(w1.Snapshot()) != 0 || w1.Total() != 0 {
		t.Fatal("Drain did not reset")
	}
	got := master.Snapshot()
	// a/500/x: 3; b: 1; d: 1 (third distinct); overflow: 1 (w1) + 1 (w2).
	if len(got) != 4 {
		t.Fatalf("merged = %+v", got)
	}
	if got[0].Step != "a" || got[0].Count != 3 || !got[0].First.Equal(t0.Add(time.Millisecond)) ||
		!got[0].Last.Equal(t0.Add(time.Hour+time.Millisecond)) {
		t.Fatalf("a = %+v", got[0])
	}
	if got[1].Step != "b" || got[2].Step != "d" {
		t.Fatalf("order = %+v", got)
	}
	if o := got[3]; o.Message != model.ErrorGroupOther || o.Count != 2 {
		t.Fatalf("overflow = %+v", o)
	}
	if master.Total() != 7 {
		t.Fatalf("total = %d", master.Total())
	}
	// Drained groups are copies.
	d1[0].Count = 999
	if master.Snapshot()[0].Count != 3 {
		t.Fatal("aliasing")
	}
}

func TestErrorGrouperConcurrent(t *testing.T) {
	g := NewErrorGrouper(5, nil)
	merged := NewErrorGrouper(5, nil)
	const goroutines, per = 8, 2000
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < per; j++ {
				g.Record(fmt.Sprintf("s%d", j%10), 500, errors.New("boom"))
				if j%100 == 0 {
					_ = g.Snapshot()
				}
			}
		}(i)
	}
	stop := make(chan struct{})
	var dw sync.WaitGroup
	dw.Add(1)
	go func() { // a consumer draining per "second", like a worker
		defer dw.Done()
		for {
			select {
			case <-stop:
				return
			default:
				merged.Merge(g.Drain())
			}
		}
	}()
	wg.Wait()
	close(stop)
	dw.Wait()
	merged.Merge(g.Drain())
	if merged.Total() != goroutines*per {
		t.Fatalf("total = %d, want %d", merged.Total(), goroutines*per)
	}
}
