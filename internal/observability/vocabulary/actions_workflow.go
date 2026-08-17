// Workflow timing Action enum values + init-time registration.
package vocabulary

const (
	ActionEventLifecycleMeasured Action = "event_lifecycle_measured"
	ActionEventSearchMeasured    Action = "event_search_measured"
	ActionEventCandidateMeasured Action = "event_candidate_measured"
	ActionEventPublishMeasured   Action = "event_publish_measured"
)

func init() {
	registerActions(
		ActionEventLifecycleMeasured,
		ActionEventSearchMeasured,
		ActionEventCandidateMeasured,
		ActionEventPublishMeasured,
	)
}
