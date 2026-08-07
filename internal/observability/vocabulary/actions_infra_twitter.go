// Twitter (internal Firefox+Selenium service) adapter Action enum values + init-time registration.
package vocabulary

const (
	ActionTwitterConnected     Action = "twitter_connected"      // /health probe succeeded
	ActionTwitterConnectFailed Action = "twitter_connect_failed" // /health probe or client build failed
	ActionTwitterSearch        Action = "twitter_search"         // /search succeeded
	ActionTwitterSearchFailed  Action = "twitter_search_failed"  // /search returned an error
)

func init() {
	registerActions(
		ActionTwitterConnected,
		ActionTwitterConnectFailed,
		ActionTwitterSearch,
		ActionTwitterSearchFailed,
	)
}
