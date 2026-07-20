// Wikidata-adapter Action enum values + init-time registration.
package vocabulary

const (
	ActionWikidataQuery         Action = "wikidata_query"
	ActionWikidataQueryFailed   Action = "wikidata_query_failed"
	ActionWikidataSearch        Action = "wikidata_search"
	ActionWikidataSearchFailed  Action = "wikidata_search_failed"
	ActionWikidataEntityFetch   Action = "wikidata_entity_fetch"
	ActionWikidataEntityFailed  Action = "wikidata_entity_failed"
)

func init() {
	registerActions(
		ActionWikidataQuery,
		ActionWikidataQueryFailed,
		ActionWikidataSearch,
		ActionWikidataSearchFailed,
		ActionWikidataEntityFetch,
		ActionWikidataEntityFailed,
	)
}
