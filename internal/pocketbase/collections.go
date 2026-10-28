package pocketbase

// Collection names used by Valkyrie.
const (
	// CollectionJobs stores the job metadata. It is the generic queue
	// table: what a job means is defined by the application that
	// registers the runner.
	CollectionJobs = "jobs"
	// CollectionArtifacts stores the files a job produced. The kind
	// field records what the file is, so the UI can tell an output
	// from an input.
	CollectionArtifacts = "artifacts"
	// CollectionAppSettings stores configuration and encrypted secrets.
	CollectionAppSettings = "app_settings"
)

// Job status values stored in jobs.status.
const (
	// StatusPending means the job is queued and has not started yet.
	StatusPending = "pending"
	// StatusRunning means the job is being executed.
	StatusRunning = "running"
	// StatusCompleted means the job finished successfully.
	StatusCompleted = "completed"
	// StatusFailed means the job ended with an error.
	StatusFailed = "failed"
	// StatusCancelled means the job was cancelled by the user.
	StatusCancelled = "cancelled"
)

// AllJobStatuses lists every valid job status.
func AllJobStatuses() []string {
	return []string{StatusPending, StatusRunning, StatusCompleted, StatusFailed, StatusCancelled}
}

// Artifact kind values stored in artifacts.kind.
//
// The scaffold defines two kinds that every application can use. Add
// your own by extending the select field in the migration.
const (
	// ArtifactKindInput is a file supplied by the user, such as an
	// upload the job consumes.
	ArtifactKindInput = "input"
	// ArtifactKindOutput is a file produced by the job.
	ArtifactKindOutput = "output"
)
