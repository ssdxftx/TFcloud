CREATE TABLE IF NOT EXISTS TF_rate_limits (
  scope_key  TEXT PRIMARY KEY,
  count      INTEGER NOT NULL DEFAULT 0,
  reset_at   TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_rate_limit_reset ON TF_rate_limits(reset_at);
