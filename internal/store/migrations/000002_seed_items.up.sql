-- Seed the item catalog (speedups, shields, summon tickets).
INSERT INTO items (key, kind, data) VALUES
    ('speedup_small',  'speedup',       '{"seconds": 300,  "description": "Finishes 5 minutes of a timer"}'),
    ('speedup_large',  'speedup',       '{"seconds": 3600, "description": "Finishes 1 hour of a timer"}'),
    ('shield_8h',      'shield',        '{"seconds": 28800, "description": "8h city shield (phase 2)"}'),
    ('summon_ticket',  'summon_ticket', '{"description": "One free commander summon"}'),
    ('chest_common',   'chest',         '{"description": "Common supply chest"}')
ON CONFLICT (key) DO NOTHING;
