-- Emberfall Kingdom initial schema.
-- Every game-state table carries world_id (world sharding from day one).

-- identity
CREATE TABLE players (
    id           uuid PRIMARY KEY,
    apple_sub    text UNIQUE,
    device_id    text,
    display_name text NOT NULL DEFAULT 'Commander',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    banned       boolean NOT NULL DEFAULT false
);

CREATE TABLE sessions (
    id          uuid PRIMARY KEY,
    player_id   uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    refresh_hash text NOT NULL UNIQUE,
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_player_idx ON sessions(player_id);

-- world sharding
CREATE TABLE worlds (
    id          serial PRIMARY KEY,
    name        text NOT NULL,
    status      text NOT NULL DEFAULT 'open', -- open|full|closed
    size        int NOT NULL DEFAULT 512,
    max_players int NOT NULL DEFAULT 2500,
    config      jsonb NOT NULL DEFAULT '{}'::jsonb, -- per-world game config (theme packs)
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- map & cities
CREATE TABLE tiles (
    world_id      int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    x             int NOT NULL,
    y             int NOT NULL,
    terrain       text NOT NULL DEFAULT 'plain', -- plain|forest|mountain|water|node
    owner_city_id uuid,
    alliance_id   int,
    node_kind     text,        -- wood|stone|food for PvE frost nodes
    node_amount   int,
    node_respawn_at timestamptz,
    PRIMARY KEY (world_id, x, y)
);

CREATE TABLE cities (
    id         uuid PRIMARY KEY,
    player_id  uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    world_id   int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    name       text NOT NULL,
    tile_x     int NOT NULL,
    tile_y     int NOT NULL,
    level      int NOT NULL DEFAULT 1,
    power      bigint NOT NULL DEFAULT 0,
    shield_until timestamptz, -- stubbed in phase 1 (phase 2: purchasable shields)
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX cities_player_idx ON cities(player_id);
CREATE INDEX cities_world_idx ON cities(world_id);
CREATE INDEX cities_tile_idx ON cities(world_id, tile_x, tile_y);

-- lazy resources: amount = min(cap, amount + rate*(now-updated_at))
CREATE TABLE city_resources (
    city_id    uuid PRIMARY KEY REFERENCES cities(id) ON DELETE CASCADE,
    wood_amt   bigint NOT NULL DEFAULT 0,
    wood_rate  double precision NOT NULL DEFAULT 0,
    wood_cap   bigint NOT NULL DEFAULT 5000,
    stone_amt  bigint NOT NULL DEFAULT 0,
    stone_rate double precision NOT NULL DEFAULT 0,
    stone_cap  bigint NOT NULL DEFAULT 5000,
    food_amt   bigint NOT NULL DEFAULT 0,
    food_rate  double precision NOT NULL DEFAULT 0,
    food_cap   bigint NOT NULL DEFAULT 5000,
    gold_amt   bigint NOT NULL DEFAULT 0,
    gold_rate  double precision NOT NULL DEFAULT 0,
    gold_cap   bigint NOT NULL DEFAULT 5000,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- construction
CREATE TABLE buildings (
    id       uuid PRIMARY KEY,
    city_id  uuid NOT NULL REFERENCES cities(id) ON DELETE CASCADE,
    world_id int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    type     text NOT NULL,
    level    int NOT NULL DEFAULT 1,
    slot     int NOT NULL DEFAULT 0,
    state    text NOT NULL DEFAULT 'active', -- active|building|ruin
    UNIQUE (city_id, type)
);
CREATE INDEX buildings_city_idx ON buildings(city_id);

CREATE TABLE build_queue (
    id           uuid PRIMARY KEY,
    world_id     int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    city_id      uuid NOT NULL REFERENCES cities(id) ON DELETE CASCADE,
    building_id  uuid REFERENCES buildings(id) ON DELETE SET NULL,
    building_type text NOT NULL,
    action       text NOT NULL DEFAULT 'upgrade', -- upgrade (covers new builds too)
    target_level int NOT NULL,
    starts_at    timestamptz NOT NULL,
    ends_at      timestamptz NOT NULL,
    state        text NOT NULL DEFAULT 'queued' -- queued|done|cancelled
);
CREATE INDEX build_queue_due_idx ON build_queue(state, ends_at);

-- training
CREATE TABLE train_queue (
    id         uuid PRIMARY KEY,
    world_id   int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    city_id    uuid NOT NULL REFERENCES cities(id) ON DELETE CASCADE,
    infantry   int NOT NULL DEFAULT 0,
    cavalry    int NOT NULL DEFAULT 0,
    archers    int NOT NULL DEFAULT 0,
    siege      int NOT NULL DEFAULT 0,
    tier       int NOT NULL DEFAULT 1,
    starts_at  timestamptz NOT NULL,
    ends_at    timestamptz NOT NULL,
    state      text NOT NULL DEFAULT 'queued' -- queued|done|cancelled
);
CREATE INDEX train_queue_due_idx ON train_queue(state, ends_at);

-- military
CREATE TABLE troops (
    city_id  uuid NOT NULL REFERENCES cities(id) ON DELETE CASCADE,
    world_id int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    type     text NOT NULL, -- infantry|cavalry|archers|siege
    count    int NOT NULL DEFAULT 0,
    PRIMARY KEY (city_id, type)
);

CREATE TABLE marches (
    id             uuid PRIMARY KEY,
    player_id      uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    world_id       int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    origin_city_id uuid NOT NULL REFERENCES cities(id) ON DELETE CASCADE,
    target_x       int NOT NULL,
    target_y       int NOT NULL,
    target_city_id uuid REFERENCES cities(id) ON DELETE SET NULL, -- phase 2 PvP; NULL in phase 1
    purpose        text NOT NULL, -- attack|gather|reinforce|scout
    status         text NOT NULL DEFAULT 'marching', -- marching|arrived|returning|done|recalled
    departs_at     timestamptz NOT NULL,
    arrives_at     timestamptz NOT NULL,
    returns_at     timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX marches_due_idx ON marches(status, arrives_at);
CREATE INDEX marches_player_idx ON marches(player_id, status);

CREATE TABLE march_troops (
    march_id uuid NOT NULL REFERENCES marches(id) ON DELETE CASCADE,
    type     text NOT NULL,
    count    int NOT NULL DEFAULT 0,
    PRIMARY KEY (march_id, type)
);

-- combat history
CREATE TABLE battles (
    id                 uuid PRIMARY KEY,
    world_id           int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    march_id           uuid REFERENCES marches(id) ON DELETE SET NULL,
    attacker_player_id uuid NOT NULL,
    defender_player_id uuid,
    defender_city_id   uuid,
    defender_node      jsonb, -- {x,y,kind} for PvE frost nodes
    resolved_at        timestamptz NOT NULL DEFAULT now(),
    outcome            text NOT NULL, -- attacker_win|defender_win
    log                jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX battles_world_idx ON battles(world_id, resolved_at DESC);

CREATE TABLE battle_reports (
    id         uuid PRIMARY KEY,
    battle_id  uuid NOT NULL REFERENCES battles(id) ON DELETE CASCADE,
    player_id  uuid NOT NULL,
    world_id   int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    summary    jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX battle_reports_player_idx ON battle_reports(player_id, created_at DESC);

-- social
CREATE TABLE alliances (
    id         serial PRIMARY KEY,
    world_id   int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    name       text NOT NULL,
    tag        text NOT NULL,
    level      int NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (world_id, tag)
);

CREATE TABLE alliance_members (
    alliance_id int NOT NULL REFERENCES alliances(id) ON DELETE CASCADE,
    player_id   uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    world_id    int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    role        text NOT NULL DEFAULT 'member', -- leader|officer|member
    joined_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (alliance_id, player_id)
);
CREATE INDEX alliance_members_player_idx ON alliance_members(player_id);

CREATE TABLE chat_messages (
    id         bigserial PRIMARY KEY,
    world_id   int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    channel    text NOT NULL, -- global | alliance:{id}
    player_id  uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    body       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX chat_messages_channel_idx ON chat_messages(world_id, channel, created_at DESC);

-- economy / progression
CREATE TABLE items (
    id   serial PRIMARY KEY,
    key  text UNIQUE NOT NULL,
    kind text NOT NULL, -- speedup|shield|summon_ticket|chest
    data jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE inventory (
    player_id uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    world_id  int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    item_key  text NOT NULL,
    qty       int NOT NULL DEFAULT 0,
    PRIMARY KEY (player_id, item_key)
);

CREATE TABLE commanders (
    id         uuid PRIMARY KEY,
    player_id  uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    world_id   int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    key        text NOT NULL,
    name       text NOT NULL,
    rarity     text NOT NULL, -- common|rare|epic
    level      int NOT NULL DEFAULT 1,
    xp         int NOT NULL DEFAULT 0,
    stars      int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX commanders_player_idx ON commanders(player_id);

CREATE TABLE player_gacha (
    player_id     uuid PRIMARY KEY REFERENCES players(id) ON DELETE CASCADE,
    pity          int NOT NULL DEFAULT 0,
    total_summons int NOT NULL DEFAULT 0,
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- daily quests: one row per player per quest per day
CREATE TABLE quest_progress (
    player_id  uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    world_id   int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    quest_key  text NOT NULL,
    day        date NOT NULL,
    progress   int NOT NULL DEFAULT 0,
    claimed    bool NOT NULL DEFAULT false,
    PRIMARY KEY (player_id, quest_key, day)
);

-- 7-day welcome track claims
CREATE TABLE welcome_claims (
    player_id  uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    world_id   int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    day        int NOT NULL, -- 1..7
    claimed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (player_id, day)
);

-- infra: scheduled-event queue (event-driven tick, no global loop)
CREATE TABLE scheduled_events (
    id         bigserial PRIMARY KEY,
    world_id   int NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
    kind       text NOT NULL, -- build_done|train_done|march_arrive|march_return|node_respawn|shield_expire
    ref_id     uuid NOT NULL,
    run_at     timestamptz NOT NULL,
    status     text NOT NULL DEFAULT 'pending', -- pending|claimed|done|failed
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX scheduled_events_due_idx ON scheduled_events(status, run_at);

-- idempotency: 24h stored responses, never re-execute
CREATE TABLE idempotency_keys (
    player_id  uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    key        text NOT NULL,
    response   jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (player_id, key)
);

-- anti-cheat anomaly flags (review queue; never auto-ban in phase 1)
CREATE TABLE cheat_flags (
    id         bigserial PRIMARY KEY,
    player_id  uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    world_id   int,
    reason     text NOT NULL,
    detail     jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    reviewed   boolean NOT NULL DEFAULT false
);
CREATE INDEX cheat_flags_open_idx ON cheat_flags(reviewed, created_at DESC);
