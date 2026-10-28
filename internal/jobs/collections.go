package jobs

// Collection names used by the job queue.
//
// They mirror the constants in the pocketbase package, which owns the
// schema. They are repeated here so that the jobs package does not
// have to import the persistence package, which would create a cycle.
const (
	// CollectionJobs stores the job metadata.
	CollectionJobs = "jobs"
	// CollectionArtifacts stores the files a job produced or consumed.
	CollectionArtifacts = "artifacts"
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

// Artifact kind values stored in artifacts.kind.
const (
	// ArtifactKindInput is a file supplied by the user.
	ArtifactKindInput = "input"
	// ArtifactKindOutput is a file produced by the job.
	ArtifactKindOutput = "output"
)
