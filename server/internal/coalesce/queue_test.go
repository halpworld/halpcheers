package coalesce_test

import (
	"testing"
	"time"

	"github.com/halpworld/halpcheers/server/internal/coalesce"
	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/obs"
)

func TestQueueBasicOperations(t *testing.T) {
	metrics := obs.NewMetrics()
	q := coalesce.NewQueue(2, metrics)

	if q.Cap() != 2 {
		t.Fatalf("expected capacity 2, got %d", q.Cap())
	}
	if q.Len() != 0 {
		t.Fatalf("expected length 0, got %d", q.Len())
	}

	job1 := core.PingJob{Recipient: 1, SenderAccount: 10, EnqueuedAt: time.Now()}
	job2 := core.PingJob{Recipient: 2, SenderAccount: 20, EnqueuedAt: time.Now()}
	job3 := core.PingJob{Recipient: 3, SenderAccount: 30, EnqueuedAt: time.Now()}

	if !q.Enqueue(job1) {
		t.Fatalf("expected job 1 to be enqueued")
	}
	if !q.Enqueue(job2) {
		t.Fatalf("expected job 2 to be enqueued")
	}

	if q.Len() != 2 {
		t.Fatalf("expected length 2, got %d", q.Len())
	}

	// Third job should be dropped due to capacity ceiling
	if q.Enqueue(job3) {
		t.Fatalf("expected job 3 to be dropped")
	}

	// Drain
	recv1 := <-q.Channel()
	if recv1.Recipient != 1 {
		t.Fatalf("expected recipient 1, got %d", recv1.Recipient)
	}
	recv2 := <-q.Channel()
	if recv2.Recipient != 2 {
		t.Fatalf("expected recipient 2, got %d", recv2.Recipient)
	}

	if q.Len() != 0 {
		t.Fatalf("expected length 0 after drain, got %d", q.Len())
	}
}
