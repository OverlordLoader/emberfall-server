package api

import (
	"net/http"
	"time"

	"github.com/OverlordLoader/emberfall-server/internal/store"
)

// POST /v1/auth/apple {identity_token} -> {access, refresh, player}
// Verifies the Apple JWT vs Apple's JWKS; aud must be our Services ID.
// The Apple `sub` is the stable player key (never email).
func (s *Server) handleAuthApple(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IdentityToken string `json:"identity_token"`
	}
	if err := decodeJSON(r, &body); err != nil || body.IdentityToken == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "identity_token required")
		return
	}
	claims, err := s.verifier.Verify(r.Context(), body.IdentityToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "apple_verify_failed", "Apple token did not verify")
		return
	}
	p, _, err := s.store.GetOrCreatePlayerByAppleSub(r.Context(), claims.Subject, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "player lookup failed")
		return
	}
	if p.Banned {
		writeError(w, http.StatusForbidden, "forbidden", "account not available")
		return
	}
	s.finishAuth(w, r, p)
}

// POST /v1/auth/dev {device_id} -> same shape.
// DEV/TESTFLIGHT ONLY: the route only exists when ALLOW_DEV_AUTH=1.
func (s *Server) handleAuthDev(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID string `json:"device_id"`
	}
	if err := decodeJSON(r, &body); err != nil || body.DeviceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "device_id required")
		return
	}
	p, err := s.store.GetOrCreateDevPlayer(r.Context(), body.DeviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "player lookup failed")
		return
	}
	if p.Banned {
		writeError(w, http.StatusForbidden, "forbidden", "account not available")
		return
	}
	s.finishAuth(w, r, p)
}

// POST /v1/auth/refresh {refresh} -> {access, refresh}
// Rotating refresh: the presented token is consumed (deleted) and replaced.
func (s *Server) handleAuthRefresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Refresh string `json:"refresh"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Refresh == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "refresh required")
		return
	}
	playerID, err := s.store.ConsumeRefresh(r.Context(), body.Refresh)
	if err != nil {
		if store.IsSessionExpired(err) {
			writeError(w, http.StatusUnauthorized, "session_expired", "sign in again")
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid_refresh", "bad refresh token")
		return
	}
	p, err := s.store.GetPlayer(r.Context(), playerID)
	if err != nil || p.Banned {
		writeError(w, http.StatusForbidden, "forbidden", "account not available")
		return
	}
	s.finishAuth(w, r, p)
}

// finishAuth mints an access JWT + refresh session for a verified player.
func (s *Server) finishAuth(w http.ResponseWriter, r *http.Request, p store.Player) {
	// Bind the access token to the player's newest world if they have one.
	var worldID *int
	var wid int
	if err := s.store.DB.QueryRow(r.Context(),
		`SELECT world_id FROM cities WHERE player_id=$1 ORDER BY created_at LIMIT 1`, p.ID).Scan(&wid); err == nil {
		worldID = &wid
	}
	access, err := s.signer.MintAccess(p.ID, worldID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "token mint failed")
		return
	}
	_, refresh, err := s.store.CreateSession(r.Context(), p.ID, 30*24*time.Hour)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "session create failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access":  access,
		"refresh": refresh,
		"player":  map[string]any{"id": p.ID, "display_name": p.DisplayName},
	})
}

// GET /healthz — liveness for Caddy / UptimeRobot.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.DB.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "db_down", "postgres unreachable")
		return
	}
	if err := s.store.Redis.Ping(ctx).Err(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "redis_down", "redis unreachable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
