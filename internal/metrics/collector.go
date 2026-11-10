package metrics

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
)

// maxShards caps the number of collector shards. Each shard holds its own
// per-step histograms (~140KB each), so the cap bounds memory on very large
// machines while still giving each P its own lock in practice.
const maxShards = 16

// shardData is the pending (not yet flushed) state of one shard. Only
// per-step stats are kept on the hot path; the Total is built by merging
// the steps at flush time, which halves the work done per Record.
type shardData struct {
	byStep map[string]*Stats
}

func newShardData() *shardData { return &shardData{byStep: map[string]*Stats{}} }

// mergeInto adds d's non-empty steps to snap.ByStep and snap.Total and
// reports whether d held any results.
func (d *shardData) mergeInto(snap *Snapshot) bool {
	found := false
	for name, st := range d.byStep {
		if st.Requests == 0 {
			continue
		}
		found = true
		dst, ok := snap.ByStep[name]
		if !ok {
			dst = NewStats()
			snap.ByStep[name] = dst
		}
		dst.Merge(st)
		snap.Total.Merge(st)
	}
	return found
}

// reset empties d in place, keeping map entries and histogram allocations
// so the next interval records without allocating.
func (d *shardData) reset() {
	for _, st := range d.byStep {
		st.reset()
	}
}

// shard is one mutex-protected slice of the collector's pending state.
// It is padded to its own cache lines to avoid false sharing.
type shard struct {
	mu sync.Mutex
	d  *shardData
	_  [48]byte
}

// Collector aggregates results from many goroutines into per-interval
// snapshots plus a cumulative snapshot. It is safe for concurrent use.
//
// Record is the hot path. Pending state is split across up to
// min(GOMAXPROCS, 16) mutex-protected shards; a sync.Pool hands goroutines
// a shard with per-P affinity, so concurrent Record calls rarely contend
// on the same lock and Record does not allocate (except the first time a
// step name is seen on a shard).
type Collector struct {
	shards   []*shard
	affinity sync.Pool // of *shard
	next     atomic.Uint32
	dropped  atomic.Int64

	mu     sync.Mutex // serializes Flush and Cumulative; guards fields below
	start  time.Time
	last   time.Time // time of the last Flush (start initially)
	cum    *Snapshot
	spares []*shardData // per-shard empty data swapped in on Flush
}

// NewCollector returns a collector whose first interval and cumulative
// window start at start.
func NewCollector(start time.Time) *Collector {
	n := runtime.GOMAXPROCS(0)
	if n > maxShards {
		n = maxShards
	}
	if n < 1 {
		n = 1
	}
	c := &Collector{
		shards: make([]*shard, n),
		spares: make([]*shardData, n),
		start:  start,
		last:   start,
		cum:    newSnapshot(start, start),
	}
	for i := range c.shards {
		c.shards[i] = &shard{d: newShardData()}
		c.spares[i] = newShardData()
	}
	return c
}

// pick returns a shard for the calling goroutine, preferring the one
// cached for the current P.
func (c *Collector) pick() *shard {
	if sh, ok := c.affinity.Get().(*shard); ok {
		return sh
	}
	return c.shards[c.next.Add(1)%uint32(len(c.shards))]
}

// Record adds one step result. Latency is clamped to [1us, 60s]; results
// with a non-nil Err count as errors.
func (c *Collector) Record(r protocol.Result) {
	us := latencyMicros(r.Latency)
	sh := c.pick()
	sh.mu.Lock()
	st, ok := sh.d.byStep[r.Step]
	if !ok {
		st = NewStats()
		sh.d.byStep[r.Step] = st
	}
	st.record(us, r.Bytes, r.Err != nil)
	sh.mu.Unlock()
	c.affinity.Put(sh)
}

// RecordDropped counts one arrival that was dropped (e.g. MaxVUs reached).
func (c *Collector) RecordDropped() {
	c.dropped.Add(1)
}

// Flush returns the snapshot of everything recorded since the previous
// Flush (or NewCollector), covering [last flush, now), and folds it into
// the cumulative state. The returned snapshot is owned by the caller.
func (c *Collector) Flush(now time.Time) *Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	iv := newSnapshot(c.last, now)
	for i, sh := range c.shards {
		spare := c.spares[i]
		sh.mu.Lock()
		d := sh.d
		sh.d = spare
		sh.mu.Unlock()
		// Merging happens outside the shard lock so Record is never
		// blocked behind histogram iteration.
		d.mergeInto(iv)
		d.reset()
		c.spares[i] = d
	}
	iv.Dropped = c.dropped.Swap(0)

	c.cum.Merge(iv)
	c.cum.Start = c.start
	if now.After(c.cum.End) {
		c.cum.End = now
	}
	if now.After(c.last) {
		c.last = now
	}
	return iv
}

// Cumulative returns a deep copy of everything flushed so far plus any
// pending (unflushed) results, without resetting the pending state.
//
// Start is the collector's start time. End is the last Flush time, unless
// unflushed results or drops exist, in which case it is the current wall
// clock time (but never earlier than the last Flush).
func (c *Collector) Cumulative() *Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := c.cum.Clone()
	out.Start = c.start
	out.End = c.last
	pending := false
	for _, sh := range c.shards {
		sh.mu.Lock()
		if sh.d.mergeInto(out) {
			pending = true
		}
		sh.mu.Unlock()
	}
	if d := c.dropped.Load(); d > 0 {
		out.Dropped += d
		pending = true
	}
	if pending {
		if now := time.Now(); now.After(out.End) {
			out.End = now
		}
	}
	return out
}
