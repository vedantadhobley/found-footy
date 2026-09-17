// collector_types.go defines bounded durable evidence independently of database
// or workflow APIs. Guarded writers evaluate its policy under lock in both modes.
package providerintegrity

import (
	"context"
	"fmt"
	"time"
)

const (
	// HealthEvidenceVersion prevents silently decoding a different evidence shape.
	HealthEvidenceVersion = 1
	// HealthReceiptRetention bounds poll history, not unresolved incident lifetime.
	HealthReceiptRetention = 14 * 24 * time.Hour
	maxRecoveryProbes      = 32
	// MaxHealthFixtures bounds one internal observation payload.
	MaxHealthFixtures = 1024
)

// Availability distinguishes provider errors from missing local assessment.
type Availability string

const (
	AvailabilityUsable     Availability = "usable"
	AvailabilityTransport  Availability = "transport"
	AvailabilityContract   Availability = "contract"
	AvailabilityUnobserved Availability = "unobserved"
	CauseUnavailable       HealthCause  = "unavailable"
)

// HealthObservation contains facts before reconciliation, or explicit failure
// coverage. Interrupted marks a failed fetch attempt before this poll succeeded.
type HealthObservation struct {
	FixtureID          int64
	Availability       Availability
	FailureReason      string
	Interrupted        bool
	BaselineLastPolled *time.Time
	Comparison         FixtureComparison
}

// HealthPoll identifies a logical scheduled execution, independent of retries.
type HealthPoll struct {
	ID           string
	At           time.Time
	Observations []HealthObservation
}

// Validate rejects malformed or unbounded internal receipts before persistence.
func (p HealthPoll) Validate() error {
	if p.ID == "" || len(p.ID) > 200 || p.At.IsZero() || len(p.Observations) == 0 || len(p.Observations) > MaxHealthFixtures {
		return fmt.Errorf("invalid provider health poll identity or coverage")
	}
	seen := make(map[int64]bool, len(p.Observations))
	for _, o := range p.Observations {
		if o.FixtureID <= 0 || seen[o.FixtureID] || len(o.FailureReason) > 100 {
			return fmt.Errorf("invalid provider health fixture coverage")
		}
		seen[o.FixtureID] = true
		switch o.Availability {
		case AvailabilityUsable:
			if o.Comparison.Stored.FixtureID != o.FixtureID || o.Comparison.Observed.FixtureID != o.FixtureID ||
				len(o.Comparison.StoredEvents) > 256 || len(o.Comparison.ConfirmedEvents) > 256 ||
				len(o.Comparison.ObservedEvents) > 256 {
				return fmt.Errorf("invalid provider health fixture facts")
			}
		case AvailabilityTransport, AvailabilityContract, AvailabilityUnobserved:
		default:
			return fmt.Errorf("invalid provider availability %q", o.Availability)
		}
	}
	return nil
}

// FetchFailureEvidence retains failed attempts even if the same activity retries
// successfully. It deliberately contains no request bodies, URLs, or secrets.
type FetchFailureEvidence struct {
	PollID     string
	ObservedAt time.Time
	Attempt    int
	Failures   []HealthObservation
}

// HealthIncident retains a causal baseline, not the latest corrupted snapshot.
// Integrity causes use their Reason value as Cause; clock causes use probes.
type HealthIncident struct {
	Cause           HealthCause
	StartedAt       time.Time
	LastCredibleAt  time.Time
	Policy          MutationPolicy
	Baseline        FixtureFacts
	StoredEvents    []EventFact          `json:",omitempty"`
	ConfirmedEvents []EventFact          `json:",omitempty"`
	Probes          []FixtureHealthProbe `json:",omitempty"`
}

// FixtureHealthEvidence is the bounded, versioned incident payload. Progress
// cursors live in typed SQL columns rather than a second fixture snapshot.
type FixtureHealthEvidence struct {
	Version       int
	Availability  Availability
	FailureReason string
	Freshness     Freshness
	Incidents     []HealthIncident
}

// FixtureHealth keeps observation coverage separate from clock advancement.
type FixtureHealth struct {
	FixtureID       int64
	LastPollID      string
	LastObservedAt  time.Time
	LastUsableAt    time.Time
	ContinuousSince time.Time
	ClockProgressAt time.Time
	PhaseSince      time.Time
	ObservedStatus  string
	ClockStatus     string
	ClockElapsed    *int
	ClockExtra      *int
	Evidence        FixtureHealthEvidence
}

// ProviderHealthEvidence stores only recent correlation and recovery evidence.
type ProviderHealthEvidence struct {
	Version    int
	Recent     []HealthEpisode
	IncidentAt time.Time
	Probes     []ProviderHealthProbe
}

// ProviderHealth is the single API-Football assessment cursor. Generation changes
// only when the enforced provider policy changes, not on every successful poll.
type ProviderHealth struct {
	LastObservedAt time.Time
	Generation     int64
	Policy         MutationPolicy
	Evidence       ProviderHealthEvidence
}

// HealthTransition is an internal diagnostic; it must never enter event_log.
// FixtureID zero denotes the provider-wide integrity cause.
type HealthTransition struct {
	FixtureID        int64
	Cause            HealthCause
	StartedAt        time.Time
	Resolved         bool
	LastCredibleAt   time.Time     `json:",omitempty"`
	Availability     Availability  `json:",omitempty"`
	MissingEventKeys []string      `json:",omitempty"`
	Baseline         *FixtureFacts `json:",omitempty"`
	Observed         *FixtureFacts `json:",omitempty"`
	FailureReason    string        `json:",omitempty"`
}

// HealthResult is the compact retry receipt and workflow diagnostic. It does
// not contain per-poll provider payloads or grant permission to mutate data.
type HealthResult struct {
	ObservedAt     time.Time
	Generation     int64
	ProviderPolicy MutationPolicy
	Requested      int
	Usable         int
	FailedAttempts int
	UnavailableIDs []int64
	RestrictedIDs  []int64
	StalledIDs     []int64
	BoundaryIDs    []int64
	Correlation    HealthCorrelation
	Transitions    []HealthTransition
	Ignored        bool
}

// HealthStore owns atomic receipt, evidence, and transition persistence.
type HealthStore interface {
	RecordFetchFailure(context.Context, FetchFailureEvidence) error
	Observe(context.Context, HealthPoll) (HealthResult, error)
}
