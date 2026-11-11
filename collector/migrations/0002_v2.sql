-- Report version 2: six columns, all null on the version 1 rows already
-- there, and the counter for the all-time count of installs. Run once:
--   npx wrangler@4 d1 execute meru-reports --remote --file=migrations/0002_v2.sql
ALTER TABLE reports ADD COLUMN active_days TEXT;
ALTER TABLE reports ADD COLUMN streak TEXT;
ALTER TABLE reports ADD COLUMN previous TEXT;
ALTER TABLE reports ADD COLUMN week_first INTEGER;
ALTER TABLE reports ADD COLUMN month_first INTEGER;
ALTER TABLE reports ADD COLUMN first INTEGER;
CREATE TABLE IF NOT EXISTS counters (
    name TEXT PRIMARY KEY,
    n    INTEGER NOT NULL
);
