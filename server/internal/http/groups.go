package http

import "net/http"

// GroupsHandler handles Phase 2 /v1/groups* routes.
// Stubs only during Phase 1.
type GroupsHandler struct{}

func NewGroupsHandler() *GroupsHandler {
	return &GroupsHandler{}
}

func (h *GroupsHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *GroupsHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *GroupsHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *GroupsHandler) JoinGroup(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *GroupsHandler) LeaveGroup(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *GroupsHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *GroupsHandler) RotateInvite(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *GroupsHandler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}

func (h *GroupsHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}
