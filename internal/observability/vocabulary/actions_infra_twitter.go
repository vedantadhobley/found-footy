// Twitter (internal Firefox+Playwright service) adapter Action enum values + init-time registration.
package vocabulary

const (
	ActionTwitterSearch       Action = "twitter_search"        // /search succeeded
	ActionTwitterSearchFailed Action = "twitter_search_failed" // /search returned an error
	ActionTwitterVerify       Action = "twitter_verify"        // forced auth verify + cookie persistence succeeded
	ActionTwitterVerifyFailed Action = "twitter_verify_failed" // forced auth verify or persistence failed
)

func init() {
	registerActions(
		ActionTwitterSearch,
		ActionTwitterSearchFailed,
		ActionTwitterVerify,
		ActionTwitterVerifyFailed,
	)
}
