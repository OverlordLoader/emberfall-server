// Command emberfalld is the Emberfall Kingdom game server: a single binary
// serving REST + WebSocket on :8080, running the 1s event sweeper, applying
// embedded Postgres migrations at startup.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/OverlordLoader/emberfall-server/internal/api"
	"github.com/OverlordLoader/emberfall-server/internal/scheduler"
	"github.com/OverlordLoader/emberfall-server/internal/store"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pgURL := env("DATABASE_URL", "postgres://emberfall:emberfall@postgres:5432/emberfall?sslmode=disable")
	redisURL := env("REDIS_URL", "redis:6379")
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET is required")
	}

	st, err := store.New(ctx, pgURL, redisURL)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	log.Println("applying migrations...")
	if err := st.RunMigrations(ctx); err != nil {
		log.Fatalf("migrations: %v", err)
	}
	worldID, err := st.EnsureDefaultWorld(ctx)
	if err != nil {
		log.Fatalf("default world: %v", err)
	}
	log.Printf("default world id=%d", worldID)
	if err := st.SeedFrostNodes(ctx, worldID, 200); err != nil {
		log.Printf("seed nodes: %v", err)
	}

	srv := api.New(st, api.Config{
		JWTSecret:     jwtSecret,
		AppleServices: env("APPLE_SERVICES_ID", ""),
		AllowDevAuth:  os.Getenv("ALLOW_DEV_AUTH") == "1",
		AdminToken:    os.Getenv("ADMIN_TOKEN"),
	})
	if srv.Config().AllowDevAuth {
		log.Println("WARNING: ALLOW_DEV_AUTH=1 — dev auth is enabled. Never enable in production.")
	}

	// Event-driven tick: 1s sweeper.
	sweep := scheduler.New(st)
	go sweep.Run(ctx)

	addr := env("LISTEN_ADDR", ":8080")
	httpSrv := &http.Server{Addr: addr, Handler: srv.Router(),
		ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("emberfalld listening on %s", addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	log.Println("shutdown complete")
}
