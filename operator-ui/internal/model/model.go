// Package model defines the operator UI's read-only JSON contract.
package model

import (
	"encoding/json"
	"time"
)

// EventList is one paginated operator result.
type EventList struct {
	Events []EventSummary `json:"events"`
	Total  int            `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

// EventSummary supplies enough fixture and media state for the event browser.
type EventSummary struct {
	ID             string    `json:"id"`
	FixtureID      int64     `json:"fixture_id"`
	EventType      string    `json:"event_type"`
	Detail         string    `json:"detail"`
	PlayerName     string    `json:"player_name"`
	TeamName       string    `json:"team_name"`
	Minute         int       `json:"minute"`
	Extra          int       `json:"extra"`
	FirstSeenAt    time.Time `json:"first_seen_at"`
	Removed        bool      `json:"removed"`
	FixtureState   string    `json:"fixture_state"`
	Kickoff        time.Time `json:"kickoff"`
	HomeTeam       string    `json:"home_team"`
	AwayTeam       string    `json:"away_team"`
	LeagueName     string    `json:"league_name"`
	AssetCount     int       `json:"asset_count"`
	PlayableAssets int       `json:"playable_assets"`
	RootCount      int       `json:"root_count"`
	CandidateCount int       `json:"candidate_count"`
}

// EventDetail is the complete retained evidence for one event.
type EventDetail struct {
	Event            EventSummary      `json:"event"`
	Fixture          Fixture           `json:"fixture"`
	Assets           []Asset           `json:"assets"`
	Candidates       []Candidate       `json:"candidates"`
	SelectionCommits []SelectionCommit `json:"selection_commits"`
}

// Fixture contains operator-facing provider and lifecycle context.
type Fixture struct {
	ID            int64      `json:"id"`
	State         string     `json:"state"`
	StatusShort   string     `json:"status_short"`
	StatusLong    string     `json:"status_long"`
	Elapsed       int        `json:"elapsed"`
	Extra         int        `json:"extra"`
	Kickoff       time.Time  `json:"kickoff"`
	HomeTeam      string     `json:"home_team"`
	AwayTeam      string     `json:"away_team"`
	HomeScore     int        `json:"home_score"`
	AwayScore     int        `json:"away_score"`
	LeagueName    string     `json:"league_name"`
	LeagueCountry string     `json:"league_country"`
	LeagueRound   string     `json:"league_round"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	LastPolledAt  *time.Time `json:"last_polled_at,omitempty"`
}

// Asset describes one accepted, event-scoped MD5 variant.
type Asset struct {
	ID                 string          `json:"id"`
	MD5                string          `json:"md5"`
	HashVersion        string          `json:"hash_version"`
	FrameCount         int             `json:"frame_count"`
	Width              int             `json:"width"`
	Height             int             `json:"height"`
	DurationMS         int             `json:"duration_ms"`
	FileSizeBytes      int64           `json:"file_size_bytes"`
	Bitrate            int             `json:"bitrate"`
	FrameRate          float64         `json:"frame_rate"`
	Popularity         int             `json:"direct_support"`
	ExactObservations  int             `json:"exact_observations"`
	RoutedObservations int             `json:"routed_observations"`
	SupersededBy       string          `json:"superseded_by,omitempty"`
	FirstSeenAt        time.Time       `json:"first_seen_at"`
	ReclaimedAt        *time.Time      `json:"reclaimed_at,omitempty"`
	ShareID            string          `json:"share_id,omitempty"`
	ShareState         string          `json:"share_state,omitempty"`
	TimestampVerified  bool            `json:"timestamp_verified"`
	ExtractedMinute    int             `json:"extracted_minute"`
	Validation         json.RawMessage `json:"validation,omitempty"`
	Playable           bool            `json:"playable"`
	PublicRoot         bool            `json:"public_root"`
}

// Candidate records one searched tweet and its terminal result.
type Candidate struct {
	ID              string     `json:"id"`
	SearchAttempt   int        `json:"search_attempt"`
	TweetURL        string     `json:"tweet_url"`
	TweetText       string     `json:"tweet_text"`
	Username        string     `json:"username"`
	DiscoveredAt    time.Time  `json:"discovered_at"`
	OutcomeClass    string     `json:"outcome_class"`
	RejectReason    string     `json:"reject_reason,omitempty"`
	OutcomeAt       *time.Time `json:"outcome_at,omitempty"`
	ObservedAssetID string     `json:"observed_asset_id,omitempty"`
	CreditedAssetID string     `json:"credited_asset_id,omitempty"`
}

// SelectionCommit exposes the latest immutable graph decisions without decoding them.
type SelectionCommit struct {
	ID          string          `json:"id"`
	CommittedAt time.Time       `json:"committed_at"`
	Policy      json.RawMessage `json:"policy"`
	Result      json.RawMessage `json:"result"`
}

// MediaObject resolves an asset ID to one retained object.
type MediaObject struct {
	Bucket      string
	Key         string
	ReclaimedAt *time.Time
}

// ListOptions bounds and filters the event browser query.
type ListOptions struct {
	Query          string
	EventType      string
	OnlyWithAssets bool
	Limit          int
	Offset         int
}
