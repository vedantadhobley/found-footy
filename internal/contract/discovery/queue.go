// Vision queue naming is shared by deterministic workflows and worker bootstrap.
package discovery

// VisionTaskQueue isolates validation without adding an environment-specific
// setting. The workflow's recorded base queue and bootstrap's configured base
// queue must derive the same durable activity destination.
func VisionTaskQueue(base string) string { return base + "-vision" }
