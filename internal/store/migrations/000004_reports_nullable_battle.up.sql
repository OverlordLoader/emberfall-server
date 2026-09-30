-- Battle reports can describe non-battle events (e.g. gathering).
ALTER TABLE battle_reports ALTER COLUMN battle_id DROP NOT NULL;
