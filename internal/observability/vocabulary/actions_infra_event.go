// Event-composer Action enum values + init-time registration.
// The composer writes the durable Postgres event log. Live NATS fan-out has
// its own publisher and vocabulary.
package vocabulary

const (
	ActionEventPublish       Action = "event_publish"
	ActionEventPublishFailed Action = "event_publish_failed"
)

func init() {
	registerActions(
		ActionEventPublish,
		ActionEventPublishFailed,
	)
}
