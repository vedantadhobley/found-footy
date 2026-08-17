// TemporalConfig — env-driven settings for the Temporal workflow-orchestration adapter.
package config

import "time"

// TemporalConfig covers the Temporal client + worker adapter. Every
// binary that starts or executes workflows needs this.
//
// LoadFor validates HostPort before the worker opens external connections.
// Constructor checks remain as defense in depth for direct use.
type TemporalConfig struct {
	// HostPort is the gRPC address of the Temporal frontend. Example:
	//   temporal:7233
	HostPort string `env:"TEMPORAL_HOSTPORT"`

	// Namespace scopes everything: workflow IDs, task queues, schedules,
	// history. Every found-footy workflow lives in this namespace. Prod
	// and dev use different namespaces to keep data separate.
	Namespace string `env:"TEMPORAL_NAMESPACE" envDefault:"default"`

	// TaskQueue is the single queue all workflows + activities dispatch
	// through. Matches the Python-era value so migration + coexistence
	// works.
	TaskQueue string `env:"TEMPORAL_TASK_QUEUE" envDefault:"found-footy"`

	// ConnectTimeout bounds the initial dial + health check.
	ConnectTimeout time.Duration `env:"TEMPORAL_CONNECT_TIMEOUT" envDefault:"10s"`

	// WorkerShutdownTimeout is the grace period Worker.Stop waits for
	// in-flight activities to drain before force-terminating them.
	// Longer than the bootstrap Closer's 10s default because activity
	// drain can genuinely take a while (long-running video-download
	// activities). Bootstrap's closer for the worker uses a bounded
	// context based on this value.
	WorkerShutdownTimeout time.Duration `env:"TEMPORAL_WORKER_SHUTDOWN_TIMEOUT" envDefault:"60s"`

	// MaxConcurrentActivities caps in-flight activity executions on the
	// worker. The zero-value worker.Options defaults to ~1000, which on
	// this memory-budgeted host risks OOM when many video-download/ffmpeg
	// activities run at once — especially across simultaneous matches
	// (audit-2026-08-05 Tier-2 #8). Mirrors Python's
	// max_concurrent_activities=30.
	MaxConcurrentActivities int `env:"TEMPORAL_MAX_CONCURRENT_ACTIVITIES" envDefault:"30"`

	// MaxConcurrentWorkflowTasks caps in-flight workflow-task executions.
	// Mirrors Python's max_concurrent_workflow_tasks=10.
	MaxConcurrentWorkflowTasks int `env:"TEMPORAL_MAX_CONCURRENT_WORKFLOW_TASKS" envDefault:"10"`
}
