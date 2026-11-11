-- The tables behind Meru's usage-report collector. reports holds one row
-- per report; it has no column for an IP address, a time of day or a place,
-- so the collector can't store one even by mistake. counters holds the
-- all-time count of installs, which the daily clean-up of old rows would
-- otherwise shrink. Run it once on a new database:
--   npx wrangler@4 d1 execute meru-reports --remote --file=schema.sql
-- A database made before report version 2 takes migrations/0002_v2.sql.

CREATE TABLE IF NOT EXISTS reports (
    day         TEXT NOT NULL,    -- UTC date the collector got it, YYYY-MM-DD
    v           INTEGER NOT NULL, -- report format version, 1 or 2
    meru        TEXT NOT NULL,    -- Meru version, such as 0.7.7
    os          TEXT NOT NULL,    -- darwin, linux or windows
    arch        TEXT NOT NULL,    -- arm64 or amd64
    profile     TEXT NOT NULL,    -- lite or full
    age         TEXT NOT NULL,    -- week, month, year or older
    questions   TEXT NOT NULL,    -- 0, 1-5, 6-20 or 21+
    -- Version 2 and later; null on a version 1 row.
    active_days TEXT,             -- 0, 1-2, 3-5 or 6-7
    streak      TEXT,             -- 1, 2-4, 5-12 or 13+
    previous    TEXT,             -- the version last reported, or none
    week_first  INTEGER,          -- 1 on an install's first report of the ISO week
    month_first INTEGER,          -- 1 on its first report of the UTC month
    first       INTEGER           -- 1 on its first report ever
);

-- The totals and the daily clean-up both read by day.
CREATE INDEX IF NOT EXISTS reports_day ON reports (day);

CREATE TABLE IF NOT EXISTS counters (
    name TEXT PRIMARY KEY,        -- "installs"
    n    INTEGER NOT NULL
);
