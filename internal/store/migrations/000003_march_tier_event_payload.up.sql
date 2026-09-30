-- March tier persistence + generic event payloads.
ALTER TABLE marches ADD COLUMN tier int NOT NULL DEFAULT 1;
ALTER TABLE scheduled_events ADD COLUMN payload jsonb NOT NULL DEFAULT '{}'::jsonb;
