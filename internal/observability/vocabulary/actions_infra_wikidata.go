// Wikidata-adapter Action enum values + init-time registration.
package vocabulary

const (
	ActionWikidataQuery       Action = "wikidata_query"
	ActionWikidataQueryFailed Action = "wikidata_query_failed"
)

func init() {
	registerActions(
		ActionWikidataQuery,
		ActionWikidataQueryFailed,
	)
}
