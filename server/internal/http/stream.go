package http

import "net/http"

// StreamHandler handles /v1/stream and /v1/pending.
// Owned by the SSE track (#17).
type StreamHandler struct{}

func NewStreamHandler() *StreamHandler {
	return &StreamHandler{}
}

func (h *StreamHandler) Stream(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *StreamHandler) Pending(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}
