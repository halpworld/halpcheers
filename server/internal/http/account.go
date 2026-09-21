package http

import "net/http"

// AccountsHandler handles /v1/accounts, /v1/session, and /v1/account* routes.
// Owned by the Accounts track (#9).
type AccountsHandler struct{}

func NewAccountsHandler() *AccountsHandler {
	return &AccountsHandler{}
}

func (h *AccountsHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *AccountsHandler) CreateSession(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *AccountsHandler) DeleteSession(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *AccountsHandler) GetAccount(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *AccountsHandler) ExportAccount(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *AccountsHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}
