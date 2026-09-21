package core

import "context"

// Enqueuer defines the contract for submitting a PingJob to the asynchronous
// coalescing and dispatch pipeline.
//
// INVARIANT 2:
// Enqueue MUST be strictly non-blocking (completing in ~100 ns).
// If the bounded channel is full, Enqueue drops the job, increments the dropped
// metric counter, and returns false.
// The caller (the HTTP handler) MUST still return HTTP 202 Accepted to the sender
// (AGENTS.md Invariant 7: enforcement is invisible to the sender).
type Enqueuer interface {
	// Enqueue attempts to place the ping job onto the bounded queue.
	// Returns true if queued, false if dropped due to queue capacity.
	Enqueue(job PingJob) bool
}

// Resolver resolves a target identifier provided in the POST /v1/ping/{target} URL path.
//
// The target may be a Handle (e.g. "e7k4p2m9qx3v") or an Alias (e.g. "@kenth").
// Both are handled identically by Resolver.
//
// INVARIANT 7:
// Resolution occurs inside the send path so probing costs the same as sending.
// There is deliberately NO public resolve endpoint (docs/DISCOVERY.md).
type Resolver interface {
	// Resolve maps target to the recipient's AccountID and canonical Handle.
	// ok is false if target does not exist or is inactive.
	Resolve(ctx context.Context, target string) (accountID AccountID, handle Handle, ok bool)
}

// Guard evaluates ingress rate limits and abuse prevention rules on the hot path.
//
// It checks the sender's account token bucket, the recipient's inbound rate bucket,
// and the (sender, recipient) pair deduplication filter cascade.
//
// INVARIANTS:
// - Hot path execution is O(1) in-memory with zero disk I/O and zero network calls.
// - Returns true if the ping is allowed, false if rejected/deduped.
// - Rejection is invisible to the sender (returns HTTP 202 regardless).
type Guard interface {
	// Allow determines whether a ping from sender to target is permitted.
	Allow(ctx context.Context, sender AccountID, target Handle, targetKind HandleKind) bool
}
