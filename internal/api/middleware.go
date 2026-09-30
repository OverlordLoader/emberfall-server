package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

type ctxKey string

const ctxPlayerID ctxKey = "player_id"

func playerIDFrom(r *http.Request) string {
	v, _ := r.Context().Value(ctxPlayerID).(string)
	return v
}

// requireAuth verifies the Bearer access JWT and stashes the player id.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		claims, err := s.signer.VerifyAccess(strings.TrimPrefix(h, "Bearer "))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
			return
		}
		// Banned players get no game access.
		p, err := s.store.GetPlayer(r.Context(), claims.PlayerID)
		if err != nil || p.Banned {
			writeError(w, http.StatusForbidden, "forbidden", "account not available")
			return
		}
		ctx := context.WithValue(r.Context(), ctxPlayerID, claims.PlayerID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAdmin gates admin endpoints behind the ADMIN_TOKEN env secret.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminToken == "" {
			writeError(w, http.StatusNotFound, "not_found", "admin disabled")
			return
		}
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") || strings.TrimPrefix(h, "Bearer ") != s.cfg.AdminToken {
			writeError(w, http.StatusUnauthorized, "unauthorized", "bad admin token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimitMiddleware enforces a per-player fixed-window rate limit.
func (s *Server) rateLimitMiddleware(bucket string, limit int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pid := playerIDFrom(r)
			if pid == "" {
				// Pre-auth endpoints: key by IP.
				pid = "ip:" + r.RemoteAddr
			}
			ok, retry := s.store.RateLimit(r.Context(), pid, bucket, limit, window)
			if !ok {
				w.Header().Set("Retry-After", formatDur(retry))
				writeError(w, http.StatusTooManyRequests, "rate_limited", "slow down")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// expensive wraps handlers that cost real game resources (10/min bucket).
func (s *Server) expensive(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid := playerIDFrom(r)
		ok, retry := s.store.RateLimit(r.Context(), pid, "expensive", 10, time.Minute)
		if !ok {
			w.Header().Set("Retry-After", formatDur(retry))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "slow down")
			return
		}
		next(w, r)
	}
}

func formatDur(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	return itoa(secs)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// idempotent implements the Idempotency-Key contract: 24h stored responses,
// never re-executed. Redis is the fast path; Postgres is the source of truth.
func (s *Server) idempotent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			next(w, r)
			return
		}
		pid := playerIDFrom(r)
		if pid == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "idempotency requires auth")
			return
		}
		ctx := r.Context()
		// Fast path: Redis.
		if raw, ok := s.store.IdemGet(ctx, pid, key); ok {
			replayStored(w, raw)
			return
		}
		// Source of truth: Postgres.
		if raw, ok := s.store.IdemGetPG(ctx, pid, key); ok {
			s.store.IdemSet(ctx, pid, key, raw)
			replayStored(w, raw)
			return
		}
		// Execute and capture.
		rec := &captureWriter{ResponseWriter: w, status: 200, buf: &bytes.Buffer{}}
		next(rec, r)
		stored, _ := json.Marshal(map[string]any{"status": rec.status, "body": json.RawMessage(rec.buf.Bytes())})
		if rec.status >= 200 && rec.status < 300 {
			// The handler already committed; persist the response best-effort.
			_ = s.store.IdemSetPG(ctx, s.store.DB, pid, key, stored)
			s.store.IdemSet(ctx, pid, key, stored)
		}
	}
}

// captureWriter records status + body for idempotent replay.
type captureWriter struct {
	http.ResponseWriter
	status int
	buf    *bytes.Buffer
}

func (c *captureWriter) WriteHeader(status int) {
	c.status = status
	c.ResponseWriter.WriteHeader(status)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	c.buf.Write(b)
	return c.ResponseWriter.Write(b)
}

func replayStored(w http.ResponseWriter, raw []byte) {
	var env struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "idempotency replay failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Idempotent-Replayed", "true")
	w.WriteHeader(env.Status)
	_, _ = w.Write(env.Body)
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
