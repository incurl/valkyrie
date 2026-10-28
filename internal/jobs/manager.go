package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/incurl/valkyrie/internal/console"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
)

// EventEmitter is notified whenever a job changes state. The Wails
// layer implements it to push updates to the frontend.
type EventEmitter interface {
	// EmitJobUpdated publishes an updated job.
	EmitJobUpdated(job Job)
}

// Manager orchestrates jobs.
//
// The manager owns the lifecycle only: persistence, scheduling,
// cancellation and the status transitions. The work is delegated to the
// Runner.
type Manager struct {
	app     core.App
	emitter EventEmitter
	runner  Runner

	// emitterFn lazily resolves the emitter, if one was configured.
	emitterFn func() EventEmitter

	// runnerFn lazily resolves the runner, if one was configured.
	runnerFn func() Runner

	// log records the job traffic for the debug console. It may be nil.
	log *console.Log

	// mu guards the cancellation map.
	mu sync.Mutex
	// cancels holds the cancel function of each running job.
	cancels map[string]context.CancelFunc

	// queue bounds how many jobs run at once.
	queue chan struct{}
}

// ManagerOption configures a Manager.
type ManagerOption func(*Manager)

// WithEmitter sets the job update emitter.
func WithEmitter(e EventEmitter) ManagerOption {
	return func(m *Manager) { m.emitter = e }
}

// WithEmitterFunc sets a lazily resolved job update emitter. It is used
// because the emitter needs the Wails application, which is created
// after the job manager.
func WithEmitterFunc(fn func() EventEmitter) ManagerOption {
	return func(m *Manager) { m.emitterFn = fn }
}

// WithRunner sets the runner that executes the jobs.
func WithRunner(r Runner) ManagerOption {
	return func(m *Manager) { m.runner = r }
}

// WithRunnerFunc sets a lazily resolved runner. It is used when the
// runner depends on the settings, which are read after the manager is
// created.
func WithRunnerFunc(fn func() Runner) ManagerOption {
	return func(m *Manager) { m.runnerFn = fn }
}

// WithLog attaches the debug console log, so that the job traffic is
// recorded for the console panel.
func WithLog(l *console.Log) ManagerOption {
	return func(m *Manager) { m.log = l }
}

// WithConcurrency sets the number of jobs executed in parallel.
func WithConcurrency(n int) ManagerOption {
	return func(m *Manager) {
		if n <= 0 {
			n = 1
		}
		m.queue = make(chan struct{}, n)
	}
}

// NewManager creates a job manager.
func NewManager(app core.App, opts ...ManagerOption) *Manager {
	m := &Manager{
		app:     app,
		cancels: make(map[string]context.CancelFunc),
		queue:   make(chan struct{}, 2),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
}

// App returns the underlying PocketBase app.
func (m *Manager) App() core.App { return m.app }

// SetEmitter attaches the job update emitter once it becomes available.
func (m *Manager) SetEmitter(e EventEmitter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emitter = e
}

// SetRunner attaches the runner once it becomes available.
func (m *Manager) SetRunner(r Runner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runner = r
}

// currentEmitter resolves the active emitter.
func (m *Manager) currentEmitter() EventEmitter {
	m.mu.Lock()
	emitter := m.emitter
	fn := m.emitterFn
	m.mu.Unlock()

	if emitter == nil && fn != nil {
		emitter = fn()
	}
	return emitter
}

// currentRunner resolves the active runner.
func (m *Manager) currentRunner() Runner {
	m.mu.Lock()
	runner := m.runner
	fn := m.runnerFn
	m.mu.Unlock()

	if runner == nil && fn != nil {
		runner = fn()
	}
	return runner
}

// Submit creates a job record and schedules it for execution.
//
// The job is persisted with status "pending" before it is queued, so the
// UI can display it immediately.
func (m *Manager) Submit(ctx context.Context, req Request) (Job, error) {
	if req.Prompt == "" {
		return Job{}, fmt.Errorf("a prompt is required")
	}

	col, err := m.app.FindCollectionByNameOrId(CollectionJobs)
	if err != nil {
		return Job{}, fmt.Errorf("find jobs collection: %w", err)
	}

	title := req.Title
	if title == "" {
		title = defaultTitle(req.Prompt)
	}

	record := core.NewRecord(col)
	record.Set("title", title)
	record.Set("prompt", req.Prompt)
	record.Set("status", StatusPending)
	if len(req.Params) > 0 {
		record.Set("params", req.Params)
	} else {
		record.Set("params", map[string]any{})
	}

	if err := m.app.Save(record); err != nil {
		return Job{}, fmt.Errorf("create job: %w", err)
	}

	job, err := m.toJob(record)
	if err != nil {
		return Job{}, err
	}

	// Link the uploaded inputs to the job now that it has an id.
	if err := m.linkArtifacts(record.Id, req.InputArtifactIDs); err != nil {
		return Job{}, err
	}

	go m.run(record.Id, req)

	return job, nil
}

// defaultTitle derives a short label from the prompt.
//
// It is the first line of the prompt, truncated, so the queue shows
// something meaningful before the application sets its own title.
func defaultTitle(prompt string) string {
	const max = 60
	title := prompt
	if idx := indexNewline(title); idx >= 0 {
		title = title[:idx]
	}
	title = trimSpace(title)
	if len(title) > max {
		// The cut must not split a rune in half: the byte at the cut
		// position has to be the start of a rune, so that the text
		// before it ends on a rune boundary.
		cut := max
		for cut > 0 && !isRuneStart(title[cut]) {
			cut--
		}
		title = title[:cut] + "…"
	}
	if title == "" {
		return "Job"
	}
	return title
}

// linkArtifacts binds the input artifacts to the job.
func (m *Manager) linkArtifacts(jobID string, artifactIDs []string) error {
	for _, artifactID := range artifactIDs {
		if artifactID == "" {
			continue
		}
		record, err := m.app.FindRecordById(CollectionArtifacts, artifactID)
		if err != nil {
			return fmt.Errorf("find artifact %q: %w", artifactID, err)
		}
		record.Set("job", jobID)
		if err := m.app.Save(record); err != nil {
			return fmt.Errorf("link artifact %q: %w", artifactID, err)
		}
	}
	return nil
}

// Cancel aborts a pending or running job.
func (m *Manager) Cancel(jobID string) error {
	m.mu.Lock()
	cancel, running := m.cancels[jobID]
	m.mu.Unlock()

	if running && cancel != nil {
		cancel()
	}

	record, err := m.app.FindRecordById(CollectionJobs, jobID)
	if err != nil {
		return fmt.Errorf("find job %q: %w", jobID, err)
	}

	status := record.GetString("status")
	if status == StatusCompleted || status == StatusCancelled {
		return nil
	}
	if status == StatusFailed {
		return fmt.Errorf("job %q already failed and cannot be cancelled", jobID)
	}

	record.Set("status", StatusCancelled)
	if err := m.app.Save(record); err != nil {
		return fmt.Errorf("cancel job %q: %w", jobID, err)
	}

	m.emitRecord(record)
	return nil
}

// DeleteJob removes a finished job from the queue.
//
// The artifacts the job produced are removed with it.
func (m *Manager) DeleteJob(jobID string) error {
	record, err := m.app.FindRecordById(CollectionJobs, jobID)
	if err != nil {
		return fmt.Errorf("find job %q: %w", jobID, err)
	}

	status := record.GetString("status")
	if status == StatusPending || status == StatusRunning {
		return fmt.Errorf("job %q is still running and cannot be deleted", jobID)
	}

	// Remove the artifacts the job produced, so that no orphan files
	// are left in the storage directory.
	artifacts, err := m.app.FindRecordsByFilter(
		CollectionArtifacts,
		"job = {:job}",
		"",
		0,
		0,
		dbx.Params{"job": jobID},
	)
	if err == nil {
		for _, artifact := range artifacts {
			if err := m.app.Delete(artifact); err != nil {
				return fmt.Errorf("delete artifact %q: %w", artifact.Id, err)
			}
		}
	}

	if err := m.app.Delete(record); err != nil {
		return fmt.Errorf("delete job %q: %w", jobID, err)
	}
	return nil
}

// ListJobs returns the recorded jobs, newest first.
func (m *Manager) ListJobs(limit int) ([]Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	records, err := m.app.FindRecordsByFilter(
		CollectionJobs,
		"",
		"-created",
		limit,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}

	jobs := make([]Job, 0, len(records))
	for _, record := range records {
		job, err := m.toJob(record)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// GetJob returns a single job.
func (m *Manager) GetJob(jobID string) (Job, error) {
	record, err := m.app.FindRecordById(CollectionJobs, jobID)
	if err != nil {
		return Job{}, fmt.Errorf("find job %q: %w", jobID, err)
	}
	return m.toJob(record)
}

// run executes a job.
//
// It acquires a slot in the concurrency queue, transitions the job to
// running, calls the runner and records the outcome.
func (m *Manager) run(jobID string, req Request) {
	// Wait for a free slot. The queue bounds the parallelism so that a
	// burst of submissions cannot flood a rate-limited backend.
	m.queue <- struct{}{}
	defer func() { <-m.queue }()

	record, err := m.app.FindRecordById(CollectionJobs, jobID)
	if err != nil {
		m.logError(jobID, fmt.Errorf("find job: %w", err))
		return
	}

	// The job may have been cancelled while it was waiting for a slot.
	if record.GetString("status") == StatusCancelled {
		return
	}

	runner := m.currentRunner()
	if runner == nil {
		m.failJob(jobID, 0, "no runner is registered. Register one before submitting jobs.")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancels[jobID] = cancel
	m.mu.Unlock()

	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.cancels, jobID)
		m.mu.Unlock()
	}()

	record.Set("status", StatusRunning)
	if err := m.app.Save(record); err != nil {
		m.logError(jobID, fmt.Errorf("mark running: %w", err))
		return
	}
	m.emitRecord(record)

	job, err := m.toJob(record)
	if err != nil {
		m.logError(jobID, err)
		return
	}

	m.logInfo(jobID, "the job started")

	result, runErr := runner.Run(ctx, job)
	if runErr != nil {
		// A cancelled job keeps its cancelled status: the user asked
		// for it, it is not a failure.
		if ctx.Err() != nil {
			m.logInfo(jobID, "the job was cancelled")
			return
		}
		code, message := describeError(runErr)
		m.failJob(jobID, code, message)
		return
	}

	m.completeJob(jobID, result)
}

// completeJob marks a job as completed and stores its result.
func (m *Manager) completeJob(jobID string, result *Result) {
	record, err := m.app.FindRecordById(CollectionJobs, jobID)
	if err != nil {
		m.logError(jobID, fmt.Errorf("find job: %w", err))
		return
	}

	record.Set("status", StatusCompleted)
	if result != nil && len(result.Payload) > 0 {
		record.Set("result", result.Payload)
	}

	if err := m.app.Save(record); err != nil {
		m.logError(jobID, fmt.Errorf("mark completed: %w", err))
		return
	}

	m.emitRecord(record)
	m.logInfo(jobID, "the job completed")
}

// failJob marks a job as failed and records the error.
func (m *Manager) failJob(jobID string, code int, message string) {
	record, err := m.app.FindRecordById(CollectionJobs, jobID)
	if err != nil {
		m.logError(jobID, fmt.Errorf("find job: %w", err))
		return
	}

	record.Set("status", StatusFailed)
	if code != 0 {
		record.Set("error_code", code)
	}
	record.Set("error_message", message)

	if err := m.app.Save(record); err != nil {
		m.logError(jobID, fmt.Errorf("mark failed: %w", err))
		return
	}

	m.emitRecord(record)
	m.logError(jobID, fmt.Errorf("%s", message))
}

// describeError extracts the code and the message from an error.
//
// A Runner may return a *Error to attach a numeric code; anything else
// is reported with its own text and no code.
func describeError(err error) (int, string) {
	var coded *Error
	if asError(err, &coded) {
		return coded.Code, coded.Error()
	}
	return 0, err.Error()
}

// asError unwraps a *Error from an error chain.
func asError(err error, target **Error) bool {
	for err != nil {
		if coded, ok := err.(*Error); ok {
			*target = coded
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// logInfo records a normal event for the debug console.
func (m *Manager) logInfo(jobID, message string) {
	if m.log == nil {
		return
	}
	m.log.Info("job "+jobID, message)
}

// logError records a failure for the debug console.
func (m *Manager) logError(jobID string, cause error) {
	if m.log == nil {
		return
	}
	m.log.Error("job "+jobID, cause.Error())
}

// emitRecord publishes a job update.
func (m *Manager) emitRecord(record *core.Record) {
	emitter := m.currentEmitter()
	if emitter == nil {
		return
	}
	job, err := m.toJob(record)
	if err != nil {
		return
	}
	emitter.EmitJobUpdated(job)
}

// toJob converts a PocketBase record into the frontend-facing job.
func (m *Manager) toJob(record *core.Record) (Job, error) {
	job := Job{
		ID:           record.Id,
		Title:        record.GetString("title"),
		Prompt:       record.GetString("prompt"),
		Params:       map[string]any{},
		Result:       map[string]any{},
		Status:       record.GetString("status"),
		ErrorMessage: record.GetString("error_message"),
		ArtifactIDs:  []string{},
		Created:      record.GetString("created"),
		Updated:      record.GetString("updated"),
	}

	if raw := record.Get("params"); raw != nil {
		if encoded, err := json.Marshal(raw); err == nil {
			_ = json.Unmarshal(encoded, &job.Params)
		}
	}
	if job.Params == nil {
		job.Params = map[string]any{}
	}

	if raw := record.Get("result"); raw != nil {
		if encoded, err := json.Marshal(raw); err == nil {
			_ = json.Unmarshal(encoded, &job.Result)
		}
	}
	if job.Result == nil {
		job.Result = map[string]any{}
	}

	if code := record.GetInt("error_code"); code != 0 {
		job.ErrorCode = code
	}

	// Collect the artifacts the job produced.
	records, err := m.app.FindRecordsByFilter(
		CollectionArtifacts,
		"job = {:job}",
		"-created",
		100,
		0,
		dbx.Params{"job": record.Id},
	)
	if err == nil {
		for _, artifactRecord := range records {
			job.ArtifactIDs = append(job.ArtifactIDs, artifactRecord.Id)
		}
	}

	return job, nil
}

// StoreArtifact saves a file produced by a job.
//
// The file is stored using PocketBase's native file field, so it is
// served by the built-in file route and inherits the collection's access
// rules.
func (m *Manager) StoreArtifact(jobID, kind string, data []byte, filename string, meta map[string]any) (Artifact, error) {
	if kind != ArtifactKindInput && kind != ArtifactKindOutput {
		return Artifact{}, fmt.Errorf("unknown artifact kind %q", kind)
	}
	if len(data) == 0 {
		return Artifact{}, fmt.Errorf("the file is empty")
	}

	col, err := m.app.FindCollectionByNameOrId(CollectionArtifacts)
	if err != nil {
		return Artifact{}, fmt.Errorf("find artifacts collection: %w", err)
	}

	if filename == "" {
		filename = "artifact." + DetectMediaType(data).Extension
	}

	file, err := filesystem.NewFileFromBytes(data, filename)
	if err != nil {
		return Artifact{}, fmt.Errorf("create file: %w", err)
	}

	record := core.NewRecord(col)
	if jobID != "" {
		record.Set("job", jobID)
	}
	record.Set("kind", kind)
	record.Set("file", file)
	record.Set("name", filename)
	record.Set("mime", MimeTypeOf(data))
	record.Set("bytes", len(data))

	if size, dimErr := DimensionsOf(data); dimErr == nil {
		record.Set("width", size.Width)
		record.Set("height", size.Height)
	}
	if len(meta) > 0 {
		record.Set("meta", meta)
	}

	if err := m.app.Save(record); err != nil {
		return Artifact{}, fmt.Errorf("save artifact: %w", err)
	}

	return m.toArtifact(record), nil
}
