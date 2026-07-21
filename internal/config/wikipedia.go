// WikipediaConfig — env-driven settings for the Wikipedia adapter (used
// by the alias-lookup pipeline for entity resolution via full-text
// search). Public MediaWiki API; no auth but Wikimedia policy requires a
// meaningful User-Agent.
package config

import "time"

// WikipediaConfig covers the MediaWiki API endpoint used for
// entity resolution. Separate from WikidataConfig because Wikipedia is a
// distinct MediaWiki instance (`en.wikipedia.org` etc., not
// `www.wikidata.org`).
type WikipediaConfig struct {
	// Host is the base URL of the Wikipedia MediaWiki instance. Prod
	// default is `en.wikipedia.org`; tests set it to their httptest server URL.
	//
	// For a future multi-language fallback (Approach C step 5 in the
	// entity-resolution proposal), we'd add a per-language host builder;
	// for now, English-only is enough to fix the Nice-class miss.
	Host string `env:"WIKIPEDIA_HOST" envDefault:"https://en.wikipedia.org"`

	// UserAgent must identify the caller per Wikimedia policy. Include
	// a project name + contact URL. Sending default Go UA gets you
	// rate-limited or 403'd.
	UserAgent string `env:"WIKIPEDIA_USER_AGENT" envDefault:"found-footy/dev (https://github.com/vedantadhobley/found-footy)"`

	// Timeout bounds an individual HTTP request. CirrusSearch responds
	// in ~500ms typically; 15s covers cold-start + tail-latency headroom.
	Timeout time.Duration `env:"WIKIPEDIA_TIMEOUT" envDefault:"15s"`
}
