package http

import "net/http"

// HandlesHandler handles /v1/handles* and /v1/alias routes.
// Owned by the Handles track (#10).
type HandlesHandler struct{}

func NewHandlesHandler() *HandlesHandler {
	return &HandlesHandler{}
}

func (h *HandlesHandler) ListHandles(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *HandlesHandler) CreateHandle(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *HandlesHandler) UpdateHandle(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *HandlesHandler) DeleteHandle(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *HandlesHandler) SetAlias(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *HandlesHandler) DeleteAlias(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}
