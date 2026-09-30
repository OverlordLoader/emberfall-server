package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/OverlordLoader/emberfall-server/internal/anticheat"
)

// POST /v1/alliances {world_id, name, tag} -> {alliance_id}
func (s *Server) handleCreateAlliance(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorldID int    `json:"world_id"`
		Name    string `json:"name"`
		Tag     string `json:"tag"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if err := anticheat.CheckName(body.Name); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := anticheat.CheckTag(body.Tag); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, err := s.store.CreateAlliance(r.Context(), body.WorldID, playerIDFrom(r), body.Name, body.Tag)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alliance_id": id})
}

// POST /v1/alliances/{id}/join
func (s *Server) handleJoinAlliance(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "bad alliance id")
		return
	}
	if err := s.store.JoinAlliance(r.Context(), id, playerIDFrom(r)); err != nil {
		writeError(w, http.StatusBadRequest, "join_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// POST /v1/alliances/{id}/leave
func (s *Server) handleLeaveAlliance(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "bad alliance id")
		return
	}
	if err := s.store.LeaveAlliance(r.Context(), id, playerIDFrom(r)); err != nil {
		writeError(w, http.StatusBadRequest, "leave_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// GET /v1/alliances/{id}
func (s *Server) handleGetAlliance(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "bad alliance id")
		return
	}
	a, err := s.store.GetAlliance(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "alliance not found")
		return
	}
	writeJSON(w, http.StatusOK, a)
}
