package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/OverlordLoader/emberfall-server/internal/anticheat"
	"github.com/OverlordLoader/emberfall-server/internal/sim"
	"github.com/OverlordLoader/emberfall-server/internal/store"
)

// POST /v1/marches {origin_city_id, troops, tx, ty, purpose} -> {march_id, departs_at, arrives_at}
func (s *Server) handleLaunchMarch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OriginCityID string          `json:"origin_city_id"`
		Troops       sim.TroopCounts `json:"troops"`
		Tier         int             `json:"tier"`
		TX           int             `json:"tx"`
		TY           int             `json:"ty"`
		Purpose      string          `json:"purpose"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if body.Tier == 0 {
		body.Tier = 1
	}
	if err := anticheat.CheckTroopCounts(body.Troops); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := s.ownCity(r, body.OriginCityID); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	wid := s.worldOf(r, body.OriginCityID)
	var size int
	if err := s.store.DB.QueryRow(r.Context(), `SELECT size FROM worlds WHERE id=$1`, wid).Scan(&size); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "bad world")
		return
	}
	if err := anticheat.CheckCoords(body.TX, body.TY, size); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	marchID, departsAt, arrivesAt, err := s.store.LaunchMarch(
		r.Context(), playerIDFrom(r), body.OriginCityID,
		body.Troops, body.Tier, body.TX, body.TY, body.Purpose, time.Now())
	if err != nil {
		if store.IsPvPPhase2(err) {
			writeError(w, http.StatusConflict, "pvp_phase2",
				"City-vs-city attacks arrive in phase 2. Frost nodes are fair game now.")
			return
		}
		writeError(w, http.StatusBadRequest, "march_failed", err.Error())
		return
	}
	s.store.BumpQuestProgress(r.Context(), s.store.DB, playerIDFrom(r), wid, []string{"march_1"}, time.Now(), 1)
	_ = s.store.PublishWorld(r.Context(), wid, map[string]any{
		"type": "march_update", "march_id": marchID,
		"x1": body.TX - 2, "y1": body.TY - 2, "x2": body.TX + 2, "y2": body.TY + 2,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"march_id": marchID, "departs_at": departsAt.UTC(), "arrives_at": arrivesAt.UTC(),
	})
}

// GET /v1/marches -> own active marches
func (s *Server) handleListMarches(w http.ResponseWriter, r *http.Request) {
	marches, err := s.store.ListMarches(r.Context(), playerIDFrom(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	if marches == nil {
		marches = []store.March{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"marches": marches})
}

// POST /v1/marches/{id}/recall -> {}
func (s *Server) handleRecallMarch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.RecallMarch(r.Context(), playerIDFrom(r), id, time.Now()); err != nil {
		writeError(w, http.StatusBadRequest, "recall_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}
