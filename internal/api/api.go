// Package api is the HTTP layer: REST handlers under /v1, the WebSocket hub,
// and middleware (auth, rate limits, idempotency, logging).
package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/OverlordLoader/emberfall-server/internal/auth"
	"github.com/OverlordLoader/emberfall-server/internal/store"
)

// Config carries server tunables from env.
type Config struct {
	JWTSecret     string
	AppleServices string
	AllowDevAuth  bool
	AdminToken    string
}

// Server wires the router, store, auth, and WS hub.
type Server struct {
	store    *store.Store
	signer   *auth.Signer
	verifier *auth.Verifier
	cfg      Config
	hub      *Hub
}

// New builds the server and its routes.
func New(st *store.Store, cfg Config) *Server {
	s := &Server{
		store:    st,
		signer:   auth.NewSigner(cfg.JWTSecret),
		verifier: auth.NewVerifier(cfg.AppleServices),
		cfg:      cfg,
	}
	s.hub = NewHub(st)
	return s
}

// Router returns the chi router with all routes mounted.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.logging)
	r.Use(s.cors)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/healthz", s.handleHealth)

	r.Route("/v1", func(r chi.Router) {
		// Auth (no bearer required).
		r.Post("/auth/apple", s.handleAuthApple)
		r.Post("/auth/refresh", s.handleAuthRefresh)
		if s.cfg.AllowDevAuth {
			r.Post("/auth/dev", s.handleAuthDev)
		}

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Use(s.rateLimitMiddleware("rest", 60, time.Minute))

			r.Get("/worlds", s.handleListWorlds)
			r.Post("/worlds/{id}/join", s.idempotent(s.handleJoinWorld))
			r.Get("/worlds/{id}/config", s.handleWorldConfig)

			r.Get("/cities/{id}", s.handleGetCity)
			r.Post("/cities/{id}/build", s.idempotent(s.expensive(s.handleBuild)))
			r.Post("/cities/{id}/train", s.idempotent(s.expensive(s.handleTrain)))
			r.Post("/build-queue/{id}/cancel", s.idempotent(s.handleCancelBuild))
			r.Post("/build-queue/{id}/speedup", s.idempotent(s.handleSpeedupBuild))

			r.Post("/marches", s.idempotent(s.expensive(s.handleLaunchMarch)))
			r.Get("/marches", s.handleListMarches)
			r.Post("/marches/{id}/recall", s.idempotent(s.handleRecallMarch))

			r.Get("/map", s.handleMap)
			r.Get("/reports", s.handleReports)
			r.Get("/sync", s.handleSync)

			r.Get("/commanders", s.handleListCommanders)
			r.Post("/commanders/summon", s.idempotent(s.expensive(s.handleSummon)))

			r.Get("/quests", s.handleQuests)
			r.Post("/quests/{id}/claim", s.idempotent(s.handleClaimQuest))

			r.Get("/welcome", s.handleWelcome)
			r.Post("/welcome/claim", s.idempotent(s.handleClaimWelcome))

			r.Get("/inventory", s.handleInventory)
			r.Post("/inventory/use", s.idempotent(s.handleUseItem))

			r.Post("/alliances", s.idempotent(s.expensive(s.handleCreateAlliance)))
			r.Post("/alliances/{id}/join", s.idempotent(s.handleJoinAlliance))
			r.Post("/alliances/{id}/leave", s.idempotent(s.handleLeaveAlliance))
			r.Get("/alliances/{id}", s.handleGetAlliance)
			r.Get("/chat", s.handleChatHistory)

			// Admin (gated by ADMIN_TOKEN, not by player auth role).
			r.Route("/admin", func(r chi.Router) {
				r.Use(s.requireAdmin)
				r.Post("/worlds", s.handleAdminCreateWorld)
				r.Post("/grant", s.handleAdminGrant)
				r.Post("/ban", s.handleAdminBan)
			})
		})

		r.Get("/ws", s.handleWS)
	})
	return r
}

// Hub returns the WS hub (for the scheduler/main to publish through).
func (s *Server) Hub() *Hub { return s.hub }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

// Config returns the server config (used at startup for warnings).
func (s *Server) Config() Config { return s.cfg }
