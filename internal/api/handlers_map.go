package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/OverlordLoader/emberfall-server/internal/store"
)

// GET /v1/map?world=&x1&y1&x2&y2 -> tiles + visible cities/marches
func (s *Server) handleMap(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	worldID, _ := strconv.Atoi(q.Get("world"))
	x1, _ := strconv.Atoi(q.Get("x1"))
	y1, _ := strconv.Atoi(q.Get("y1"))
	x2, _ := strconv.Atoi(q.Get("x2"))
	y2, _ := strconv.Atoi(q.Get("y2"))
	if worldID == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "world required")
		return
	}
	if x1 > x2 {
		x1, x2 = x2, x1
	}
	if y1 > y2 {
		y1, y2 = y2, y1
	}
	mb, err := s.store.GetMapBBox(r.Context(), playerIDFrom(r), worldID, x1, y1, x2, y2, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "map failed")
		return
	}
	writeJSON(w, http.StatusOK, mb)
}

// GET /v1/reports?cursor= -> battle_reports (cursor = ISO timestamp)
func (s *Server) handleReports(w http.ResponseWriter, r *http.Request) {
	cursor := r.URL.Query().Get("cursor")
	reports, next, err := s.store.ListReports(r.Context(), playerIDFrom(r), cursor, 25)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "reports failed")
		return
	}
	if reports == nil {
		reports = []store.BattleReport{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": reports, "next_cursor": next})
}

// GET /v1/sync?since=<iso> -> deltas
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	sinceStr := r.URL.Query().Get("since")
	since := time.Now().Add(-24 * time.Hour)
	if sinceStr != "" {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = t
		}
	}
	deltas, err := s.store.SyncDeltas(r.Context(), playerIDFrom(r), since, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "sync failed")
		return
	}
	writeJSON(w, http.StatusOK, deltas)
}

// GET /v1/chat?channel=&limit= -> recent messages (WS is the live path)
func (s *Server) handleChatHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	channel := q.Get("channel")
	if channel == "" {
		channel = "global"
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	worldID, _ := strconv.Atoi(q.Get("world"))
	if worldID == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "world required")
		return
	}
	msgs, err := s.store.ChatHistory(r.Context(), worldID, channel, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "chat failed")
		return
	}
	if msgs == nil {
		msgs = []store.ChatMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}
