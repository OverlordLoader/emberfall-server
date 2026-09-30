package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/OverlordLoader/emberfall-server/internal/anticheat"
	"github.com/OverlordLoader/emberfall-server/internal/sim"
	"github.com/OverlordLoader/emberfall-server/internal/store"
)

// GET /v1/worlds -> [{id, name, status, players}]
func (s *Server) handleListWorlds(w http.ResponseWriter, r *http.Request) {
	worlds, err := s.store.ListWorlds(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	if worlds == nil {
		worlds = []store.World{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"worlds": worlds})
}

// POST /v1/worlds/{id}/join -> {city_id} (idempotent per player+world)
func (s *Server) handleJoinWorld(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "bad world id")
		return
	}
	pid := playerIDFrom(r)
	cityID, created, err := s.store.JoinWorld(r.Context(), id, pid)
	if err != nil {
		writeError(w, http.StatusBadRequest, "join_failed", err.Error())
		return
	}
	if created {
		s.publishNotification(id, pid, "welcome", "Your city has been founded. Build, train, and march!")
	}
	writeJSON(w, http.StatusOK, map[string]any{"city_id": cityID, "created": created})
}

// GET /v1/worlds/{id}/config -> theme-tunable game config JSON
func (s *Server) handleWorldConfig(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "bad world id")
		return
	}
	cfg, err := s.store.WorldConfig(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "world not found")
		return
	}
	raw, _ := json.Marshal(cfg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// GET /v1/cities/{id} -> full city with lazily-computed resources
func (s *Server) handleGetCity(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cf, err := s.store.GetCityFull(r.Context(), id, time.Now())
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "city not found")
		return
	}
	if cf.City.PlayerID != playerIDFrom(r) {
		writeError(w, http.StatusForbidden, "forbidden", "not your city")
		return
	}
	writeJSON(w, http.StatusOK, cf)
}

// POST /v1/cities/{id}/build {type, slot} -> {queue_id, ends_at}
func (s *Server) handleBuild(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Type string `json:"type"`
		Slot int    `json:"slot"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Type == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "type required")
		return
	}
	if err := s.ownCity(r, id); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	qid, endsAt, err := s.store.StartBuild(r.Context(), id, body.Type, body.Slot, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, "build_failed", err.Error())
		return
	}
	s.store.BumpQuestProgress(r.Context(), s.store.DB, playerIDFrom(r), s.worldOf(r, id), []string{"build_2"}, time.Now(), 1)
	s.publishCityDelta(s.worldOf(r, id), id)
	writeJSON(w, http.StatusOK, map[string]any{"queue_id": qid, "ends_at": endsAt.UTC()})
}

// POST /v1/build-queue/{id}/cancel -> {}
func (s *Server) handleCancelBuild(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.CancelBuild(r.Context(), id, time.Now()); err != nil {
		writeError(w, http.StatusBadRequest, "cancel_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// POST /v1/build-queue/{id}/speedup {item_key?} -> {ends_at}
func (s *Server) handleSpeedupBuild(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		ItemKey string `json:"item_key"`
	}
	_ = decodeJSON(r, &body)
	endsAt, err := s.store.SpeedupBuild(r.Context(), id, playerIDFrom(r), body.ItemKey, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, "speedup_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ends_at": endsAt.UTC()})
}

// POST /v1/cities/{id}/train {troops:{...}, tier} -> {queue_id, ends_at}
func (s *Server) handleTrain(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Troops sim.TroopCounts `json:"troops"`
		Tier   int             `json:"tier"`
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
	if err := s.ownCity(r, id); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	qid, endsAt, err := s.store.StartTrain(r.Context(), id, body.Troops, body.Tier, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, "train_failed", err.Error())
		return
	}
	wid := s.worldOf(r, id)
	s.store.BumpQuestProgress(r.Context(), s.store.DB, playerIDFrom(r), wid, []string{"train_50"}, time.Now(), int(body.Troops.Total()))
	writeJSON(w, http.StatusOK, map[string]any{"queue_id": qid, "ends_at": endsAt.UTC()})
}

// ownCity verifies the city belongs to the request's player.
func (s *Server) ownCity(r *http.Request, cityID string) error {
	owner, _, err := s.store.CityOwner(r.Context(), s.store.DB, cityID)
	if err != nil {
		return err
	}
	if owner != playerIDFrom(r) {
		return errNotOwner
	}
	return nil
}

var errNotOwner = errNotOwnerT{}

type errNotOwnerT struct{}

func (errNotOwnerT) Error() string { return "not your city" }

// worldOf returns the world id for a city (0 on error; used for quest/hub hints).
func (s *Server) worldOf(r *http.Request, cityID string) int {
	_, wid, err := s.store.CityOwner(r.Context(), s.store.DB, cityID)
	if err != nil {
		return 0
	}
	return wid
}
