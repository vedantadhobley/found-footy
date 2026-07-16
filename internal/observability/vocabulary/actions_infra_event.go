// Event-composer Action enum values + init-time registration.
// The composer dual-writes to (pg event_log, NATS). Actions cover the
// three outcomes: full success, pg-write failure (fatal, nothing
// published), and skew (pg-write succeeded but NATS publish failed —
// the outbox catch-up worker will republish gaps on the next tick).
package vocabulary

const (
	ActionEventPublish       Action = "event_publish"        // dual-write succeeded end-to-end
	ActionEventPublishFailed Action = "event_publish_failed" // pg INSERT failed — fatal, nothing was published
	ActionEventPublishSkew   Action = "event_publish_skew"   // pg INSERT ok, NATS publish failed — outbox worker closes gap
)

func init() {
	registerActions(
		ActionEventPublish,
		ActionEventPublishFailed,
		ActionEventPublishSkew,
	)
}
