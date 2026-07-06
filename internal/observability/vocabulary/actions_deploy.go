package vocabulary

// Deploy / binary-lifecycle actions — the first actions to land because
// every binary emits at least the startup + shutdown pair. See
// docs/rebuild-plan.md §11 deploy tracking.
const (
	ActionStartup         Action = "startup"         // binary starting; carries git_sha + built_at
	ActionShutdown        Action = "shutdown"        // binary stopping cleanly
	ActionShutdownForced  Action = "shutdown_forced" // binary killed by second SIGINT/SIGTERM
	ActionConfigLoaded    Action = "config_loaded"   // config.Load succeeded
	ActionConfigLoadError Action = "config_load_error"
)

func init() {
	registerActions(
		ActionStartup,
		ActionShutdown,
		ActionShutdownForced,
		ActionConfigLoaded,
		ActionConfigLoadError,
	)
}
