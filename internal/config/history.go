// HistoryConfig defines the shared public-history and media-expiry window.
package config

// HistoryConfig is consumed by both the API and worker so presentation and
// media retention cannot drift onto different cutoffs.
type HistoryConfig struct {
	// CompletedFixtureDates is the minimum number of distinct UTC kickoff dates
	// containing completed fixtures kept in the unfiltered public snapshot.
	// Media for older completed fixture dates becomes eligible for reclamation;
	// SQL audit rows remain durable.
	CompletedFixtureDates int `env:"PUBLIC_HISTORY_COMPLETED_FIXTURE_DATES" envDefault:"14"`
}
