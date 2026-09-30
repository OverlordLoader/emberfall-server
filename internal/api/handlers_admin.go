package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/OverlordLoader/emberfall-server/internal/sim"
	"github.com/OverlordLoader/emberfall-server/internal/store"
)

// Admin endpoints (phase 1): spawn world, grant items, ban player.
// Gated by ADMIN_TOKEN; the token is a bearer secret, never a player role.

// POST /v1/admin/worlds {name, size?, max_players?} -> {id}
func (s *Server) handleAdminCreateWorld(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string `json:"name"`
		Size       int    `json:"size"`
		MaxPlayers int    `json:"max_players"`
	}
	_ = decodeJSON(r, &body)
	if body.Name == "" {
		body.Name = "New Realm"
	}
	if body.Size <= 0 {
		body.Size = 256
	}
	if body.MaxPlayers <= 0 {
		body.MaxPlayers = 2500
	}
	cfgJSON, _ := json.Marshal(sim.DefaultConfig())
	var id int
	err := s.store.DB.QueryRow(r.Context(),
		`INSERT INTO worlds(name, status, size, max_players, config) VALUES($1,'open',$2,$3,$4) RETURNING id`,
		body.Name, body.Size, body.MaxPlayers, cfgJSON).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "create failed")
		return
	}
	if err := s.store.SeedFrostNodes(r.Context(), id, 200); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "node seeding failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// POST /v1/admin/grant {player_id, items:{key:qty}} -> {}
func (s *Server) handleAdminGrant(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PlayerID string         `json:"player_id"`
		Items    map[string]int `json:"items"`
	}
	if err := decodeJSON(r, &body); err != nil || body.PlayerID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "player_id and items required")
		return
	}
	err := s.store.TxFunc(r.Context(), func(ctx context.Context, tx store.Querier) error {
		var worldID int
		if err := tx.QueryRow(ctx,
			`SELECT world_id FROM cities WHERE player_id=$1 ORDER BY created_at LIMIT 1`, body.PlayerID).Scan(&worldID); err != nil {
			return err
		}
		return s.store.GrantItems(ctx, tx, body.PlayerID, worldID, body.Items)
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "grant_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// POST /v1/admin/ban {player_id, banned} -> {}
func (s *Server) handleAdminBan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PlayerID string `json:"player_id"`
		Banned   bool   `json:"banned"`
	}
	if err := decodeJSON(r, &body); err != nil || body.PlayerID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "player_id required")
		return
	}
	if err := s.store.SetBanned(r.Context(), body.PlayerID, body.Banned); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "ban failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}
