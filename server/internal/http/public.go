package http

import "net/http"

// PublicHandler handles public, unauthenticated routes (/h/{handle}, /@{alias}, /badge/{handle}.svg, /overlay/{token}).
// Owned by the Public pages track (#19).
type PublicHandler struct{}

func NewPublicHandler() *PublicHandler {
	return &PublicHandler{}
}

func (h *PublicHandler) HandleLanding(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *PublicHandler) AliasLanding(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *PublicHandler) BadgeSVG(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *PublicHandler) Overlay(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}
