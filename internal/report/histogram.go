package report

import (
	"math"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// HistogramBuckets is the number of log-spaced buckets of the latency
// distribution in a report (fewer after empty edges are trimmed).
const HistogramBuckets = 32

// BucketHistogram condenses an HDR histogram (microseconds) into at most n
// log-spaced buckets between the smallest and the largest recorded value,
// in milliseconds. Leading and trailing empty buckets are dropped; empty
// ones in between are kept so the shape stays honest. It returns nil for an
// empty (or nil) histogram. Deterministic and linear in the histogram size.
func BucketHistogram(h *hdrhistogram.Histogram, n int) []model.HistBucket {
	if h == nil || h.TotalCount() == 0 {
		return nil
	}
	return bucketBars(h.Distribution(), n)
}

// bucketBars is the pure core of BucketHistogram. Bars are ordered by value;
// each is assigned, whole, to the bucket containing its lower bound.
func bucketBars(bars []hdrhistogram.Bar, n int) []model.HistBucket {
	if n < 1 {
		n = 1
	}
	first, last := -1, -1
	for i, b := range bars {
		if b.Count <= 0 {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	if first < 0 {
		return nil
	}
	lo := math.Max(float64(bars[first].From), 1)
	hi := math.Max(float64(bars[last].To), lo)
	if hi == lo {
		var c int64
		for _, b := range bars[first : last+1] {
			c += max(b.Count, 0)
		}
		return []model.HistBucket{{FromMs: lo / 1000, ToMs: hi / 1000, Count: c}}
	}
	span := math.Log(hi / lo)
	counts := make([]int64, n)
	for _, b := range bars[first : last+1] {
		if b.Count <= 0 {
			continue
		}
		idx := int(float64(n) * math.Log(math.Max(float64(b.From), lo)/lo) / span)
		idx = min(max(idx, 0), n-1)
		counts[idx] += b.Count
	}
	lf, ll := 0, n-1
	for lf < n && counts[lf] == 0 {
		lf++
	}
	for ll >= 0 && counts[ll] == 0 {
		ll--
	}
	edge := func(i int) float64 { return lo * math.Exp(span*float64(i)/float64(n)) / 1000 }
	out := make([]model.HistBucket, 0, ll-lf+1)
	for i := lf; i <= ll; i++ {
		out = append(out, model.HistBucket{FromMs: edge(i), ToMs: edge(i + 1), Count: counts[i]})
	}
	return out
}
