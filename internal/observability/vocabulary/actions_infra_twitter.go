// Twitter (internal Firefox+Playwright service) adapter Action enum values + init-time registration.
package vocabulary

const (
	ActionTwitterSearch       Action = "twitter_search"        // /search succeeded
	ActionTwitterSearchFailed Action = "twitter_search_failed" // /search returned an error
)

func init() {
	registerActions(
		ActionTwitterSearch,
		ActionTwitterSearchFailed,
	)
}
