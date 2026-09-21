package http

import "net/http"

// SettingsHandler handles /v1/settings and /v1/handles/{handle}/report-abuse.
// Owned by the Settings track (#18).
type SettingsHandler struct{}

func NewSettingsHandler() *SettingsHandler {
	return &SettingsHandler{}
}

func (h *SettingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *SettingsHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *SettingsHandler) ReportAbuse(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}
