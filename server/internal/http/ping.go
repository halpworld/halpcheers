package http

import "net/http"

// PingHandler handles POST /v1/ping/{target}.
// Owned by the Ingress track (#12).
//
// INVARIANT 2:
// Validates, enqueues to bounded channel, and returns 202 in under 3 ms.
// Never awaits a push service, a disk write, or a DNS lookup.
type PingHandler struct{}

func NewPingHandler() *PingHandler {
	return &PingHandler{}
}

func (h *PingHandler) SendPing(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}
