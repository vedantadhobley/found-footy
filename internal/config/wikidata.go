// WikidataConfig — env-driven settings for the Wikidata SPARQL adapter.
package config

import "time"

// WikidataConfig covers the public Wikidata SPARQL endpoint used by the
// RAG team-alias pipeline. Public, no auth, but the endpoint requires a
// meaningful User-Agent header per their policy.
type WikidataConfig struct {
	// Endpoint is the SPARQL endpoint URL. Default is the public one.
	Endpoint string `env:"WIKIDATA_ENDPOINT" envDefault:"https://query.wikidata.org/sparql"`

	// UserAgent must identify the caller per Wikidata's policy. Include
	// a project name + contact URL. Sending default Go UA gets you
	// rate-limited or 403'd.
	UserAgent string `env:"WIKIDATA_USER_AGENT" envDefault:"found-footy/dev (https://github.com/vedantadhobley/found-footy)"`

	// Timeout bounds an individual SPARQL query. Wikidata sometimes
	// takes 5-15s for complex team lookups.
	Timeout time.Duration `env:"WIKIDATA_TIMEOUT" envDefault:"30s"`
}
