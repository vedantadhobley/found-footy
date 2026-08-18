// Temporal-adapter Action enum values + init-time registration.
package vocabulary

// Temporal-adapter actions. Cover client lifecycle, workflow start, schedule,
// and worker lifecycle. Worker-lifecycle actions land in
// S5.2 with the Worker wrapper.
const (
	ActionTemporalConnected             Action = "temporal_connected"               // client dialed + health probe passed
	ActionTemporalConnectFailed         Action = "temporal_connect_failed"          // client dial or health probe failed
	ActionTemporalClosed                Action = "temporal_closed"                  // client.Close invoked
	ActionTemporalStartWorkflow         Action = "temporal_start_workflow"          // StartWorkflow succeeded
	ActionTemporalStartFailed           Action = "temporal_start_failed"            // StartWorkflow returned an error
	ActionTemporalWorkerStarted         Action = "temporal_worker_started"          // Worker.Start returned (background loop running)
	ActionTemporalWorkerStopped         Action = "temporal_worker_stopped"          // Worker.Stop returned (drain complete)
	ActionTemporalWorkerFailed          Action = "temporal_worker_failed"           // Worker.Start returned an error
	ActionTemporalScheduleCreated       Action = "temporal_schedule_created"        // Schedule.Create succeeded
	ActionTemporalScheduleAlreadyExists Action = "temporal_schedule_already_exists" // Create returned ErrScheduleAlreadyRunning; treated as success
	ActionTemporalScheduleFailed        Action = "temporal_schedule_failed"         // Schedule.Create returned an unexpected error
)

func init() {
	registerActions(
		ActionTemporalConnected,
		ActionTemporalConnectFailed,
		ActionTemporalClosed,
		ActionTemporalStartWorkflow,
		ActionTemporalStartFailed,
		ActionTemporalWorkerStarted,
		ActionTemporalWorkerStopped,
		ActionTemporalWorkerFailed,
		ActionTemporalScheduleCreated,
		ActionTemporalScheduleAlreadyExists,
		ActionTemporalScheduleFailed,
	)
}
