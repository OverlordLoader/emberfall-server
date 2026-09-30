# Emberfall Kingdom — Game Server

The authoritative backend for **Emberfall Kingdom** (iOS SwiftUI + SpriteKit client).
One Go binary serves REST + WebSocket on `:8080`, runs the 1-second event sweeper,
and applies embedded Postgres migrations at startup.

**The client is a dumb terminal with nice graphics.** It renders state and sends
intents; the server validates everything, computes resources, and resolves all
battles. The client never computes outcomes.

## Quick start (local dev)

Requirements: Go 1.25+, Docker.

```bash
cp .env.example .env
# edit .env: set POSTGRES_PASSWORD, JWT_SECRET, ALLOW_DEV_AUTH=1 for dev
docker compose up -d --build
curl localhost:8080/healthz   # {"status":"ok"}

# dev login (only with ALLOW_DEV_AUTH=1 — never in production):
curl -X POST localhost:8080/v1/auth/dev -H 'Content-Type: application/json' \
  -d '{"device_id":"dev-henry-1"}'
# -> {"access":"...","refresh":"...","player":{...}}

# join the default world, then fetch your city:
curl -X POST localhost:8080/v1/worlds/1/join -H "Authorization: Bearer $ACCESS"
curl localhost:8080/v1/cities/$CITY_ID -H "Authorization: Bearer $ACCESS"
```

Run the pure-logic sim suite + the deterministic demo:

```bash
go test ./...
go run ./cmd/simtest
```

## Deploying to Hetzner (Henry's exact steps)

Assumes the `emberfall-server` repo is on GitHub under `OverlordLoader`.

**1. Order the box.** Hetzner Cloud → new project → server:
- Location: **nbg1** (Nuremberg, EU — CX lines are EU-only)
- Type: **CX33** — 4 vCPU / 8 GB / 80 GB NVMe (~€8.49/mo ≈ **$10/mo**)
- Image: Ubuntu 24.04, add your SSH key.

**2. DNS.** At your registrar, add an **A record**: `api.emberfall.game` → the box's IPv4.
(Use whatever domain you own; it goes in `.env` as `DOMAIN`.)

**3. First boot.** SSH in and run:

```bash
# Docker
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER && newgrp docker

# Code
git clone https://github.com/OverlordLoader/emberfall-server.git /opt/emberfall
cd /opt/emberfall
cp .env.example .env
nano .env   # DOMAIN, POSTGRES_PASSWORD, JWT_SECRET (openssl rand -base64 48),
            # APPLE_SERVICES_ID, ADMIN_TOKEN, ALLOW_DEV_AUTH=0

# Launch
docker compose up -d --build
docker compose logs -f emberfalld   # watch migrations + "listening on :8080"
curl https://api.emberfall.game/healthz
```

Caddy fetches a Let's Encrypt cert automatically on first request. That's it —
the game is live.

**4. Backups.** The box keeps 7 days of Postgres dumps locally; copy them off-box:

```bash
# on the box — nightly cron:
(crontab -l; echo "0 3 * * * /opt/emberfall/scripts/backup.sh >> /var/log/emberfall-backup.log 2>&1") | crontab -
# then rsync /var/backups/emberfall to Hetzner Object Storage (S3-compatible)
# or any bucket. A 500-player DB is < 100 MB — pennies.
```

Weekly: take a Hetzner snapshot of the whole box in the Cloud console
(~€0.01/GB/mo).

**5. Updates.** `cd /opt/emberfall && git pull && docker compose up -d --build`.
Migrations run automatically at container start.

**6. Monitoring.** Point free [UptimeRobot](https://uptimerobot.com) at
`https://api.emberfall.game/healthz` (5-min checks, alerts to your phone).

## Cost table

| Scale | Infra | ~$/mo |
|---|---|---|
| 100 active players (MVP) | 1× CX33 + snapshots + object storage + domain | **~$12** |
| 1,000 active | same CX33 (or CX43 8vCPU/16GB ~€16) | **~$20** |
| 10,000 active | 2× game boxes (CX43) + dedicated DB box (~€30–85) + LB | **~$70–120** |

A slow-tick game is cheap: no per-frame simulation, tiny JSON deltas, WebSockets
mostly idle. The DB is the first thing that needs its own box, not the game server.

## API

Base `:8080`, all under `/v1`, JSON. Bearer access-JWT on everything except
`/v1/auth/*` and `/healthz`. Mutations accept `Idempotency-Key: <uuid>` (24h
stored responses, never re-executed; replayed responses carry
`Idempotent-Replayed: true`).

| Method & path | What it does |
|---|---|
| `POST /v1/auth/apple` | `{identity_token}` → `{access, refresh, player}` (Apple JWKS verify, aud check, `sub` = player key) |
| `POST /v1/auth/dev` | `{device_id}` → same shape. **DEV/TESTFLIGHT ONLY** — route exists only with `ALLOW_DEV_AUTH=1` |
| `POST /v1/auth/refresh` | `{refresh}` → `{access, refresh}` (rotating: old token is consumed) |
| `GET /v1/worlds` | `[{id, name, status, players}]` |
| `POST /v1/worlds/{id}/join` | `{city_id}` — idempotent per player+world, founds city on a free tile |
| `GET /v1/worlds/{id}/config` | theme-tunable game config JSON (build costs, timers, troop stats) |
| `GET /v1/cities/{id}` | full city; resources computed lazily (`min(cap, amount + rate·Δt)`) |
| `POST /v1/cities/{id}/build` | `{type, slot}` → `{queue_id, ends_at}` (one active build/city in phase 1) |
| `POST /v1/build-queue/{id}/cancel` | `{}` — cancels, refunds 50% |
| `POST /v1/build-queue/{id}/speedup` | `{item_key}` → `{ends_at}` — consumes a speedup item |
| `POST /v1/cities/{id}/train` | `{troops:{infantry,cavalry,archers,siege}, tier:1\|2\|3}` → `{queue_id, ends_at}` |
| `POST /v1/marches` | `{origin_city_id, troops, tx, ty, purpose}` → `{march_id, departs_at, arrives_at}` |
| `GET /v1/marches` | own active marches |
| `POST /v1/marches/{id}/recall` | `{}` — march turns around from its current position |
| `GET /v1/map?world=&x1&y1&x2&y2` | tiles + cities + marches in bbox (details only in scout range) |
| `GET /v1/reports?cursor=` | battle reports, newest first |
| `GET /v1/commanders` | player's commanders |
| `POST /v1/commanders/summon` | `{count}` — **server-side gacha** (rates + pity, never client-rolled) |
| `GET /v1/quests` | today's daily quests with progress |
| `POST /v1/quests/{id}/claim` | `{}` — grants reward when target met |
| `GET /v1/welcome` | 7-day track |
| `POST /v1/welcome/claim` | `{day}` — in-order, missed days claimable late (no punishment) |
| `GET /v1/inventory` | items |
| `POST /v1/inventory/use` | `{item_key, qty}` |
| `GET /v1/sync?since=<iso>` | deltas: cities, marches, reports, inventory |
| `POST /v1/alliances` | `{world_id, name, tag}` → `{alliance_id}` (10/min rate bucket) |
| `POST /v1/alliances/{id}/join` / `/leave` | membership |
| `GET /v1/alliances/{id}` | alliance detail |
| `GET /v1/chat?world=&channel=&limit=` | recent messages (WS is the live path) |
| `WS /v1/ws?token=` | see wire protocol below |
| `GET /healthz` | liveness |

Admin (bearer `ADMIN_TOKEN`, not player auth): `POST /v1/admin/worlds`
(spawn world + seed 200 frost nodes), `POST /v1/admin/grant`
`{player_id, items}`, `POST /v1/admin/ban` `{player_id, banned}`.

### WebSocket wire protocol

One JSON object per frame, `{"type": ...}`.

Client → server: `subscribe_map {bbox:{x1,y1,x2,y2}}`, `unsubscribe_map`,
`chat_send {channel, body}` (`global` or `alliance:{id}`), `ping`.

Server → client: `city_delta {city_id}`, `tile_delta {tiles, cities, marches}`,
`march_update`, `march_resolved`, `battle_report {report}`, `chat {message}`,
`notification {kind, text}` (targeted via `player_id`), `pong`, `error {code, message}`.

Fan-out rides Redis pub/sub channel `world:{id}`; the hub filters per connection
(targeted notifications by player, map deltas by bbox intersection, alliance
chat by membership). Rate limit: 20 msg/s per player.

## Protocol notes (decisions the iOS builder should know)

- **Access JWT**: HS256, 15 min, carries `pid` (+ `wid` when the player has a
  city). Refresh: 30 days, rotating, SHA-256 hashed in `sessions`.
- **Idempotency-Key** is honored on all POSTs. Send a client-generated UUID per
  mutating action; on retry after a network failure the server returns the stored
  response instead of re-executing — no double-spend, ever.
- **Resources are lazy.** The server never ticks them per second; every read
  computes `min(cap, amount + rate·(now − updated_at))`. Spends re-anchor inside
  a transaction.
- **March movement is interpolated.** `departs_at`/`arrives_at` are
  authoritative; clients interpolate locally. The server pushes `march_update`
  on launch; the sweeper resolves arrival exactly once.
- **Troop accounting on arrival.** When a march arrives, survivors/loot are
  applied immediately and the return leg is a visual timer (phase-1
  simplification — documented, not a bug).
- **One active build per city** in phase 1 (keeps the economy readable; second
  builder slot is a trivial config flip later).
- **PvP attacks rejected**: `POST /v1/marches` with `purpose: "attack"` aimed at
  a city tile returns **409 `{error: "pvp_phase2"}`**. Attack frost nodes instead.
- **Inventory is keyed by item key** (`player_id, item_key`), not a numeric
  item id — the contract says `item_key`, so the schema follows the contract.
- **Reports cursor** is an ISO-8601 timestamp (`next_cursor` in the response).
- **Map bbox** is clamped to 64×64 tiles server-side (REST and WS alike).
- **Scout range** = 6 + 2/watchtower level tiles; outside it you see terrain and
  "unknown forces" (no owner name, power, or troop counts).

## Game tuning (data-driven)

`GET /v1/worlds/{id}/config` returns the full tuning: 8 buildings
(town_hall, farm, lumber_mill, quarry, gold_mine, barracks, wall, watchtower),
4 troop types × 3 tiers, costs as `base · 1.45^(level−1)`, battle triangle
(infantry→cavalry→archers→infantry at 1.25×), gacha rates (70/25/5, pity at 40),
daily quests, and the 7-day welcome track. A themed variant = a different config
JSON on the world row + client art — no code fork.

## Architecture

```
cmd/emberfalld        # main: HTTP+WS server, sweeper, migrations at startup
cmd/simtest           # deterministic sim demo (no DB)
internal/
  api/                # REST handlers, WS hub, middleware
  auth/               # Apple JWKS verify, session JWT
  sim/                # PURE game logic (no I/O) — unit-tested
  store/              # pgx queries, redis helpers, embedded migrations
  scheduler/          # 1s event sweeper (SELECT ... FOR UPDATE SKIP LOCKED)
  anticheat/          # legality checks, anomaly flags (no auto-ban)
internal/store/migrations/  # golang-migrate-style SQL (up/down)
```

- **Tick model**: event-driven. `scheduled_events` rows (`build_done`,
  `train_done`, `march_arrive`, `march_return`, `node_respawn`) are claimed by
  the 1s sweeper; battles resolve via `sim.ResolveBattle` (power sums, troop
  triangle, seeded ±10% jitter, plunder) in the same transaction — the same code
  path an admin tool would use.
- **Redis is used for exactly**: WS fan-out (`world:{id}`), presence
  (`presence:{world_id}`), rate-limit buckets (`rl:{player}:{bucket}` —
  60/min REST, 10/min expensive, 20 msg/s WS), sweeper lock
  (`lock:sweeper:{world_id}`), hot-city cache (`city:{id}`, 30s),
  idempotency short-circuit (`idem:{player}:{key}`, 24h).
- **Anti-cheat**: server-side validation on every action (ownership,
  recomputed resources, march-slot caps, target legality), anomaly flags into
  `cheat_flags` for human review, **never auto-ban in phase 1**.
- **Offline progress**: lazy resources + scheduled events mean builds, training,
  and marches resolve while the player is away; `GET /v1/sync?since=` catches
  the client up.

## Phase-2 delta (what changes when PvP lands)

The schema and march code were written so phase 2 needs **no rewrites**, only
additive work:

1. **Flip the PvP gate**: `store.LaunchMarch` currently returns `pvp_phase2`
   when `purpose=attack` targets a city tile. Phase 2 resolves that path
   against the defender's city garrison using the **same**
   `sim.ResolveBattle` (add a `resolveCityAttack` next to `resolveNodeAttack`;
   `marches.target_city_id` already exists).
2. **Shields**: `cities.shield_until` exists and is returned by the API today.
   Phase 2 wires `shield_8h` items (`POST /v1/inventory/use` currently returns
   `shields_activate_in_phase_2`) to set it, and the attack path checks it.
3. **Plunder rules**: the resolver already computes plunder vs carry capacity;
   phase 2 decides how much of a *player's* stockpile is lootable.
4. **Reinforce**: `purpose=reinforce` already stores `target_city_id`; phase 2
   garrisons those troops instead of bouncing them home.
5. **Rallies**: MVP resolves each march independently; combined rallies are new
   (additive) logic.
6. **Monetization**: App Store receipt verification for speedups/shields/summon
   tickets (server-side verify → `GrantItems`).

## What was deliberately deferred

- Formal alliance wars / war-declaration state machine (wars are emergent in MVP)
- Combined rally marches (each march resolves independently)
- Alliance territory flags, alliance tech
- Commander XP/level-up and star-up mechanics (commanders grant a power bonus
  via `sim.CommanderPowerBonus` today; the attach-to-march UI is client work)
- Trading between players (RMT/fraud magnet)
- Real-time MMO battles, cross-world migration, voice
- Read replicas, multi-region (worlds are region-independent by schema)

## Repo layout

```
.
├── cmd/emberfalld/     # server binary
├── cmd/simtest/        # deterministic sim demo
├── internal/...        # api auth sim store scheduler anticheat
├── migrations live in internal/store/migrations/ (embedded in the binary)
├── Dockerfile  docker-compose.yml  Caddyfile  .env.example
└── scripts/backup.sh   # nightly pg_dump -> /var/backups/emberfall
```
