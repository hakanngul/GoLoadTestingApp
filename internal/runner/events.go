package runner

import (
	"context"
	"sync"

	"github.com/hakanngul/GoLoadTestingApp/internal/metrics"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// activeRun is the in-memory state of one run between Start and the end
// of its goroutine.
type activeRun struct {
	cancel context.CancelFunc    // cancels the engine context
	remote bool                  // executes on remote workers, not in-process
	groups *metrics.ErrorGrouper // every failure of the run, grouped

	mu       sync.Mutex
	run      model.Run        // latest known record
	stats    *model.LiveStats // latest live stats (nil before the first tick)
	finished bool             // done published; no more subscribers accepted
	subs     map[*subscriber]struct{}
}

// subscriber is one Subscribe call. Its channel is only sent to and
// closed while holding activeRun.mu, so sends never race with close.
type subscriber struct {
	ch chan Event
}

// requestStop cancels the engine; the run then ends with status stopped.
// It reports false if the run has already finished.
func (ar *activeRun) requestStop() bool {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	if ar.finished {
		return false
	}
	ar.cancel()
	return true
}

func (ar *activeRun) subscribe(buffer int) (<-chan Event, func(), error) {
	if buffer < 1 {
		buffer = 1
	}
	ar.mu.Lock()
	defer ar.mu.Unlock()
	if ar.finished {
		return nil, nil, ErrNotRunning
	}
	sub := &subscriber{ch: make(chan Event, buffer)}
	r := ar.run
	sub.ch <- Event{Type: EventStatus, Run: &r} // fresh channel: never blocks
	if ar.stats != nil {
		select {
		case sub.ch <- Event{Type: EventStats, Stats: ar.stats}:
		default: // buffer of 1: the next tick brings fresh stats anyway
		}
	}
	ar.subs[sub] = struct{}{}

	cancel := func() {
		ar.mu.Lock()
		defer ar.mu.Unlock()
		if _, ok := ar.subs[sub]; ok {
			delete(ar.subs, sub)
			close(sub.ch)
		}
	}
	return sub.ch, cancel, nil
}

// publish fans ev out without ever blocking: subscribers whose buffer is
// full simply miss it.
func (ar *activeRun) publish(ev Event) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	for sub := range ar.subs {
		select {
		case sub.ch <- ev:
		default: // slow subscriber: drop
		}
	}
}

// finish records the final run, delivers EventDone to every subscriber
// (evicting the oldest queued event if a buffer is full, so done is never
// lost and the run never blocks) and closes their channels.
func (ar *activeRun) finish(final model.Run) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	ar.finished = true
	ar.run = final
	for sub := range ar.subs {
		r := final
		ev := Event{Type: EventDone, Run: &r}
		select {
		case sub.ch <- ev:
		default:
			// Only this goroutine sends, so after taking one event out
			// there is room for done.
			select {
			case <-sub.ch:
			default:
			}
			select {
			case sub.ch <- ev:
			default:
			}
		}
		close(sub.ch)
		delete(ar.subs, sub)
	}
}

// publishStats records ls as the latest live stats (sent to new
// subscribers) and publishes it. ls must not be mutated afterwards.
func (ar *activeRun) publishStats(ls *model.LiveStats) {
	ar.mu.Lock()
	ar.stats = ls
	ar.mu.Unlock()
	ar.publish(Event{Type: EventStats, Stats: ls})
}

// setRun replaces the latest known record (e.g. queued -> running) and
// publishes it as an EventStatus.
func (ar *activeRun) setRun(r model.Run) {
	ar.mu.Lock()
	ar.run = r
	ar.mu.Unlock()
	ar.publish(Event{Type: EventStatus, Run: &r})
}

// snapshot returns the latest known record.
func (ar *activeRun) snapshot() model.Run {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	return ar.run
}
