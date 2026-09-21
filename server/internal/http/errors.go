package http

import (
	"encoding/json"
	"net/http"
)

// UniformError represents the single, uniform error structure returned across all Halp APIs.
//
// INVARIANT 7 (AGENTS.md Invariant 7 & docs/API.md § Conventions):
// Errors are uniform in shape and timing. Lookups never distinguish "not found"
// from "paused" or "blocked". Rejections return identical bodies and timing
// to prevent attackers from probing or tuning attacks.
type UniformError struct {
	Error string `json:"error"`
}

var (
	// UniformNotFoundBytes is the exact byte-identical response body for not found, paused, or blocked resources.
	UniformNotFoundBytes = []byte("{\"error\":\"not_found\"}\n")

	// NotImplementedBytes is the uniform response body for unmounted feature stubs.
	NotImplementedBytes = []byte("{\"error\":\"not_implemented\"}\n")
)

// WriteUniformNotFound writes the byte-identical 404 response used for missing,
// paused, or blocked handles. It guarantees identical byte representations.
func WriteUniformNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(UniformNotFoundBytes)
}

// WriteUniformAccepted writes the byte-identical 202 Accepted response used for
// accepted, rate-limited, deduped, and blocked pings (AGENTS.md Invariants 2 & 7).
func WriteUniformAccepted(w http.ResponseWriter) {
	w.WriteHeader(http.StatusAccepted)
}

// WriteUniformError writes a uniform JSON error response.
func WriteUniformError(w http.ResponseWriter, status int, errCode string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(UniformError{Error: errCode})
}

// NotImplemented writes the uniform 501 Not Implemented response for stub endpoints.
func NotImplemented(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write(NotImplementedBytes)
}
