// TemporalConfig — env-driven settings for the Temporal workflow-orchestration adapter.
package config

import "time"

// TemporalConfig covers the Temporal client + worker adapter. Every
// binary that starts or executes workflows needs this.
//
// HostPort is not tagged required at the env layer because Phase S1/S2
// binaries don't need Temporal yet; the temporal package's constructor
// returns a descriptive error when a binary that DOES need it starts
// without the env var set.
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
}
