package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/OverlordLoader/emberfall-server/internal/store"
)

// GET /v1/commanders
func (s *Server) handleListCommanders(w http.ResponseWriter, r *http.Request) {
	cmds, err := s.store.ListCommanders(r.Context(), playerIDFrom(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	if cmds == nil {
		cmds = []store.Commander{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"commanders": cmds})
}

// POST /v1/commanders/summon {count} -> server-side gacha (rates + pity)
func (s *Server) handleSummon(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Count int `json:"count"`
	}
	_ = decodeJSON(r, &body)
	if body.Count == 0 {
		body.Count = 1
	}
	results, err := s.store.SummonCommanders(r.Context(), playerIDFrom(r), body.Count)
	if err != nil {
		writeError(w, http.StatusBadRequest, "summon_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// GET /v1/quests -> today's quests with progress
func (s *Server) handleQuests(w http.ResponseWriter, r *http.Request) {
	quests, err := s.store.QuestStatuses(r.Context(), playerIDFrom(r), time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "quests failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"quests": quests})
}

// POST /v1/quests/{id}/claim
func (s *Server) handleClaimQuest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.ClaimQuest(r.Context(), playerIDFrom(r), id, time.Now()); err != nil {
		writeError(w, http.StatusBadRequest, "claim_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// GET /v1/welcome -> 7-day track
func (s *Server) handleWelcome(w http.ResponseWriter, r *http.Request) {
	track, err := s.store.WelcomeStatus(r.Context(), playerIDFrom(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "welcome failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"track": track})
}

// POST /v1/welcome/claim {day}
func (s *Server) handleClaimWelcome(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Day int `json:"day"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Day == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "day required")
		return
	}
	if err := s.store.ClaimWelcome(r.Context(), playerIDFrom(r), body.Day, time.Now()); err != nil {
		writeError(w, http.StatusBadRequest, "claim_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// GET /v1/inventory
func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	inv, err := s.store.ListInventory(r.Context(), playerIDFrom(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "inventory failed")
		return
	}
	if inv == nil {
		inv = []store.InventoryItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": inv})
}

// POST /v1/inventory/use {item_key, qty}
func (s *Server) handleUseItem(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ItemKey string `json:"item_key"`
		Qty     int    `json:"qty"`
	}
	if err := decodeJSON(r, &body); err != nil || body.ItemKey == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "item_key required")
		return
	}
	result, err := s.store.UseItem(r.Context(), playerIDFrom(r), body.ItemKey, body.Qty, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, "use_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
