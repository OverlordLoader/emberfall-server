package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"nhooyr.io/websocket"

	"github.com/OverlordLoader/emberfall-server/internal/store"
)

// Wire protocol (JSON, one object per frame with a "type" field).
//
// Client -> server:
//   {"type":"subscribe_map","bbox":{"x1":0,"y1":0,"x2":40,"y2":40}}
//   {"type":"unsubscribe_map"}
//   {"type":"chat_send","channel":"global","body":"hello"}
//   {"type":"ping"}
//
// Server -> client:
//   {"type":"city_delta","city_id":"...","patch":{...}}
//   {"type":"tile_delta","tiles":[...],"cities":[...],"marches":[...]}
//   {"type":"march_update","marches":[...]}
//   {"type":"march_resolved","march_id":"..."}
//   {"type":"battle_report","report":{...}}
//   {"type":"chat","message":{...}}
//   {"type":"notification","kind":"...","text":"..."}
//   {"type":"pong"}
//   {"type":"error","code":"...","message":"..."}

// Hub tracks WS connections per world and relays Redis fan-out to them.
type Hub struct {
	store *store.Store
	mu    sync.RWMutex
	// worldID -> conn set
	rooms map[int]map[*Conn]struct{}
	// one redis subscriber per world with at least one connection
	subs map[int]*redisSub
}

type redisSub struct {
	cancel context.CancelFunc
}

type bbox struct {
	X1, Y1, X2, Y2 int
}

// Conn is one client connection.
type Conn struct {
	hub      *Hub
	store    *store.Store
	ws       *websocket.Conn
	playerID string
	worldID  int
	mu       sync.Mutex
	bbox     *bbox
	closed   bool
}

// NewHub builds the hub.
func NewHub(st *store.Store) *Hub {
	return &Hub{store: st, rooms: map[int]map[*Conn]struct{}{}, subs: map[int]*redisSub{}}
}

func (h *Hub) add(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rooms[c.worldID] == nil {
		h.rooms[c.worldID] = map[*Conn]struct{}{}
	}
	h.rooms[c.worldID][c] = struct{}{}
	if _, ok := h.subs[c.worldID]; !ok {
		ctx, cancel := context.WithCancel(context.Background())
		sub := h.store.SubscribeWorld(ctx, c.worldID)
		h.subs[c.worldID] = &redisSub{cancel: cancel}
		go h.relay(ctx, c.worldID, sub)
	}
}

func (h *Hub) remove(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set, ok := h.rooms[c.worldID]; ok {
		delete(set, c)
		if len(set) == 0 {
			delete(h.rooms, c.worldID)
			if sub, ok := h.subs[c.worldID]; ok {
				sub.cancel()
				delete(h.subs, c.worldID)
			}
		}
	}
}

// relay forwards Redis world-channel messages to room connections.
// Per-client filtering happens here: chat and notifications go to everyone in
// the room; targeted notifications carry player_id and are filtered;
// tile_delta / march_update payloads may carry a bbox hint (x1..y2) and are
// only sent to conns whose subscription bbox intersects it.
func (h *Hub) relay(ctx context.Context, worldID int, sub *redis.PubSub) {
	defer sub.Close()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			var evt map[string]any
			if err := json.Unmarshal([]byte(m.Payload), &evt); err != nil {
				continue
			}
			h.broadcast(worldID, evt)
		}
	}
}

// broadcast delivers one event to the room, applying per-conn filters.
func (h *Hub) broadcast(worldID int, evt map[string]any) {
	typ, _ := evt["type"].(string)
	h.mu.RLock()
	conns := make([]*Conn, 0, len(h.rooms[worldID]))
	for c := range h.rooms[worldID] {
		conns = append(conns, c)
	}
	h.mu.RUnlock()
	for _, c := range conns {
		switch typ {
		case "notification":
			// Targeted: only the intended player gets it.
			if pid, _ := evt["player_id"].(string); pid != "" && pid != c.playerID {
				continue
			}
		case "tile_delta", "march_update":
			// Bbox-filtered: need a subscription and an intersection.
			c.mu.Lock()
			bb := c.bbox
			c.mu.Unlock()
			if bb == nil {
				continue
			}
			if ex1, ok := toInt(evt["x1"]); ok {
				ex2, _ := toInt(evt["x2"])
				ey1, _ := toInt(evt["y1"])
				ey2, _ := toInt(evt["y2"])
				if ex2 < bb.X1 || ex1 > bb.X2 || ey2 < bb.Y1 || ey1 > bb.Y2 {
					continue
				}
			}
		case "chat":
			// Alliance channels only reach members.
			if m, ok := evt["message"].(map[string]any); ok {
				if ch, _ := m["channel"].(string); len(ch) > 9 && ch[:9] == "alliance:" {
					aid := atoi(ch[9:])
					a, err := c.store.PlayerAlliance(context.Background(), c.playerID, c.worldID)
					if err != nil || a.ID != aid {
						continue
					}
				}
			}
		}
		c.send(evt)
	}
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

// --- server-initiated publishes (called from handlers / scheduler) ---

// publishNotification sends a notification to one player in a world.
func (s *Server) publishNotification(worldID int, playerID, kind, text string) {
	_ = s.store.PublishWorld(context.Background(), worldID, map[string]any{
		"type": "notification", "kind": kind, "text": text, "player_id": playerID,
	})
}

// publishCityDelta tells subscribers a city changed (they refetch the city).
func (s *Server) publishCityDelta(worldID int, cityID string) {
	if worldID == 0 {
		return
	}
	_ = s.store.PublishWorld(context.Background(), worldID, map[string]any{
		"type": "city_delta", "city_id": cityID,
	})
}

// handleWS upgrades /v1/ws?token=<access_jwt>.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "token query param required")
		return
	}
	claims, err := s.signer.VerifyAccess(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid token")
		return
	}
	// World: from the token if bound, else the player's first city.
	worldID := 0
	if claims.WorldID != nil {
		worldID = *claims.WorldID
	} else {
		var wid int
		if err := s.store.DB.QueryRow(r.Context(),
			`SELECT world_id FROM cities WHERE player_id=$1 ORDER BY created_at LIMIT 1`,
			claims.PlayerID).Scan(&wid); err == nil {
			worldID = wid
		}
	}
	if worldID == 0 {
		writeError(w, http.StatusBadRequest, "no_world", "join a world first (POST /v1/worlds/{id}/join)")
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	c := &Conn{hub: s.hub, store: s.store, ws: ws, playerID: claims.PlayerID, worldID: worldID}
	s.hub.add(c)
	_ = s.store.PresenceAdd(r.Context(), worldID, claims.PlayerID)
	defer func() {
		s.hub.remove(c)
		_ = s.store.PresenceRemove(context.Background(), worldID, claims.PlayerID)
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}()
	// Welcome frame.
	c.send(map[string]any{"type": "welcome", "world_id": worldID, "server_time": time.Now().UTC()})
	ctx := r.Context()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		// WS rate limit: 20 msg/s per player.
		ok, _ := s.store.RateLimit(ctx, claims.PlayerID, "ws", 20, time.Second)
		if !ok {
			c.send(map[string]any{"type": "error", "code": "rate_limited", "message": "slow down"})
			continue
		}
		c.handleMessage(ctx, data)
	}
}

func (c *Conn) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		c.closed = true
	}
}

func (c *Conn) handleMessage(ctx context.Context, data []byte) {
	// bbox has a custom shape; decode in two passes for clarity.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		c.send(map[string]any{"type": "error", "code": "bad_request", "message": "invalid json"})
		return
	}
	var typ string
	_ = json.Unmarshal(raw["type"], &typ)
	switch typ {
	case "ping":
		c.send(map[string]any{"type": "pong", "server_time": time.Now().UTC()})
	case "subscribe_map":
		var bb struct {
			X1 int `json:"x1"`
			Y1 int `json:"y1"`
			X2 int `json:"x2"`
			Y2 int `json:"y2"`
		}
		if err := json.Unmarshal(raw["bbox"], &bb); err != nil {
			c.send(map[string]any{"type": "error", "code": "bad_request", "message": "bbox required"})
			return
		}
		// Clamp bbox to 64 tiles per side (same as REST map).
		if bb.X2-bb.X1 > 64 {
			bb.X2 = bb.X1 + 64
		}
		if bb.Y2-bb.Y1 > 64 {
			bb.Y2 = bb.Y1 + 64
		}
		c.mu.Lock()
		c.bbox = &bbox{X1: bb.X1, Y1: bb.Y1, X2: bb.X2, Y2: bb.Y2}
		c.mu.Unlock()
		// Push the current view immediately.
		mb, err := c.store.GetMapBBox(ctx, c.playerID, c.worldID, bb.X1, bb.Y1, bb.X2, bb.Y2, time.Now())
		if err != nil {
			c.send(map[string]any{"type": "error", "code": "internal", "message": "map failed"})
			return
		}
		c.send(map[string]any{"type": "tile_delta", "tiles": mb.Tiles, "cities": mb.Cities, "marches": mb.Marches})
	case "unsubscribe_map":
		c.mu.Lock()
		c.bbox = nil
		c.mu.Unlock()
		c.send(map[string]any{"type": "ok"})
	case "chat_send":
		var cs struct {
			Channel string `json:"channel"`
			Body    string `json:"body"`
		}
		_ = json.Unmarshal(raw["channel"], &cs.Channel)
		_ = json.Unmarshal(raw["body"], &cs.Body)
		if cs.Channel == "" {
			cs.Channel = "global"
		}
		if err := c.checkChatChannel(ctx, cs.Channel); err != nil {
			c.send(map[string]any{"type": "error", "code": "forbidden", "message": err.Error()})
			return
		}
		msg, err := c.store.PostChat(ctx, c.worldID, cs.Channel, c.playerID, cs.Body)
		if err != nil {
			c.send(map[string]any{"type": "error", "code": "bad_request", "message": err.Error()})
			return
		}
		_ = c.store.PublishWorld(ctx, c.worldID, map[string]any{"type": "chat", "message": msg})
	default:
		c.send(map[string]any{"type": "error", "code": "bad_request", "message": "unknown message type"})
	}
}

// checkChatChannel enforces that alliance:{id} channels match the player's
// actual alliance.
func (c *Conn) checkChatChannel(ctx context.Context, channel string) error {
	if channel == "global" {
		return nil
	}
	if len(channel) > 9 && channel[:9] == "alliance:" {
		a, err := c.store.PlayerAlliance(ctx, c.playerID, c.worldID)
		if err != nil || a.ID != atoi(channel[9:]) {
			return errNotMember
		}
		return nil
	}
	return errBadChannel
}

var (
	errNotMember  = errString("not an alliance member")
	errBadChannel = errString("unknown channel")
)

type errString string

func (e errString) Error() string { return string(e) }

func atoi(s string) int {
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}
