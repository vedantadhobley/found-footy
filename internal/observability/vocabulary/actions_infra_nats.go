package vocabulary

// NATS-adapter actions. Cover the connection lifecycle + publish/
// subscribe operations. Publish-level actions get emitted at DEBUG by
// default; connection lifecycle at INFO; disconnect/reconnect at WARN
// because they indicate real degradation.
const (
	ActionNATSConnected      Action = "nats_connected"       // initial connect succeeded
	ActionNATSConnectFailed  Action = "nats_connect_failed"  // Connect() returned an error
	ActionNATSClosed         Action = "nats_closed"          // Conn.Close() invoked
	ActionNATSDisconnected   Action = "nats_disconnected"    // server dropped us; reconnect logic engaged
	ActionNATSReconnected    Action = "nats_reconnected"     // reconnected to (possibly different) server
	ActionNATSPublish        Action = "nats_publish"         // publish returned nil
	ActionNATSPublishFailed  Action = "nats_publish_failed"  // publish returned an error
	ActionNATSSubscribe      Action = "nats_subscribe"       // Subscribe registered ok
	ActionNATSSubscribeFail  Action = "nats_subscribe_fail"  // Subscribe returned an error
	ActionNATSAsyncError     Action = "nats_async_error"     // async error handler fired (server-side failure)
)

func init() {
	registerActions(
		ActionNATSConnected,
		ActionNATSConnectFailed,
		ActionNATSClosed,
		ActionNATSDisconnected,
		ActionNATSReconnected,
		ActionNATSPublish,
		ActionNATSPublishFailed,
		ActionNATSSubscribe,
		ActionNATSSubscribeFail,
		ActionNATSAsyncError,
	)
}
