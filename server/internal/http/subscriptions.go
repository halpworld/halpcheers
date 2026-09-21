package http

import "net/http"

// SubscriptionsHandler handles /v1/subscriptions*.
// Owned by the Dispatch track (#16).
type SubscriptionsHandler struct{}

func NewSubscriptionsHandler() *SubscriptionsHandler {
	return &SubscriptionsHandler{}
}

func (h *SubscriptionsHandler) CreateSubscription(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *SubscriptionsHandler) DeleteSubscription(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}
