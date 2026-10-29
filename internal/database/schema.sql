-- TFcloud · SQLite schema (v1)
-- 与 PHP 版 22 张表一一对应，差异：
--   1) DATETIME(3) -> TEXT 'YYYY-MM-DD HH:MM:SS.mmm'（字典序即时间序，接口输出与 MySQL 一致）
--   2) JSON -> TEXT（json_valid 约束，查询用 json_extract）
--   3) 新增 username_norm / email_norm 承载「忽略大小写与重音」的唯一性语义
--   4) 移除 users.quota（本版本不设账号配额与会员限制）

CREATE TABLE IF NOT EXISTS TF_users (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  username           TEXT    NOT NULL,
  username_norm      TEXT    NOT NULL,
  password_hash      TEXT,
  display_name       TEXT    NOT NULL DEFAULT '',
  email              TEXT,
  email_norm         TEXT,
  qq                 TEXT,
  role               TEXT    NOT NULL DEFAULT 'user' CHECK (role IN ('user','admin')),
  status             TEXT    NOT NULL DEFAULT 'active',
  daily_sign_email   INTEGER NOT NULL DEFAULT 0,
  push_channel       TEXT    NOT NULL DEFAULT 'none',
  push_token         TEXT,
  password_migrated_at TEXT,
  last_login_at      TEXT,
  last_login_ip      TEXT,
  created_at         TEXT    NOT NULL,
  updated_at         TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_users_username_norm ON TF_users(username_norm);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_users_email_norm ON TF_users(email_norm) WHERE email_norm IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uniq_users_qq ON TF_users(qq) WHERE qq IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_users_role_status ON TF_users(role, status);

CREATE TABLE IF NOT EXISTS TF_sessions (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL REFERENCES TF_users(id) ON DELETE CASCADE,
  token_hash   TEXT    NOT NULL,
  ip_address   TEXT,
  user_agent   TEXT,
  last_seen_at TEXT,
  expires_at   TEXT    NOT NULL,
  revoked_at   TEXT,
  created_at   TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_sessions_token ON TF_sessions(token_hash);
CREATE INDEX IF NOT EXISTS idx_sessions_user_active ON TF_sessions(user_id, revoked_at, expires_at);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON TF_sessions(expires_at);

CREATE TABLE IF NOT EXISTS TF_password_change_codes (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id     INTEGER NOT NULL REFERENCES TF_users(id) ON DELETE CASCADE,
  purpose     TEXT    NOT NULL DEFAULT 'change',
  email       TEXT    NOT NULL,
  code_hash   TEXT    NOT NULL,
  attempts    INTEGER NOT NULL DEFAULT 0,
  expires_at  TEXT    NOT NULL,
  consumed_at TEXT,
  created_at  TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_password_code_user ON TF_password_change_codes(user_id, purpose, consumed_at, expires_at, id);

CREATE TABLE IF NOT EXISTS TF_email_verification_codes (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  email       TEXT    NOT NULL,
  email_norm  TEXT    NOT NULL,
  purpose     TEXT    NOT NULL DEFAULT 'register',
  code_hash   TEXT    NOT NULL,
  ip_hash     TEXT,
  attempts    INTEGER NOT NULL DEFAULT 0,
  expires_at  TEXT    NOT NULL,
  consumed_at TEXT,
  created_at  TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_email_code ON TF_email_verification_codes(email_norm, purpose, consumed_at, expires_at, id);
CREATE INDEX IF NOT EXISTS idx_email_code_ip ON TF_email_verification_codes(ip_hash, created_at);

CREATE TABLE IF NOT EXISTS TF_user_identities (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id          INTEGER NOT NULL REFERENCES TF_users(id) ON DELETE CASCADE,
  provider         TEXT    NOT NULL,
  external_subject TEXT    NOT NULL,
  display_name     TEXT    NOT NULL DEFAULT '',
  profile_json     TEXT,
  linked_at        TEXT    NOT NULL,
  last_login_at    TEXT,
  updated_at       TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_identity_provider_subject ON TF_user_identities(provider, external_subject);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_identity_user_provider ON TF_user_identities(user_id, provider);
CREATE INDEX IF NOT EXISTS idx_identity_user ON TF_user_identities(user_id);

CREATE TABLE IF NOT EXISTS TF_plugins (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  plugin_code  TEXT    NOT NULL,
  name         TEXT    NOT NULL,
  version      TEXT    NOT NULL DEFAULT '0.1.0',
  status       TEXT    NOT NULL DEFAULT 'enabled',
  config_json  TEXT,
  installed_at TEXT,
  updated_at   TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_plugins_code ON TF_plugins(plugin_code);

CREATE TABLE IF NOT EXISTS TF_plugin_accounts (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id            INTEGER NOT NULL REFERENCES TF_users(id),
  plugin_code        TEXT    NOT NULL,
  external_user_id   TEXT,
  display_name       TEXT    NOT NULL DEFAULT '',
  status             TEXT    NOT NULL DEFAULT 'active',
  settings_json      TEXT,
  profile_json       TEXT,
  last_verified_at   TEXT,
  last_run_at        TEXT,
  next_run_at        TEXT,
  last_error_code    TEXT,
  last_error_message TEXT,
  created_at         TEXT    NOT NULL,
  updated_at         TEXT    NOT NULL,
  deleted_at         TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_account_user_plugin_external
  ON TF_plugin_accounts(user_id, plugin_code, external_user_id) WHERE external_user_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_account_schedule ON TF_plugin_accounts(plugin_code, status, next_run_at);
CREATE INDEX IF NOT EXISTS idx_account_user_plugin ON TF_plugin_accounts(user_id, plugin_code, deleted_at);

CREATE TABLE IF NOT EXISTS TF_plugin_credentials (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id      INTEGER NOT NULL REFERENCES TF_plugin_accounts(id) ON DELETE CASCADE,
  credential_type TEXT    NOT NULL DEFAULT 'cookie',
  payload_cipher  BLOB    NOT NULL,
  nonce           BLOB    NOT NULL,
  key_version     INTEGER NOT NULL DEFAULT 1,
  fingerprint     TEXT    NOT NULL,
  expires_at      TEXT,
  verified_at     TEXT,
  rotated_at      TEXT,
  created_at      TEXT    NOT NULL,
  updated_at      TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_credential_account_type ON TF_plugin_credentials(account_id, credential_type);
CREATE INDEX IF NOT EXISTS idx_credential_fingerprint ON TF_plugin_credentials(fingerprint);
CREATE INDEX IF NOT EXISTS idx_credential_expiration ON TF_plugin_credentials(expires_at);

CREATE TABLE IF NOT EXISTS TF_platform_auth_flows (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  flow_no        TEXT    NOT NULL,
  user_id        INTEGER NOT NULL REFERENCES TF_users(id) ON DELETE CASCADE,
  platform_code  TEXT    NOT NULL,
  method         TEXT    NOT NULL,
  status         TEXT    NOT NULL DEFAULT 'waiting',
  context_cipher BLOB    NOT NULL,
  nonce          BLOB    NOT NULL,
  key_version    INTEGER NOT NULL DEFAULT 1,
  expires_at     TEXT    NOT NULL,
  consumed_at    TEXT,
  created_at     TEXT    NOT NULL,
  updated_at     TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_platform_auth_flow ON TF_platform_auth_flows(flow_no);
CREATE INDEX IF NOT EXISTS idx_platform_auth_owner ON TF_platform_auth_flows(user_id, platform_code, status, expires_at);
CREATE INDEX IF NOT EXISTS idx_platform_auth_expiration ON TF_platform_auth_flows(expires_at);

CREATE TABLE IF NOT EXISTS TF_sign_tasks (
  id                   INTEGER PRIMARY KEY AUTOINCREMENT,
  task_no              TEXT    NOT NULL,
  request_id           TEXT,
  user_id              INTEGER NOT NULL REFERENCES TF_users(id),
  account_id           INTEGER NOT NULL REFERENCES TF_plugin_accounts(id),
  plugin_code          TEXT    NOT NULL,
  queue_name           TEXT    NOT NULL DEFAULT 'default',
  action               TEXT    NOT NULL DEFAULT 'daily_sign',
  trigger_type         TEXT    NOT NULL DEFAULT 'schedule',
  idempotency_key      TEXT,
  schedule_date        TEXT,
  status               TEXT    NOT NULL DEFAULT 'pending',
  priority             INTEGER NOT NULL DEFAULT 0,
  attempts             INTEGER NOT NULL DEFAULT 0,
  max_attempts         INTEGER NOT NULL DEFAULT 24,
  total_count          INTEGER NOT NULL DEFAULT 0,
  success_count        INTEGER NOT NULL DEFAULT 0,
  already_count        INTEGER NOT NULL DEFAULT 0,
  skipped_count        INTEGER NOT NULL DEFAULT 0,
  failed_count         INTEGER NOT NULL DEFAULT 0,
  summary_json         TEXT,
  account_snapshot_json TEXT,
  last_error_code      TEXT,
  last_error_message   TEXT,
  scheduled_at         TEXT,
  available_at         TEXT,
  locked_at            TEXT,
  locked_by            TEXT,
  started_at           TEXT,
  finished_at          TEXT,
  created_at           TEXT    NOT NULL,
  updated_at           TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_task_no ON TF_sign_tasks(task_no);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_task_idempotency ON TF_sign_tasks(idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_task_dispatch ON TF_sign_tasks(status, available_at, priority, id);
CREATE INDEX IF NOT EXISTS idx_task_owner ON TF_sign_tasks(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_task_account ON TF_sign_tasks(account_id, created_at);
CREATE INDEX IF NOT EXISTS idx_task_schedule ON TF_sign_tasks(plugin_code, schedule_date, status);

CREATE TABLE IF NOT EXISTS TF_bilibili_live_sessions (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  session_no            TEXT    NOT NULL,
  task_id               INTEGER NOT NULL REFERENCES TF_sign_tasks(id) ON DELETE CASCADE,
  run_id                INTEGER NOT NULL,
  user_id               INTEGER NOT NULL REFERENCES TF_users(id) ON DELETE CASCADE,
  account_id            INTEGER NOT NULL REFERENCES TF_plugin_accounts(id) ON DELETE CASCADE,
  status                TEXT    NOT NULL DEFAULT 'discovering',
  target_heartbeat_count INTEGER NOT NULL DEFAULT 70,
  max_failures          INTEGER NOT NULL DEFAULT 5,
  max_rooms             INTEGER NOT NULL DEFAULT 5,
  settings_json         TEXT,
  summary_json          TEXT,
  next_action_at        TEXT,
  lock_token            TEXT,
  lock_owner            TEXT,
  lock_until            TEXT,
  expires_at            TEXT    NOT NULL,
  started_at            TEXT    NOT NULL,
  finished_at           TEXT,
  created_at            TEXT    NOT NULL,
  updated_at            TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_live_session_no ON TF_bilibili_live_sessions(session_no);
CREATE INDEX IF NOT EXISTS idx_live_task ON TF_bilibili_live_sessions(task_id);
CREATE INDEX IF NOT EXISTS idx_live_account_status ON TF_bilibili_live_sessions(account_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_live_expiry ON TF_bilibili_live_sessions(status, expires_at);
CREATE INDEX IF NOT EXISTS idx_live_due ON TF_bilibili_live_sessions(status, next_action_at, lock_until, id);

CREATE TABLE IF NOT EXISTS TF_bilibili_live_room_sessions (
  id                   INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id           INTEGER NOT NULL REFERENCES TF_bilibili_live_sessions(id) ON DELETE CASCADE,
  room_id              INTEGER NOT NULL,
  ruid                 INTEGER NOT NULL,
  medal_target_id      INTEGER NOT NULL DEFAULT 0,
  parent_area_id       INTEGER NOT NULL DEFAULT 0,
  area_id              INTEGER NOT NULL DEFAULT 0,
  room_name            TEXT,
  medal_name           TEXT,
  medal_level          INTEGER NOT NULL DEFAULT 0,
  status               TEXT    NOT NULL DEFAULT 'entering',
  sequence_no          INTEGER NOT NULL DEFAULT 0,
  heartbeat_count      INTEGER NOT NULL DEFAULT 0,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  server_timestamp     INTEGER,
  secret_key           TEXT,
  secret_rule_json     TEXT,
  device_uuid          TEXT,
  next_heartbeat_at    TEXT,
  request_started_at   TEXT,
  lock_token           TEXT,
  lock_owner           TEXT,
  lock_until           TEXT,
  last_error_code      TEXT,
  last_error_message   TEXT,
  started_at           TEXT    NOT NULL,
  finished_at          TEXT,
  created_at           TEXT    NOT NULL,
  updated_at           TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_live_room ON TF_bilibili_live_room_sessions(session_id, room_id);
CREATE INDEX IF NOT EXISTS idx_live_room_due ON TF_bilibili_live_room_sessions(status, next_heartbeat_at, lock_until, id);
CREATE INDEX IF NOT EXISTS idx_live_room_session ON TF_bilibili_live_room_sessions(session_id, status, id);

CREATE TABLE IF NOT EXISTS TF_sign_runs (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  run_no          TEXT    NOT NULL,
  task_id         INTEGER NOT NULL REFERENCES TF_sign_tasks(id) ON DELETE CASCADE,
  attempt_no      INTEGER NOT NULL DEFAULT 1,
  plugin_code     TEXT    NOT NULL,
  plugin_version  TEXT,
  worker_id       TEXT,
  status          TEXT    NOT NULL DEFAULT 'running',
  total_count     INTEGER NOT NULL DEFAULT 0,
  success_count   INTEGER NOT NULL DEFAULT 0,
  already_count   INTEGER NOT NULL DEFAULT 0,
  skipped_count   INTEGER NOT NULL DEFAULT 0,
  failed_count    INTEGER NOT NULL DEFAULT 0,
  error_code      TEXT,
  error_message   TEXT,
  exception_class TEXT,
  summary_json    TEXT,
  started_at      TEXT    NOT NULL,
  finished_at     TEXT,
  duration_ms     INTEGER,
  created_at      TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_run_no ON TF_sign_runs(run_no);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_run_task_attempt ON TF_sign_runs(task_id, attempt_no);
CREATE INDEX IF NOT EXISTS idx_run_plugin_status ON TF_sign_runs(plugin_code, status, created_at);

CREATE TABLE IF NOT EXISTS TF_sign_records (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id          INTEGER NOT NULL REFERENCES TF_sign_tasks(id) ON DELETE CASCADE,
  run_id           INTEGER NOT NULL REFERENCES TF_sign_runs(id) ON DELETE CASCADE,
  parent_record_id INTEGER,
  user_id          INTEGER NOT NULL,
  account_id       INTEGER,
  plugin_code      TEXT    NOT NULL,
  record_key       TEXT    NOT NULL,
  sequence_no      INTEGER NOT NULL DEFAULT 0,
  schema_version   INTEGER NOT NULL DEFAULT 1,
  action           TEXT    NOT NULL,
  target_type      TEXT,
  target_id        TEXT,
  target_name      TEXT,
  status           TEXT    NOT NULL,
  result_code      TEXT,
  message          TEXT,
  reward_json      TEXT,
  metrics_json     TEXT,
  safe_result_json TEXT,
  started_at       TEXT,
  finished_at      TEXT,
  duration_ms      INTEGER,
  created_at       TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_record_run_key ON TF_sign_records(run_id, record_key);
CREATE INDEX IF NOT EXISTS idx_record_task_seq ON TF_sign_records(task_id, sequence_no, id);
CREATE INDEX IF NOT EXISTS idx_record_account ON TF_sign_records(account_id, created_at);
CREATE INDEX IF NOT EXISTS idx_record_plugin_status ON TF_sign_records(plugin_code, status, created_at);

CREATE TABLE IF NOT EXISTS TF_sign_record_payloads (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  record_id     INTEGER NOT NULL REFERENCES TF_sign_records(id) ON DELETE CASCADE,
  payload_type  TEXT    NOT NULL DEFAULT 'response',
  payload_cipher BLOB   NOT NULL,
  nonce         BLOB    NOT NULL,
  key_version   INTEGER NOT NULL DEFAULT 1,
  content_type  TEXT,
  payload_size  INTEGER NOT NULL DEFAULT 0,
  expires_at    TEXT    NOT NULL,
  created_at    TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_payload_record ON TF_sign_record_payloads(record_id);
CREATE INDEX IF NOT EXISTS idx_payload_expiration ON TF_sign_record_payloads(expires_at);

CREATE TABLE IF NOT EXISTS TF_settings (
  setting_key   TEXT PRIMARY KEY,
  setting_value TEXT,
  is_secret     INTEGER NOT NULL DEFAULT 0,
  updated_by    INTEGER,
  updated_at    TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS TF_audit_logs (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id    TEXT,
  user_id       INTEGER,
  action        TEXT    NOT NULL,
  resource_type TEXT,
  resource_id   TEXT,
  ip_address    TEXT,
  context_json  TEXT,
  created_at    TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_user ON TF_audit_logs(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_resource ON TF_audit_logs(resource_type, resource_id, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_request ON TF_audit_logs(request_id);

CREATE TABLE IF NOT EXISTS TF_mail_tasks (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  task_no       TEXT    NOT NULL,
  user_id       INTEGER,
  template_code TEXT    NOT NULL,
  subject       TEXT    NOT NULL,
  context_json  TEXT,
  status        TEXT    NOT NULL DEFAULT 'pending',
  priority      INTEGER NOT NULL DEFAULT 0,
  total_count   INTEGER NOT NULL DEFAULT 0,
  success_count INTEGER NOT NULL DEFAULT 0,
  failed_count  INTEGER NOT NULL DEFAULT 0,
  scheduled_at  TEXT,
  started_at    TEXT,
  finished_at   TEXT,
  created_at    TEXT    NOT NULL,
  updated_at    TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_mail_task_no ON TF_mail_tasks(task_no);
CREATE INDEX IF NOT EXISTS idx_mail_task_dispatch ON TF_mail_tasks(status, scheduled_at, priority, id);
CREATE INDEX IF NOT EXISTS idx_mail_task_user ON TF_mail_tasks(user_id, created_at);

CREATE TABLE IF NOT EXISTS TF_mail_records (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id             INTEGER REFERENCES TF_mail_tasks(id) ON DELETE SET NULL,
  message_no          TEXT    NOT NULL,
  recipient           TEXT    NOT NULL,
  recipient_hash      TEXT    NOT NULL,
  subject             TEXT    NOT NULL DEFAULT '',
  body_html           TEXT,
  status              TEXT    NOT NULL DEFAULT 'pending',
  attempts            INTEGER NOT NULL DEFAULT 0,
  max_attempts        INTEGER NOT NULL DEFAULT 3,
  provider_message_id TEXT,
  last_error_code     TEXT,
  last_error_message  TEXT,
  available_at        TEXT,
  sent_at             TEXT,
  created_at          TEXT    NOT NULL,
  updated_at          TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_mail_msg_no ON TF_mail_records(message_no);
CREATE INDEX IF NOT EXISTS idx_mail_record_dispatch ON TF_mail_records(status, available_at, id);
CREATE INDEX IF NOT EXISTS idx_mail_record_task ON TF_mail_records(task_id, id);
CREATE INDEX IF NOT EXISTS idx_mail_record_recipient ON TF_mail_records(recipient_hash, created_at);

CREATE TABLE IF NOT EXISTS TF_notification_events (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL REFERENCES TF_users(id) ON DELETE CASCADE,
  mail_task_id INTEGER REFERENCES TF_mail_tasks(id) ON DELETE SET NULL,
  event_type   TEXT    NOT NULL,
  event_key    TEXT    NOT NULL,
  status       TEXT    NOT NULL DEFAULT 'queued',
  payload_json TEXT,
  created_at   TEXT    NOT NULL,
  updated_at   TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_notification_event_key ON TF_notification_events(event_key);
CREATE INDEX IF NOT EXISTS idx_notification_user ON TF_notification_events(user_id, event_type, created_at);

INSERT INTO TF_plugins (plugin_code, name, version, status, installed_at, updated_at) VALUES
  ('tieba','百度贴吧','0.1.0','enabled',datetime('now','localtime'),datetime('now','localtime')),
  ('bilibili','哔哩哔哩','0.4.0','enabled',datetime('now','localtime'),datetime('now','localtime')),
  ('netease','网易云音乐','0.2.0','enabled',datetime('now','localtime'),datetime('now','localtime')),
  ('iqiyi','爱奇艺','0.2.0','enabled',datetime('now','localtime'),datetime('now','localtime')),
  ('sport','小米运动','0.2.0','enabled',datetime('now','localtime'),datetime('now','localtime')),
  ('cloud189','天翼云盘','0.1.0','enabled',datetime('now','localtime'),datetime('now','localtime')),
  ('picacomic','哔咔漫画','0.1.0','enabled',datetime('now','localtime'),datetime('now','localtime')),
  ('uma','赛马娘','0.1.0','enabled',datetime('now','localtime'),datetime('now','localtime')),
  ('yamibo','Yamibo（百合会 / 300）','0.1.0','enabled',datetime('now','localtime'),datetime('now','localtime')),
  ('v2ex','V2EX','0.1.0','enabled',datetime('now','localtime'),datetime('now','localtime'))
ON CONFLICT(plugin_code) DO UPDATE SET name=excluded.name, version=excluded.version, updated_at=excluded.updated_at;
