package store

import (
	"time"

	"github.com/OverlordLoader/emberfall-server/internal/sim"
)

// ---- identity ----

type Player struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
	Banned      bool      `json:"banned"`
}

// ---- worlds ----

type World struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Players   int64     `json:"players"`
	CreatedAt time.Time `json:"created_at"`
}

// ---- cities ----

type Building struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Level int    `json:"level"`
	Slot  int    `json:"slot"`
	State string `json:"state"`
}

type BuildQueueItem struct {
	ID           string    `json:"id"`
	BuildingID   *string   `json:"building_id"`
	BuildingType string    `json:"building_type"`
	Action       string    `json:"action"`
	TargetLevel  int       `json:"target_level"`
	StartsAt     time.Time `json:"starts_at"`
	EndsAt       time.Time `json:"ends_at"`
	State        string    `json:"state"`
}

type TrainQueueItem struct {
	ID       string          `json:"id"`
	Troops   sim.TroopCounts `json:"troops"`
	Tier     int             `json:"tier"`
	StartsAt time.Time       `json:"starts_at"`
	EndsAt   time.Time       `json:"ends_at"`
	State    string          `json:"state"`
}

type CityResources struct {
	Amounts sim.ResourceBundle `json:"amounts"`
	Rates   sim.RateBundle     `json:"rates"`
	Caps    sim.ResourceBundle `json:"caps"`
}

type City struct {
	ID          string     `json:"id"`
	PlayerID    string     `json:"player_id"`
	WorldID     int        `json:"world_id"`
	Name        string     `json:"name"`
	X           int        `json:"x"`
	Y           int        `json:"y"`
	Level       int        `json:"level"`
	Power       int64      `json:"power"`
	ShieldUntil *time.Time `json:"shield_until,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type CityFull struct {
	City       City             `json:"city"`
	Resources  CityResources    `json:"resources"`
	Buildings  []Building       `json:"buildings"`
	Troops     sim.TroopCounts  `json:"troops"`
	BuildQueue []BuildQueueItem `json:"build_queue"`
	TrainQueue []TrainQueueItem `json:"train_queue"`
	MarchSlots int              `json:"march_slots"`
}

// ---- map ----

type Tile struct {
	X           int     `json:"x"`
	Y           int     `json:"y"`
	Terrain     string  `json:"terrain"`
	OwnerCityID *string `json:"owner_city_id,omitempty"`
	NodeKind    *string `json:"node_kind,omitempty"`
	NodeAmount  *int    `json:"node_amount,omitempty"`
}

type MapCity struct {
	CityID      string  `json:"city_id"`
	PlayerID    string  `json:"player_id"`
	PlayerName  string  `json:"player_name,omitempty"` // only in scout range
	Name        string  `json:"name"`
	X           int     `json:"x"`
	Y           int     `json:"y"`
	Level       int     `json:"level"`
	Power       *int64  `json:"power,omitempty"` // only in scout range
	AllianceTag *string `json:"alliance_tag,omitempty"`
}

type MapMarch struct {
	MarchID  string           `json:"march_id"`
	PlayerID string           `json:"player_id"`
	X        float64          `json:"x"`
	Y        float64          `json:"y"`
	Progress float64          `json:"progress"`
	Purpose  string           `json:"purpose"`
	Troops   *sim.TroopCounts `json:"troops,omitempty"` // only in scout range
}

// ---- marches ----

type March struct {
	ID           string          `json:"id"`
	PlayerID     string          `json:"player_id"`
	WorldID      int             `json:"world_id"`
	OriginCityID string          `json:"origin_city_id"`
	TargetX      int             `json:"target_x"`
	TargetY      int             `json:"target_y"`
	TargetCityID *string         `json:"target_city_id"`
	Purpose      string          `json:"purpose"`
	Status       string          `json:"status"`
	Troops       sim.TroopCounts `json:"troops"`
	Tier         int             `json:"tier"`
	DepartsAt    time.Time       `json:"departs_at"`
	ArrivesAt    time.Time       `json:"arrives_at"`
	ReturnsAt    *time.Time      `json:"returns_at"`
}

// ---- battles / reports ----

type BattleReport struct {
	ID        string         `json:"id"`
	BattleID  string         `json:"battle_id"`
	Summary   map[string]any `json:"summary"`
	CreatedAt time.Time      `json:"created_at"`
}

// ---- social ----

type Alliance struct {
	ID        int       `json:"id"`
	WorldID   int       `json:"world_id"`
	Name      string    `json:"name"`
	Tag       string    `json:"tag"`
	Level     int       `json:"level"`
	Members   int64     `json:"members"`
	CreatedAt time.Time `json:"created_at"`
}

type ChatMessage struct {
	ID         int64     `json:"id"`
	Channel    string    `json:"channel"`
	PlayerID   string    `json:"player_id"`
	PlayerName string    `json:"player_name"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
}

// ---- economy ----

type InventoryItem struct {
	Key  string         `json:"key"`
	Kind string         `json:"kind"`
	Qty  int            `json:"qty"`
	Data map[string]any `json:"data"`
}

type Commander struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Rarity    string    `json:"rarity"`
	Level     int       `json:"level"`
	XP        int       `json:"xp"`
	Stars     int       `json:"stars"`
	CreatedAt time.Time `json:"created_at"`
}

type QuestStatus struct {
	Key         string             `json:"key"`
	Description string             `json:"description"`
	Target      int                `json:"target"`
	Progress    int                `json:"progress"`
	Claimed     bool               `json:"claimed"`
	Reward      sim.ResourceBundle `json:"reward"`
}

// ---- infra ----

type ScheduledEvent struct {
	ID      int64     `json:"id"`
	WorldID int       `json:"world_id"`
	Kind    string    `json:"kind"`
	RefID   string    `json:"ref_id"`
	RunAt   time.Time `json:"run_at"`
	Status  string    `json:"status"`
}
