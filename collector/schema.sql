-- The one table behind Meru's usage-report collector. One row per report.
-- It has no column for an IP address, a time of day or a place, so the
-- collector can't store one even by mistake. Run it once on a new database:
--   npx wrangler d1 execute meru-reports --remote --file=schema.sql

CREATE TABLE IF NOT EXISTS reports (
    day       TEXT NOT NULL,    -- UTC date the collector got it, YYYY-MM-DD
    v         INTEGER NOT NULL, -- report format version
    meru      TEXT NOT NULL,    -- Meru version, such as 0.7.5
    os        TEXT NOT NULL,    -- darwin, linux or windows
    arch      TEXT NOT NULL,    -- arm64 or amd64
    profile   TEXT NOT NULL,    -- lite or full
    age       TEXT NOT NULL,    -- week, month, year or older
    questions TEXT NOT NULL     -- 0, 1-5, 6-20 or 21+
);

-- The totals and the daily clean-up both read by day.
CREATE INDEX IF NOT EXISTS reports_day ON reports (day);
