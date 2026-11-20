package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/dkolancs/crashlab/internal/model"

	_ "modernc.org/sqlite"
)

// Store persists experiments, samples, and reports in a local SQLite file.
type Store struct {
	db *sql.DB
}

// Open creates the schema if needed and returns a ready store.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS experiments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  status TEXT NOT NULL,
  phase TEXT NOT NULL,
  target TEXT NOT NULL,
  delay_ms INTEGER NOT NULL,
  baseline_ms INTEGER NOT NULL,
  duration_ms INTEGER NOT NULL,
  recovery_ms INTEGER NOT NULL,
  entry_url TEXT NOT NULL,
  entry_service TEXT NOT NULL,
  compose_file TEXT NOT NULL,
  project TEXT NOT NULL,
  started_at INTEGER NOT NULL,
  finished_at INTEGER,
  error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS services (
  experiment_id INTEGER NOT NULL,
  name TEXT NOT NULL,
  container_id TEXT NOT NULL,
  image TEXT NOT NULL,
  ports TEXT NOT NULL,
  host_ports TEXT NOT NULL,
  networks TEXT NOT NULL,
  depends_on TEXT NOT NULL,
  health TEXT NOT NULL,
  status TEXT NOT NULL,
  restart_count INTEGER NOT NULL,
  PRIMARY KEY (experiment_id, name)
);
CREATE TABLE IF NOT EXISTS faults (
  experiment_id INTEGER PRIMARY KEY,
  service TEXT NOT NULL,
  delay_ms INTEGER NOT NULL,
  injected_at INTEGER,
  removed_at INTEGER
);
CREATE TABLE IF NOT EXISTS metric_samples (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  experiment_id INTEGER NOT NULL,
  ts INTEGER NOT NULL,
  phase TEXT NOT NULL,
  source TEXT NOT NULL,
  service TEXT NOT NULL,
  name TEXT NOT NULL,
  value REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_samples_exp ON metric_samples(experiment_id, ts);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  experiment_id INTEGER NOT NULL,
  ts INTEGER NOT NULL,
  kind TEXT NOT NULL,
  service TEXT NOT NULL,
  message TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS experiment_results (
  experiment_id INTEGER PRIMARY KEY,
  report_text TEXT NOT NULL,
  summary_json TEXT NOT NULL
);
`)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	for _, stmt := range []string{
		`ALTER TABLE experiments ADD COLUMN fault_type TEXT NOT NULL DEFAULT 'latency'`,
		`ALTER TABLE experiments ADD COLUMN loss_percent REAL NOT NULL DEFAULT 0`,
		`ALTER TABLE experiments ADD COLUMN cpu_percent REAL NOT NULL DEFAULT 0`,
		`ALTER TABLE faults ADD COLUMN fault_type TEXT NOT NULL DEFAULT 'latency'`,
		`ALTER TABLE faults ADD COLUMN loss_percent REAL NOT NULL DEFAULT 0`,
		`ALTER TABLE faults ADD COLUMN cpu_percent REAL NOT NULL DEFAULT 0`,
		`ALTER TABLE faults ADD COLUMN container_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE faults ADD COLUMN deadline_at INTEGER`,
		`ALTER TABLE experiments ADD COLUMN expect_failure INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := s.db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// CreateExperiment inserts a new running experiment and returns it with an id.
func (s *Store) CreateExperiment(e model.Experiment) (model.Experiment, error) {
	res, err := s.db.Exec(`
INSERT INTO experiments (
  name, status, phase, target, delay_ms, baseline_ms, duration_ms, recovery_ms,
  entry_url, entry_service, compose_file, project, started_at, finished_at, error,
  fault_type, loss_percent, cpu_percent, expect_failure
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Name, e.Status, e.Phase, e.Target, e.Delay.Milliseconds(), e.Baseline.Milliseconds(),
		e.Duration.Milliseconds(), e.Recovery.Milliseconds(), e.EntryURL, e.EntryService,
		e.ComposeFile, e.Project, e.StartedAt.UnixMilli(), nullTime(e.FinishedAt), e.Error,
		faultTypeOrLatency(e.FaultType), e.LossPercent, e.CPUPercent, boolInt(e.ExpectFailure),
	)
	if err != nil {
		return e, fmt.Errorf("insert experiment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return e, err
	}
	e.ID = id
	return e, nil
}

// UpdateExperiment writes mutable run fields.
func (s *Store) UpdateExperiment(e model.Experiment) error {
	_, err := s.db.Exec(`
UPDATE experiments SET
  status = ?, phase = ?, project = ?, finished_at = ?, error = ?
WHERE id = ?`,
		e.Status, e.Phase, e.Project, nullTime(e.FinishedAt), e.Error, e.ID,
	)
	if err != nil {
		return fmt.Errorf("update experiment: %w", err)
	}
	return nil
}

// GetExperiment loads one experiment.
func (s *Store) GetExperiment(id int64) (model.Experiment, error) {
	row := s.db.QueryRow(`
SELECT id, name, status, phase, target, delay_ms, baseline_ms, duration_ms, recovery_ms,
       entry_url, entry_service, compose_file, project, started_at, finished_at, error,
       fault_type, loss_percent, cpu_percent, expect_failure
FROM experiments WHERE id = ?`, id)
	e, err := scanExperiment(row)
	if err != nil {
		return e, fmt.Errorf("get experiment: %w", err)
	}
	return e, nil
}

// ListExperiments returns the newest experiments.
func (s *Store) ListExperiments(limit int) ([]model.Experiment, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
SELECT id, name, status, phase, target, delay_ms, baseline_ms, duration_ms, recovery_ms,
       entry_url, entry_service, compose_file, project, started_at, finished_at, error,
       fault_type, loss_percent, cpu_percent, expect_failure
FROM experiments ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Experiment
	for rows.Next() {
		e, err := scanExperiment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListCompletedExperiments returns every completed run, oldest first.
func (s *Store) ListCompletedExperiments() ([]model.Experiment, error) {
	rows, err := s.db.Query(`
SELECT id, name, status, phase, target, delay_ms, baseline_ms, duration_ms, recovery_ms,
       entry_url, entry_service, compose_file, project, started_at, finished_at, error,
       fault_type, loss_percent, cpu_percent, expect_failure
FROM experiments
WHERE status = ?
ORDER BY id ASC`, model.StatusCompleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Experiment
	for rows.Next() {
		exp, err := scanExperiment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, exp)
	}
	return out, rows.Err()
}

// ListNamedExperiments returns older completed runs of one experiment name, newest first.
func (s *Store) ListNamedExperiments(name string, beforeID int64) ([]model.Experiment, error) {
	rows, err := s.db.Query(`
SELECT id, name, status, phase, target, delay_ms, baseline_ms, duration_ms, recovery_ms,
       entry_url, entry_service, compose_file, project, started_at, finished_at, error,
       fault_type, loss_percent, cpu_percent, expect_failure
FROM experiments
WHERE name = ? AND id < ? AND status = ?
ORDER BY id DESC LIMIT 20`, name, beforeID, model.StatusCompleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Experiment
	for rows.Next() {
		exp, err := scanExperiment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, exp)
	}
	return out, rows.Err()
}

// LatestExperiment returns the newest experiment if one exists.
func (s *Store) LatestExperiment() (model.Experiment, bool, error) {
	list, err := s.ListExperiments(1)
	if err != nil {
		return model.Experiment{}, false, err
	}
	if len(list) == 0 {
		return model.Experiment{}, false, nil
	}
	return list[0], true, nil
}

// SaveServices replaces the topology snapshot for an experiment.
func (s *Store) SaveServices(experimentID int64, services []model.Service) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM services WHERE experiment_id = ?`, experimentID); err != nil {
		return err
	}
	for _, svc := range services {
		_, err := tx.Exec(`
INSERT INTO services (
  experiment_id, name, container_id, image, ports, host_ports, networks, depends_on, health, status, restart_count
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			experimentID, svc.Name, svc.ContainerID, svc.Image, joinStrings(svc.Ports), joinInts(svc.HostPorts),
			joinStrings(svc.Networks), joinStrings(svc.DependsOn), svc.Health, svc.Status, svc.RestartCount,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListServices returns the saved topology for an experiment.
func (s *Store) ListServices(experimentID int64) ([]model.Service, error) {
	rows, err := s.db.Query(`
SELECT name, container_id, image, ports, host_ports, networks, depends_on, health, status, restart_count
FROM services WHERE experiment_id = ? ORDER BY name`, experimentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Service
	for rows.Next() {
		var svc model.Service
		var ports, hostPorts, networks, depends string
		if err := rows.Scan(&svc.Name, &svc.ContainerID, &svc.Image, &ports, &hostPorts, &networks, &depends, &svc.Health, &svc.Status, &svc.RestartCount); err != nil {
			return nil, err
		}
		svc.Ports = splitStrings(ports)
		svc.HostPorts = splitInts(hostPorts)
		svc.Networks = splitStrings(networks)
		svc.DependsOn = splitStrings(depends)
		out = append(out, svc)
	}
	return out, rows.Err()
}

// UpsertFault inserts or replaces the fault row.
func (s *Store) UpsertFault(f model.Fault) error {
	_, err := s.db.Exec(`
INSERT INTO faults (experiment_id, service, delay_ms, injected_at, removed_at, fault_type, loss_percent, cpu_percent, container_id, deadline_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(experiment_id) DO UPDATE SET
  service = excluded.service,
  delay_ms = excluded.delay_ms,
  injected_at = excluded.injected_at,
  removed_at = excluded.removed_at,
  fault_type = excluded.fault_type,
  loss_percent = excluded.loss_percent,
  cpu_percent = excluded.cpu_percent,
  container_id = excluded.container_id,
  deadline_at = excluded.deadline_at`,
		f.ExperimentID, f.Service, f.Delay.Milliseconds(), nullTime(f.InjectedAt), nullTime(f.RemovedAt),
		faultTypeOrLatency(f.Type), f.LossPercent, f.CPUPercent, f.ContainerID, nullTime(f.Deadline),
	)
	if err != nil {
		return fmt.Errorf("upsert fault: %w", err)
	}
	return nil
}

// GetFault loads the fault for an experiment.
func (s *Store) GetFault(experimentID int64) (model.Fault, bool, error) {
	row := s.db.QueryRow(`SELECT service, delay_ms, injected_at, removed_at, fault_type, loss_percent, cpu_percent, container_id, deadline_at FROM faults WHERE experiment_id = ?`, experimentID)
	var f model.Fault
	var delay int64
	var injected, removed, deadline sql.NullInt64
	f.ExperimentID = experimentID
	if err := row.Scan(&f.Service, &delay, &injected, &removed, &f.Type, &f.LossPercent, &f.CPUPercent, &f.ContainerID, &deadline); err != nil {
		if err == sql.ErrNoRows {
			return f, false, nil
		}
		return f, false, err
	}
	f.Delay = time.Duration(delay) * time.Millisecond
	f.InjectedAt = timePtr(injected)
	f.RemovedAt = timePtr(removed)
	f.Deadline = timePtr(deadline)
	return f, true, nil
}

// ListOpenFaults returns faults that were injected and not marked removed.
func (s *Store) ListOpenFaults() ([]model.Fault, error) {
	rows, err := s.db.Query(`SELECT experiment_id, service, delay_ms, injected_at, removed_at, fault_type, loss_percent, cpu_percent, container_id, deadline_at FROM faults WHERE removed_at IS NULL AND injected_at IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Fault
	for rows.Next() {
		var f model.Fault
		var delay int64
		var injected, removed, deadline sql.NullInt64
		if err := rows.Scan(&f.ExperimentID, &f.Service, &delay, &injected, &removed, &f.Type, &f.LossPercent, &f.CPUPercent, &f.ContainerID, &deadline); err != nil {
			return nil, err
		}
		f.Delay = time.Duration(delay) * time.Millisecond
		f.InjectedAt = timePtr(injected)
		f.RemovedAt = timePtr(removed)
		f.Deadline = timePtr(deadline)
		out = append(out, f)
	}
	return out, rows.Err()
}

// InsertSamples appends a batch of measurements.
func (s *Store) InsertSamples(samples []model.Sample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO metric_samples (experiment_id, ts, phase, source, service, name, value) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, sm := range samples {
		if _, err := stmt.Exec(sm.ExperimentID, sm.At.UnixMilli(), sm.Phase, sm.Source, sm.Service, sm.Name, sm.Value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListSamples returns measurements ordered by time.
func (s *Store) ListSamples(experimentID int64) ([]model.Sample, error) {
	rows, err := s.db.Query(`
SELECT experiment_id, ts, phase, source, service, name, value
FROM metric_samples WHERE experiment_id = ? ORDER BY ts, id`, experimentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Sample
	for rows.Next() {
		var sm model.Sample
		var ts int64
		if err := rows.Scan(&sm.ExperimentID, &ts, &sm.Phase, &sm.Source, &sm.Service, &sm.Name, &sm.Value); err != nil {
			return nil, err
		}
		sm.At = time.UnixMilli(ts).UTC()
		out = append(out, sm)
	}
	return out, rows.Err()
}

// AddEvent appends a timeline event.
func (s *Store) AddEvent(e model.Event) error {
	_, err := s.db.Exec(`INSERT INTO events (experiment_id, ts, kind, service, message) VALUES (?, ?, ?, ?, ?)`,
		e.ExperimentID, e.At.UnixMilli(), e.Kind, e.Service, e.Message)
	if err != nil {
		return fmt.Errorf("add event: %w", err)
	}
	return nil
}

// ListEvents returns the timeline in order.
func (s *Store) ListEvents(experimentID int64) ([]model.Event, error) {
	rows, err := s.db.Query(`SELECT id, experiment_id, ts, kind, service, message FROM events WHERE experiment_id = ? ORDER BY ts, id`, experimentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		var ts int64
		if err := rows.Scan(&e.ID, &e.ExperimentID, &ts, &e.Kind, &e.Service, &e.Message); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(ts).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// SaveResult stores the formatted report and its JSON summary.
func (s *Store) SaveResult(experimentID int64, text, summary string) error {
	_, err := s.db.Exec(`
INSERT INTO experiment_results (experiment_id, report_text, summary_json) VALUES (?, ?, ?)
ON CONFLICT(experiment_id) DO UPDATE SET report_text = excluded.report_text, summary_json = excluded.summary_json`,
		experimentID, text, summary)
	if err != nil {
		return fmt.Errorf("save result: %w", err)
	}
	return nil
}

// GetResult returns the stored report text and summary JSON.
func (s *Store) GetResult(experimentID int64) (string, string, bool, error) {
	var text, summary string
	err := s.db.QueryRow(`SELECT report_text, summary_json FROM experiment_results WHERE experiment_id = ?`, experimentID).Scan(&text, &summary)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return text, summary, true, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanExperiment(row scanner) (model.Experiment, error) {
	var e model.Experiment
	var delay, baseline, duration, recovery, started int64
	var expectFailure int
	var finished sql.NullInt64
	err := row.Scan(
		&e.ID, &e.Name, &e.Status, &e.Phase, &e.Target, &delay, &baseline, &duration, &recovery,
		&e.EntryURL, &e.EntryService, &e.ComposeFile, &e.Project, &started, &finished, &e.Error,
		&e.FaultType, &e.LossPercent, &e.CPUPercent, &expectFailure,
	)
	if err != nil {
		return e, err
	}
	e.Delay = time.Duration(delay) * time.Millisecond
	e.Baseline = time.Duration(baseline) * time.Millisecond
	e.Duration = time.Duration(duration) * time.Millisecond
	e.Recovery = time.Duration(recovery) * time.Millisecond
	e.StartedAt = time.UnixMilli(started).UTC()
	e.FinishedAt = timePtr(finished)
	e.ExpectFailure = expectFailure != 0
	return e, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func faultTypeOrLatency(v string) string {
	if v == "" {
		return model.FaultLatency
	}
	return v
}

func nullTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func timePtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

func joinStrings(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return strings.Join(v, "\n")
}

func splitStrings(v string) []string {
	if v == "" {
		return []string{}
	}
	return strings.Split(v, "\n")
}

func joinInts(v []int) string {
	if len(v) == 0 {
		return ""
	}
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = fmt.Sprintf("%d", n)
	}
	return strings.Join(parts, ",")
}

func splitInts(v string) []int {
	if v == "" {
		return []int{}
	}
	parts := strings.Split(v, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err == nil {
			out = append(out, n)
		}
	}
	return out
}
