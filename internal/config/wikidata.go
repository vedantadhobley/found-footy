// WikidataConfig — env-driven settings for the Wikidata adapter (SPARQL
// + wbsearchentities + Special:EntityData). Public endpoints; no auth
// but Wikidata policy requires a meaningful User-Agent.
package config

import "time"

// WikidataConfig covers all three Wikidata endpoints used by the
// alias-lookup pipeline.
type WikidataConfig struct {
	// Endpoint is the SPARQL endpoint URL. Default is the public one.
	Endpoint string `env:"WIKIDATA_ENDPOINT" envDefault:"https://query.wikidata.org/sparql"`

	// WWWHost is the base URL for the MediaWiki + Special:EntityData
	// endpoints (`/w/api.php`, `/wiki/Special:EntityData/...`). Prod
	// default is www.wikidata.org. Tests set this to their httptest
	// server URL so mock handlers can serve those paths.
	WWWHost string `env:"WIKIDATA_WWW_HOST" envDefault:"https://www.wikidata.org"`

	// UserAgent must identify the caller per Wikidata's policy. Include
	// a project name + contact URL. Sending default Go UA gets you
	// rate-limited or 403'd.
	UserAgent string `env:"WIKIDATA_USER_AGENT" envDefault:"found-footy/dev (https://github.com/vedantadhobley/found-footy)"`

	// Timeout bounds an individual HTTP request. Wikidata SPARQL can
	// take 5-15s for complex team lookups; wbsearchentities and
	// Special:EntityData are usually <500ms.
	Timeout time.Duration `env:"WIKIDATA_TIMEOUT" envDefault:"30s"`
}
