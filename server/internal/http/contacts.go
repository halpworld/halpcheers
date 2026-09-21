package http

import "net/http"

// ContactsHandler handles Phase 2 /v1/contacts routes.
// Stubs only during Phase 1.
type ContactsHandler struct{}

func NewContactsHandler() *ContactsHandler {
	return &ContactsHandler{}
}

func (h *ContactsHandler) GetContacts(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *ContactsHandler) UpdateContacts(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *ContactsHandler) DeleteContacts(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}
