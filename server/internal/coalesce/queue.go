package coalesce

import (
	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/obs"
)

// Queue is the bounded in-memory channel for in-flight PingJobs.
//
// INVARIANT 2 (AGENTS.md Invariant 2 & docs/ARCHITECTURE.md § Loss policy):
// Enqueue is strictly non-blocking (~100 ns). It never awaits a push service,
// a disk write, or a DNS lookup.
// If the queue is full, the job is dropped immediately, the dropped counter
// is incremented with reason="queue_full", and Enqueue returns false.
// The caller (POST /v1/ping) still returns HTTP 202 Accepted (Invariant 7).
type Queue struct {
	ch      chan core.PingJob
	metrics *obs.Metrics
}

// NewQueue creates a new bounded queue with capacity fixed at startup.
// AGENTS.md Invariant 8: Sized from config at startup (default 65,536).
func NewQueue(capacity int, metrics *obs.Metrics) *Queue {
	if capacity <= 0 {
		capacity = 65536
	}
	return &Queue{
		ch:      make(chan core.PingJob, capacity),
		metrics: metrics,
	}
}

// Enqueue attempts a non-blocking push onto the bounded channel.
// Returns true if queued, false if dropped due to capacity ceiling.
func (q *Queue) Enqueue(job core.PingJob) bool {
	select {
	case q.ch <- job:
		return true
	default:
		if q.metrics != nil {
			q.metrics.IncPingsDropped(obs.DropReasonQueueFull)
		}
		return false
	}
}

// Channel returns the receive-only channel drained by coalescing workers.
func (q *Queue) Channel() <-chan core.PingJob {
	return q.ch
}

// Cap returns the channel capacity.
func (q *Queue) Cap() int {
	return cap(q.ch)
}

// Len returns the current buffered job count.
func (q *Queue) Len() int {
	return len(q.ch)
}
