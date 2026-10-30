CREATE TABLE IF NOT EXISTS TF_challenges (
  id          TEXT PRIMARY KEY,
  id_hash     TEXT NOT NULL,
  purpose     TEXT NOT NULL,
  answer      TEXT NOT NULL,
  issued_at   TEXT NOT NULL,
  expires_at  TEXT NOT NULL,
  verified_at TEXT,
  attempts    INTEGER NOT NULL DEFAULT 0,
  ip_hash     TEXT,
  agent_hash  TEXT NOT NULL,
  created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_challenge_expiry ON TF_challenges(expires_at);
CREATE INDEX IF NOT EXISTS idx_challenge_agent ON TF_challenges(agent_hash, purpose);

CREATE TABLE IF NOT EXISTS TF_login_failures (
  login_key          TEXT PRIMARY KEY,
  failures           INTEGER NOT NULL DEFAULT 0,
  window_started_at  TEXT NOT NULL,
  locked_until       TEXT,
  updated_at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_login_failure_locked ON TF_login_failures(locked_until);
