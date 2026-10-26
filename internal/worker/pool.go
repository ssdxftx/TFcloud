package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"tfcloud/internal/config"
	"tfcloud/internal/database"
	"tfcloud/internal/service"
)

// Pool 统一管理常驻后台任务：调度器、签到 Worker、补签、直播挂机、邮件与维护。
type Pool struct {
	cfg    *config.Config
	store  *database.Store
	tasks  *service.TaskService
	mail   *service.MailService
	live   *service.LiveService
	logger *slog.Logger
	hostID string
	wg     sync.WaitGroup
}

func NewPool(cfg *config.Config, store *database.Store, tasks *service.TaskService,
	mail *service.MailService, live *service.LiveService, logger *slog.Logger) *Pool {
	return &Pool{cfg: cfg, store: store, tasks: tasks, mail: mail, live: live, logger: logger,
		hostID: service.HostID()}
}

func (p *Pool) Start(ctx context.Context) {
	if p.cfg.SignSchedulerEnabled {
		p.goLoop(ctx, "scheduler", p.cfg.SignSchedulerInterval, func() { p.tasks.DispatchDue(100) })
		p.goLoop(ctx, "nightly-retry", 10*time.Minute, func() { p.tasks.RequeueFailedToday(200) })
	}
	if p.cfg.SignWorkerEnabled {
		for i := 1; i <= maxInt(1, p.cfg.SignWorkerCount); i++ {
			id := p.workerID("sign", i)
			p.goWorker(ctx, "sign-worker-"+itoa(i), 2*time.Second, func() {
				if task, ok := p.tasks.Claim(id, service.QueueDefault); ok {
					p.tasks.ExecuteTask(task, id)
				}
			})
		}
	}
	if p.cfg.TiebaRetryEnabled {
		id := p.workerID("tieba", 1)
		p.goWorker(ctx, "tieba-retry", 30*time.Second, func() {
			if task, ok := p.tasks.Claim(id, service.QueueTiebaRetry); ok {
				p.tasks.ExecuteTask(task, id)
			}
		})
	}
	if p.cfg.BilibiliLiveEnabled {
		for i := 1; i <= maxInt(1, p.cfg.BilibiliLiveWorkers); i++ {
			id := p.workerID("live", i)
			p.goWorker(ctx, "live-worker-"+itoa(i), 5*time.Second, func() {
				if task, ok := p.tasks.Claim(id, service.QueueBilibiliLive); ok {
					p.live.RunSession(task, id)
				}
			})
		}
	}
	if p.cfg.MailWorkerEnabled {
		p.goWorker(ctx, "mail-worker", p.cfg.MailWorkerInterval, func() { p.mail.ProcessPending(20) })
	}
	if p.cfg.MaintenanceEnabled {
		p.goWorker(ctx, "maintenance", time.Minute, func() { p.maintain() })
	}
}

// goLoop 用于轻量定时任务，不阻塞。
func (p *Pool) goLoop(ctx context.Context, name string, interval time.Duration, fn func()) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.safe(name, fn)
			}
		}
	}()
}

// goWorker 空闲时按 idle 间隔轮询，有任务时立即继续，避免空转。
func (p *Pool) goWorker(ctx context.Context, name string, idle time.Duration, fn func()) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			start := time.Now()
			p.safe(name, fn)
			if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
				select {
				case <-ctx.Done():
					return
				case <-time.After(idle):
				}
			}
		}
	}()
}

func (p *Pool) safe(name string, fn func()) {
	defer func() {
		if rec := recover(); rec != nil {
			p.logger.Error("worker panic", slog.String("worker", name), slog.Any("panic", rec))
		}
	}()
	fn()
}

func (p *Pool) Wait() { p.wg.Wait() }

func (p *Pool) maintain() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := p.store.Write.ExecContext(ctx, `DELETE FROM TF_sessions WHERE expires_at < ? OR
		(revoked_at IS NOT NULL AND revoked_at < ?)`,
		database.Now(), database.Ts(time.Now().Add(-30*24*time.Hour))); err != nil {
		p.logger.Warn("cleanup sessions failed", slog.String("error", err.Error()))
	}
	_, _ = p.store.Write.ExecContext(ctx, `DELETE FROM TF_email_verification_codes WHERE expires_at < ?`,
		database.Ts(time.Now().Add(-24*time.Hour)))
	_, _ = p.store.Write.ExecContext(ctx, `DELETE FROM TF_password_change_codes WHERE consumed_at IS NOT NULL AND consumed_at < ?`,
		database.Ts(time.Now().Add(-7*24*time.Hour)))
	_, _ = p.store.Write.ExecContext(ctx, `DELETE FROM TF_platform_auth_flows WHERE expires_at < ?`,
		database.Now())
	_, _ = p.store.Write.ExecContext(ctx, `DELETE FROM TF_sign_record_payloads WHERE expires_at < ?`, database.Now())
	if _, err := p.store.Write.ExecContext(ctx, `UPDATE TF_bilibili_live_sessions SET status='expired',
		finished_at=?, updated_at=? WHERE status IN ('discovering','running') AND expires_at < ?`,
		database.Now(), database.Now(), database.Now()); err != nil {
		p.logger.Warn("expire live sessions failed", slog.String("error", err.Error()))
	}
	if n := p.tasks.RecoverStale(int(p.cfg.TaskLockTimeout.Seconds()), 100); n > 0 {
		p.logger.Warn("recovered stale tasks", slog.Int("count", n))
	}
	if err := p.store.Checkpoint(ctx); err != nil {
		p.logger.Warn("wal checkpoint failed", slog.String("error", err.Error()))
	}
}

func (p *Pool) workerID(kind string, index int) string {
	return p.hostID + "-" + kind + "-" + itoa(index)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [8]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
