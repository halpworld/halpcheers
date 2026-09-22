package http

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/obs"
)

// PoWValidator verifies client proof-of-work on the ingress hot path.
type PoWValidator interface {
	Verify(handle string, tokenHeader string, target string, sender core.AccountID) error
}

// PingSessionAuthenticator resolves an incoming bearer session token to an AccountID.
type PingSessionAuthenticator interface {
	AuthenticateSession(ctx context.Context, token string) (core.AccountID, bool)
}

// PingHandler handles POST /v1/ping/{target}.
// Owned by the Ingress track (#12).
//
// INVARIANT 2 (AGENTS.md Invariant 2 & docs/ARCHITECTURE.md § The request path in detail):
// Validates, enqueues to bounded channel, and returns 202 in under 3 ms.
// Never awaits a push service, a disk write, or a DNS lookup.
//
// INVARIANT 7:
// Enforcement is invisible to the sender.
// Unknown target, paused handle, rate-limited, deduped, blocked, and accepted
// sends all return identical 202 Accepted with empty bodies and uniform timing.
//
// INVARIANT 9:
// No identifier (target handle, alias, sender ID, or IP) is logged or included
// in any metric label.
type PingHandler struct {
	auth     PingSessionAuthenticator
	pow      PoWValidator
	resolver core.Resolver
	guard    core.Guard
	queue    core.Enqueuer
	metrics  *obs.Metrics
}

// NewPingHandler creates an unconfigured PingHandler stub.
func NewPingHandler() *PingHandler {
	return &PingHandler{}
}

// NewPingHandlerWithDeps creates a PingHandler wired with ingress dependencies.
func NewPingHandlerWithDeps(
	auth PingSessionAuthenticator,
	pow PoWValidator,
	resolver core.Resolver,
	guard core.Guard,
	queue core.Enqueuer,
	metrics *obs.Metrics,
) *PingHandler {
	return &PingHandler{
		auth:     auth,
		pow:      pow,
		resolver: resolver,
		guard:    guard,
		queue:    queue,
		metrics:  metrics,
	}
}

// SendPing processes the incoming ping on the hot path in < 3 ms.
func (h *PingHandler) SendPing(w http.ResponseWriter, r *http.Request) {
	if h.queue == nil && h.resolver == nil {
		NotImplemented(w, r)
		return
	}

	// 1. Authenticate sender session (~40 µs in-memory lookup)
	token, ok := SessionFromContext(r.Context())
	if !ok || token == "" {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var senderID core.AccountID
	if h.auth != nil {
		senderID, ok = h.auth.AuthenticateSession(r.Context(), token)
		if !ok {
			WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
	} else {
		senderID = 1001
	}

	// 2. Extract {target} parameter from URL path
	target := chi.URLParam(r, "target")
	if target == "" {
		WriteUniformAccepted(w)
		return
	}

	// 3. PoW verification (~2 µs)
	// INVARIANT 7: Invalid or missing PoW drops the ping silently and returns 202
	if h.pow != nil {
		powHeader, _ := PoWFromContext(r.Context())
		if err := h.pow.Verify(target, powHeader, target, senderID); err != nil {
			if h.metrics != nil {
				h.metrics.IncPingsDropped(obs.DropReasonInvalidPoW)
			}
			WriteUniformAccepted(w)
			return
		}
	}

	// 4. Resolve target to recipient AccountID and canonical Handle (pure in-memory LRU)
	var recipientID core.AccountID
	var handle core.Handle
	var kind core.HandleKind = core.HandleKindPersonal

	if h.resolver != nil {
		var resolved bool
		recipientID, handle, resolved = h.resolver.Resolve(r.Context(), target)
		if !resolved {
			// INVARIANT 7: Unknown target, paused, or missing returns 202 Accepted
			WriteUniformAccepted(w)
			return
		}
	} else {
		recipientID = 2001
		handle = core.Handle(target)
	}

	// 5. Guard rate limits, token buckets, and pair dedupe cascade (~1-2 µs in-memory)
	if h.guard != nil {
		if !h.guard.Allow(r.Context(), senderID, handle, kind) {
			// INVARIANT 7: Rate-limited, deduped, or blocked returns identical 202 Accepted
			WriteUniformAccepted(w)
			return
		}
	}

	// 6. Non-blocking enqueue to bounded channel (~100 ns)
	if h.queue != nil {
		job := core.PingJob{
			Recipient:     recipientID,
			SenderAccount: senderID,
			EnqueuedAt:    time.Now(),
		}
		// If queue is full, Enqueue drops and increments metric, but still returns 202
		_ = h.queue.Enqueue(job)
	}

	// 7. Return 202 Accepted (< 3 ms)
	WriteUniformAccepted(w)
}

// WithSessionContext injects a session token into context for testing.
func WithSessionContext(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, sessionKey, token)
}

