// Wikipedia-adapter Action enum values + init-time registration.
//
// One endpoint family (CirrusSearch full-text via `list=search` +
// `generator=search`) — a "search" event pair mirrors the wikidata
// adapter's shape.
package vocabulary

const (
	ActionWikipediaSearch       Action = "wikipedia_search"
	ActionWikipediaSearchFailed Action = "wikipedia_search_failed"
)

func init() {
	registerActions(
		ActionWikipediaSearch,
		ActionWikipediaSearchFailed,
	)
}
