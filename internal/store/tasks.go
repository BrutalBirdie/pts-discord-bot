package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

const taskCols = `id, name, guild_id, enabled, busy, slash, trigger, steps, paused_reason, created_by, updated_by, updated_at`

func scanTask(r pgx.Row) (tasks.Task, error) {
	var t tasks.Task
	err := r.Scan(&t.ID, &t.Name, &t.GuildID, &t.Enabled, &t.Busy, &t.Slash, &t.Trigger, &t.Steps, &t.PausedReason, &t.CreatedBy, &t.UpdatedBy, &t.UpdatedAt)
	return t, err
}

func (s *Store) Task(ctx context.Context, id int64) (tasks.Task, error) {
	t, err := scanTask(s.pool.QueryRow(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = $1 AND deleted_at IS NULL`, id))
	return t, noRows(err)
}

func (s *Store) ListTasks(ctx context.Context, sc Scope) ([]tasks.Task, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+taskCols+` FROM tasks WHERE deleted_at IS NULL AND `+inScope("guild_id", "1")+` ORDER BY lower(name), id`, sc.param())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (tasks.Task, error) { return scanTask(r) })
}

func (s *Store) CreateTask(ctx context.Context, t tasks.Task, by string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO tasks (name, guild_id, enabled, busy, slash, trigger, steps, created_by, updated_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8) RETURNING id`, t.Name, t.GuildID, t.Enabled, t.Busy, t.Slash, t.Trigger, steps(t.Steps), by).Scan(&id)
	return id, err
}

// UpdateTask saves the editor's state and clears a pause reason.
func (s *Store) UpdateTask(ctx context.Context, t tasks.Task, by string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE tasks SET name = $2, guild_id = $3, enabled = $4, busy = $5, slash = $6, trigger = $7, steps = $8, paused_reason = '', updated_by = $9, updated_at = now() WHERE id = $1 AND deleted_at IS NULL`,
		t.ID, t.Name, t.GuildID, t.Enabled, t.Busy, t.Slash, t.Trigger, steps(t.Steps), by)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetTaskEnabled pauses or resumes a task. reason is shown on paused tasks.
// The runner pausing a task as "system" keeps updated_by, RunScope reads it.
// So does a Discord user, else resuming a task that uses shared items would break its runs.
func (s *Store) SetTaskEnabled(ctx context.Context, id int64, enabled bool, reason, by string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE tasks SET enabled = $2, paused_reason = $3, updated_by = CASE WHEN $4 = 'system' OR $4 LIKE 'discord:%' THEN updated_by ELSE $4 END, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL`, id, enabled, reason, by)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteTask(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM tasks WHERE id = $1`, id)
	return err
}

func steps(s []tasks.Step) []tasks.Step {
	if s == nil {
		return []tasks.Step{}
	}
	return s
}

const (
	RunRunning = "running"
	RunDone    = "done"
	RunFailed  = "failed"
	RunSkipped = "skipped"

	SourceSchedule = "schedule"
	SourceManual   = "manual"
	SourceEvent    = "event"
	SourceDiscord  = "discord" // slash command
)

type Run struct {
	ID         int64
	TaskID     *int64
	Title      string
	GuildID    string
	Source     string
	Actor      string
	Status     string
	FailedStep *int // 1-based
	Error      string
	Detail     string
	StartedAt  time.Time
	FinishedAt *time.Time
}

func (r Run) Duration() time.Duration {
	if r.FinishedAt == nil {
		return time.Since(r.StartedAt)
	}
	return r.FinishedAt.Sub(r.StartedAt)
}

const runCols = `id, task_id, title, guild_id, source, actor, status, failed_step, error, detail, started_at, finished_at`

func scanRun(r pgx.Row) (Run, error) {
	var run Run
	err := r.Scan(&run.ID, &run.TaskID, &run.Title, &run.GuildID, &run.Source, &run.Actor, &run.Status, &run.FailedStep, &run.Error, &run.Detail, &run.StartedAt, &run.FinishedAt)
	return run, err
}

func (s *Store) StartRun(ctx context.Context, r Run) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO runs (task_id, title, guild_id, source, actor, detail) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		r.TaskID, r.Title, r.GuildID, r.Source, r.Actor, r.Detail).Scan(&id)
	return id, err
}

func (s *Store) FinishRun(ctx context.Context, id int64, status string, failedStep *int, errText string) error {
	_, err := s.pool.Exec(ctx, `UPDATE runs SET status = $2, failed_step = $3, error = $4, finished_at = now() WHERE id = $1`, id, status, failedStep, errText)
	return err
}

// AddMissedRun records a scheduled run that did not happen because the app was down.
func (s *Store) AddMissedRun(ctx context.Context, taskID int64, title, guildID string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO runs (task_id, title, guild_id, source, status, error, started_at, finished_at)
VALUES ($1, $2, $3, 'schedule', 'skipped', 'Missed, the app was not running.', $4, $4)`, taskID, title, guildID, at)
	return err
}

// Heartbeat is written every minute, after a restart it tells which runs were missed.
func (s *Store) Heartbeat(ctx context.Context) (time.Time, error) {
	var v string
	err := s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'heartbeat'`).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, v)
}

func (s *Store) SetHeartbeat(ctx context.Context, t time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO settings (key, value) VALUES ('heartbeat', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, t.UTC().Format(time.RFC3339))
	return err
}

// FailInterruptedRuns marks runs that were still running when the app stopped.
func (s *Store) FailInterruptedRuns(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE runs SET status = 'failed', error = 'The app restarted during the run.', finished_at = now() WHERE status = 'running'`)
	return err
}

// RunQuery filters runs. TaskID 0 means all tasks.
type RunQuery struct {
	Scope         Scope
	TaskID        int64
	Since         time.Time
	Status        string
	Limit, Offset int
}

const runWhere = `($1 = 0 OR task_id = $1) AND started_at >= $2 AND ($3 = '' OR status = $3) AND `

func (s *Store) ListRuns(ctx context.Context, q RunQuery) ([]Run, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+runCols+` FROM runs WHERE `+runWhere+inScope("guild_id", "6")+`
ORDER BY id DESC LIMIT $4 OFFSET $5`, q.TaskID, q.Since, q.Status, q.Limit, q.Offset, q.Scope.param())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Run, error) { return scanRun(r) })
}

func (s *Store) CountRuns(ctx context.Context, q RunQuery) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE `+runWhere+inScope("guild_id", "4"),
		q.TaskID, q.Since, q.Status, q.Scope.param()).Scan(&n)
	return n, err
}

// LastRuns returns the newest run of every task.
func (s *Store) LastRuns(ctx context.Context) (map[int64]Run, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (task_id) `+runCols+` FROM runs WHERE task_id IS NOT NULL ORDER BY task_id, id DESC`)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Run, error) { return scanRun(r) })
	out := make(map[int64]Run, len(list))
	for _, r := range list {
		out[*r.TaskID] = r
	}
	return out, err
}
