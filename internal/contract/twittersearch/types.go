// Package twittersearch owns the stable wire contract between the browser
// service, its HTTP client, and discovery activities.
package twittersearch

// ResultState classifies whether a browser search produced a usable
// observation. Keep this set bounded: values are also Prometheus labels.
type ResultState string

const (
	ResultRendered       ResultState = "rendered"
	ResultExplicitEmpty  ResultState = "explicit_empty"
	ResultLogin          ResultState = "login"
	ResultUpstreamError  ResultState = "upstream_error"
	ResultUnknownTimeout ResultState = "unknown_timeout"
)

// Usable reports whether this state may consume one logical discovery search.
func (s ResultState) Usable() bool {
	return s == ResultRendered || s == ResultExplicitEmpty
}

// Known reports whether the state belongs to the bounded wire/metric enum.
func (s ResultState) Known() bool {
	switch s {
	case ResultRendered, ResultExplicitEmpty, ResultLogin, ResultUpstreamError, ResultUnknownTimeout:
		return true
	default:
		return false
	}
}

// SearchEvidence is the bounded, secret-free browser and upstream evidence
// retained for one search. It deliberately excludes response bodies, request
// headers, cookies, and authorization data.
type SearchEvidence struct {
	FinalURL        string `json:"final_url,omitempty"`
	PageTitle       string `json:"page_title,omitempty"`
	AppShell        bool   `json:"app_shell"`
	EmptyState      bool   `json:"empty_state"`
	ErrorState      bool   `json:"error_state"`
	TimelineSeen    bool   `json:"timeline_seen"`
	TimelineStatus  int    `json:"timeline_status,omitempty"`
	TimelineFailure string `json:"timeline_failure,omitempty"`
	RateLimitLimit  string `json:"rate_limit_limit,omitempty"`
	RateLimitRemain string `json:"rate_limit_remaining,omitempty"`
	RateLimitReset  string `json:"rate_limit_reset,omitempty"`
}

// VideoRef is one tweet carrying a video that passed browser extraction.
type VideoRef struct {
	TweetURL        string  `json:"tweet_url"`
	TweetText       string  `json:"tweet_text"`
	VideoPageURL    string  `json:"video_page_url"`
	DurationSeconds float64 `json:"duration_seconds"`
	Username        string  `json:"username,omitempty"`
	AgeMinutes      float64 `json:"age_minutes,omitempty"`
}

// SearchRequest is the JSON body of POST /search.
type SearchRequest struct {
	Query         string   `json:"query"`
	ExcludeURLs   []string `json:"exclude_urls,omitempty"`
	MaxAgeMinutes int      `json:"max_age_minutes,omitempty"`
}

// SearchResponse is returned for every browser page result, including an
// unavailable page. HTTP/transport failures still use SearchErrorBody.
type SearchResponse struct {
	Status          string         `json:"status"`
	ResultState     ResultState    `json:"result_state,omitempty"`
	Evidence        SearchEvidence `json:"evidence"`
	Videos          []VideoRef     `json:"videos"`
	Count           int            `json:"count"`
	Query           string         `json:"query,omitempty"`
	StopReason      string         `json:"stop_reason,omitempty"`
	Scrolls         int            `json:"scrolls,omitempty"`
	InitialArticles int            `json:"initial_articles,omitempty"`
	TweetsParsed    int            `json:"tweets_parsed,omitempty"`
	VideoTweets     int            `json:"video_tweets,omitempty"`
	Elapsed         string         `json:"elapsed,omitempty"`
}

// SearchErrorBody is the structured non-2xx response. ResultState is present
// when the service observed a classified page state such as a login redirect.
type SearchErrorBody struct {
	Status      string         `json:"status"`
	ErrorClass  string         `json:"error_class"`
	Message     string         `json:"message"`
	ReauthURL   string         `json:"reauth_url,omitempty"`
	ResultState ResultState    `json:"result_state,omitempty"`
	Evidence    SearchEvidence `json:"evidence"`
}
