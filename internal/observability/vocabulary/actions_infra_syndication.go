// Syndication (Twitter guestpass) adapter Action enum values + init-time registration.
package vocabulary

const (
	ActionSyndicationFetch       Action = "syndication_fetch"
	ActionSyndicationFetchFailed Action = "syndication_fetch_failed"
)

func init() {
	registerActions(
		ActionSyndicationFetch,
		ActionSyndicationFetchFailed,
	)
}
