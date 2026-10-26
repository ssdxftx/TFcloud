package model

// User 对应 TF_users。本版本已移除 quota（账号配额/会员限制）。
type User struct {
	ID                 int64   `db:"id" json:"id"`
	Username           string  `db:"username" json:"username"`
	UsernameNorm       string  `db:"username_norm" json:"-"`
	PasswordHash       *string `db:"password_hash" json:"-"`
	DisplayName        string  `db:"display_name" json:"display_name"`
	Email              *string `db:"email" json:"email"`
	EmailNorm          *string `db:"email_norm" json:"-"`
	QQ                 *string `db:"qq" json:"qq"`
	Role               string  `db:"role" json:"role"`
	Status             string  `db:"status" json:"status"`
	DailySignEmail     int     `db:"daily_sign_email" json:"daily_sign_email"`
	PushChannel        string  `db:"push_channel" json:"push_channel"`
	PushToken          *string `db:"push_token" json:"-"`
	PasswordMigratedAt *string `db:"password_migrated_at" json:"-"`
	LastLoginAt        *string `db:"last_login_at" json:"last_login_at"`
	LastLoginIP        *string `db:"last_login_ip" json:"last_login_ip"`
	CreatedAt          string  `db:"created_at" json:"created_at"`
	UpdatedAt          string  `db:"updated_at" json:"updated_at"`
}

func (u *User) HasPassword() bool {
	return u.PasswordHash != nil && *u.PasswordHash != ""
}

type Session struct {
	ID         int64   `db:"id"`
	UserID     int64   `db:"user_id"`
	TokenHash  string  `db:"token_hash"`
	IPAddress  *string `db:"ip_address"`
	UserAgent  *string `db:"user_agent"`
	LastSeenAt *string `db:"last_seen_at"`
	ExpiresAt  string  `db:"expires_at"`
	RevokedAt  *string `db:"revoked_at"`
	CreatedAt  string  `db:"created_at"`
}

type PluginAccount struct {
	ID               int64   `db:"id" json:"id"`
	UserID           int64   `db:"user_id" json:"user_id"`
	PluginCode       string  `db:"plugin_code" json:"plugin_code"`
	ExternalUserID   *string `db:"external_user_id" json:"external_user_id"`
	DisplayName      string  `db:"display_name" json:"display_name"`
	Status           string  `db:"status" json:"status"`
	SettingsJSON     *string `db:"settings_json" json:"-"`
	ProfileJSON      *string `db:"profile_json" json:"-"`
	LastVerifiedAt   *string `db:"last_verified_at" json:"last_verified_at"`
	LastRunAt        *string `db:"last_run_at" json:"last_run_at"`
	NextRunAt        *string `db:"next_run_at" json:"next_run_at"`
	LastErrorCode    *string `db:"last_error_code" json:"last_error_code"`
	LastErrorMessage *string `db:"last_error_message" json:"last_error_message"`
	CreatedAt        string  `db:"created_at" json:"created_at"`
	UpdatedAt        string  `db:"updated_at" json:"updated_at"`
	DeletedAt        *string `db:"deleted_at" json:"-"`
}

type Credential struct {
	ID             int64   `db:"id"`
	AccountID      int64   `db:"account_id"`
	CredentialType string  `db:"credential_type"`
	PayloadCipher  []byte  `db:"payload_cipher"`
	Nonce          []byte  `db:"nonce"`
	KeyVersion     int     `db:"key_version"`
	Fingerprint    string  `db:"fingerprint"`
	ExpiresAt      *string `db:"expires_at"`
	VerifiedAt     *string `db:"verified_at"`
	RotatedAt      *string `db:"rotated_at"`
	CreatedAt      string  `db:"created_at"`
	UpdatedAt      string  `db:"updated_at"`
}

type SignTask struct {
	ID                  int64   `db:"id" json:"id"`
	TaskNo              string  `db:"task_no" json:"task_no"`
	RequestID           *string `db:"request_id" json:"request_id"`
	UserID              int64   `db:"user_id" json:"user_id"`
	AccountID           int64   `db:"account_id" json:"account_id"`
	PluginCode          string  `db:"plugin_code" json:"plugin_code"`
	QueueName           string  `db:"queue_name" json:"queue_name"`
	Action              string  `db:"action" json:"action"`
	TriggerType         string  `db:"trigger_type" json:"trigger_type"`
	IdempotencyKey      *string `db:"idempotency_key" json:"-"`
	ScheduleDate        *string `db:"schedule_date" json:"schedule_date"`
	Status              string  `db:"status" json:"status"`
	Priority            int     `db:"priority" json:"priority"`
	Attempts            int     `db:"attempts" json:"attempts"`
	MaxAttempts         int     `db:"max_attempts" json:"max_attempts"`
	TotalCount          int     `db:"total_count" json:"total_count"`
	SuccessCount        int     `db:"success_count" json:"success_count"`
	AlreadyCount        int     `db:"already_count" json:"already_count"`
	SkippedCount        int     `db:"skipped_count" json:"skipped_count"`
	FailedCount         int     `db:"failed_count" json:"failed_count"`
	SummaryJSON         *string `db:"summary_json" json:"-"`
	AccountSnapshotJSON *string `db:"account_snapshot_json" json:"-"`
	LastErrorCode       *string `db:"last_error_code" json:"last_error_code"`
	LastErrorMessage    *string `db:"last_error_message" json:"last_error_message"`
	ScheduledAt         *string `db:"scheduled_at" json:"scheduled_at"`
	AvailableAt         *string `db:"available_at" json:"available_at"`
	LockedAt            *string `db:"locked_at" json:"-"`
	LockedBy            *string `db:"locked_by" json:"-"`
	StartedAt           *string `db:"started_at" json:"started_at"`
	FinishedAt          *string `db:"finished_at" json:"finished_at"`
	CreatedAt           string  `db:"created_at" json:"created_at"`
	UpdatedAt           string  `db:"updated_at" json:"updated_at"`
}

type SignRun struct {
	ID             int64   `db:"id" json:"id"`
	RunNo          string  `db:"run_no" json:"run_no"`
	TaskID         int64   `db:"task_id" json:"task_id"`
	AttemptNo      int     `db:"attempt_no" json:"attempt_no"`
	PluginCode     string  `db:"plugin_code" json:"plugin_code"`
	PluginVersion  *string `db:"plugin_version" json:"plugin_version"`
	WorkerID       *string `db:"worker_id" json:"worker_id"`
	Status         string  `db:"status" json:"status"`
	TotalCount     int     `db:"total_count" json:"total_count"`
	SuccessCount   int     `db:"success_count" json:"success_count"`
	AlreadyCount   int     `db:"already_count" json:"already_count"`
	SkippedCount   int     `db:"skipped_count" json:"skipped_count"`
	FailedCount    int     `db:"failed_count" json:"failed_count"`
	ErrorCode      *string `db:"error_code" json:"error_code"`
	ErrorMessage   *string `db:"error_message" json:"error_message"`
	ExceptionClass *string `db:"exception_class" json:"exception_class"`
	SummaryJSON    *string `db:"summary_json" json:"-"`
	StartedAt      string  `db:"started_at" json:"started_at"`
	FinishedAt     *string `db:"finished_at" json:"finished_at"`
	DurationMs     *int    `db:"duration_ms" json:"duration_ms"`
	CreatedAt      string  `db:"created_at" json:"created_at"`
}

type SignRecord struct {
	ID             int64   `db:"id" json:"id"`
	TaskID         int64   `db:"task_id" json:"task_id"`
	RunID          int64   `db:"run_id" json:"run_id"`
	ParentRecordID *int64  `db:"parent_record_id" json:"parent_record_id"`
	UserID         int64   `db:"user_id" json:"user_id"`
	AccountID      *int64  `db:"account_id" json:"account_id"`
	PluginCode     string  `db:"plugin_code" json:"plugin_code"`
	RecordKey      string  `db:"record_key" json:"record_key"`
	SequenceNo     int     `db:"sequence_no" json:"sequence_no"`
	SchemaVersion  int     `db:"schema_version" json:"schema_version"`
	Action         string  `db:"action" json:"action"`
	TargetType     *string `db:"target_type" json:"target_type"`
	TargetID       *string `db:"target_id" json:"target_id"`
	TargetName     *string `db:"target_name" json:"target_name"`
	Status         string  `db:"status" json:"status"`
	ResultCode     *string `db:"result_code" json:"result_code"`
	Message        *string `db:"message" json:"message"`
	RewardJSON     *string `db:"reward_json" json:"-"`
	MetricsJSON    *string `db:"metrics_json" json:"-"`
	SafeResultJSON *string `db:"safe_result_json" json:"-"`
	StartedAt      *string `db:"started_at" json:"started_at"`
	FinishedAt     *string `db:"finished_at" json:"finished_at"`
	DurationMs     *int    `db:"duration_ms" json:"duration_ms"`
	CreatedAt      string  `db:"created_at" json:"created_at"`
}
