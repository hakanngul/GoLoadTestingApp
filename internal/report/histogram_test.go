package report

import (
	"context"
	"testing"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"

	"github.com/hakanngul/GoLoadTestingApp/internal/metrics"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

func TestRunReportHistogramCached(t *testing.T) {
	st := store.NewMemory()
	tt := newTest(t, st, "hist")
	run, _, _ := seedRun(t, st, tt.ID, 1, "v1")
	rp := New(st)
	ctx := context.Background()

	first, err := rp.RunReport(ctx, run.ID, Steady, DefaultTolerance)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Histogram) == 0 {
		t.Fatal("no histogram")
	}
	var total int64
	for _, b := range first.Histogram {
		total += b.Count
	}
	if total != first.Summary.Requests {
		t.Fatalf("histogram counts %d != requests %d", total, first.Summary.Requests)
	}
	// The second call is served from the cache and must carry the histogram too.
	second, err := rp.RunReport(ctx, run.ID, Steady, DefaultTolerance)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Histogram) != len(first.Histogram) || second.Histogram[0] != first.Histogram[0] {
		t.Fatalf("cached histogram differs: %v vs %v", second.Histogram, first.Histogram)
	}
	second.Histogram[0].Count = -1 // must not alias the cache
	third, _ := rp.RunReport(ctx, run.ID, Steady, DefaultTolerance)
	if third.Histogram[0].Count == -1 {
		t.Fatal("cache aliased")
	}
}

func TestBucketHistogramEmpty(t *testing.T) {
	if got := BucketHistogram(nil, 32); got != nil {
		t.Fatalf("nil histogram: %v", got)
	}
	if got := BucketHistogram(metrics.NewStats().Hist, 32); got != nil {
		t.Fatalf("empty histogram: %v", got)
	}
}

func TestBucketBarsSingleValue(t *testing.T) {
	got := bucketBars([]hdrhistogram.Bar{{From: 5000, To: 5000, Count: 7}}, 32)
	if len(got) != 1 || got[0].Count != 7 || got[0].FromMs != 5 || got[0].ToMs != 5 {
		t.Fatalf("got %+v", got)
	}
}

func TestBucketBarsTrimsAndConserves(t *testing.T) {
	// Counts at 1ms, 10ms and 1000ms; empty bars at both ends are ignored.
	bars := []hdrhistogram.Bar{
		{From: 500, To: 999, Count: 0},
		{From: 1000, To: 1000, Count: 5},
		{From: 10000, To: 10000, Count: 3},
		{From: 1_000_000, To: 1_000_000, Count: 2},
		{From: 2_000_000, To: 3_000_000, Count: 0},
	}
	got := bucketBars(bars, 30)
	if len(got) < 2 || len(got) > 30 {
		t.Fatalf("buckets: %d", len(got))
	}
	var total int64
	for i, b := range got {
		total += b.Count
		if b.ToMs <= b.FromMs {
			t.Fatalf("bucket %d not increasing: %+v", i, b)
		}
		if i > 0 && b.FromMs < got[i-1].ToMs-1e-9 {
			t.Fatalf("bucket %d overlaps previous", i)
		}
	}
	if total != 10 {
		t.Fatalf("count not conserved: %d", total)
	}
	if got[0].Count == 0 || got[len(got)-1].Count == 0 {
		t.Fatalf("edges must be non-empty: %+v %+v", got[0], got[len(got)-1])
	}
	if got[0].FromMs < 0.999 || got[0].FromMs > 1.001 || got[len(got)-1].ToMs < 999 || got[len(got)-1].ToMs > 1001 {
		t.Fatalf("range %v..%v", got[0].FromMs, got[len(got)-1].ToMs)
	}
	again := bucketBars(bars, 30)
	for i := range got {
		if got[i] != again[i] {
			t.Fatal("not deterministic")
		}
	}
}

func TestBucketHistogramFromHDR(t *testing.T) {
	st := metrics.NewStats()
	for i := 1; i <= 1000; i++ {
		if err := st.Hist.RecordValue(int64(i) * 1000); err != nil { // i ms in us
			t.Fatal(err)
		}
	}
	got := BucketHistogram(st.Hist, HistogramBuckets)
	if len(got) == 0 || len(got) > HistogramBuckets {
		t.Fatalf("buckets: %d", len(got))
	}
	var total int64
	for _, b := range got {
		total += b.Count
	}
	if total != 1000 {
		t.Fatalf("total %d", total)
	}
}
