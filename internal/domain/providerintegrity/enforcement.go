// enforcement.go separates health recommendations from rollout permissions.
package providerintegrity

import "github.com/google/uuid"

// EnforcementMode controls semantic restrictions, never transactional safety.
type EnforcementMode string

const (
	EnforcementShadow  EnforcementMode = "shadow"
	EnforcementEnforce EnforcementMode = "enforce"
)

// Valid rejects misspellings rather than silently enabling or bypassing policy.
func (m EnforcementMode) Valid() bool {
	return m == EnforcementShadow || m == EnforcementEnforce
}

// MutationDecision preserves the recommendation and rollout mode at commit.
// MutationResult.Policy remains the applied policy. A retry returns both from
// the original receipt even when the worker's configured mode has changed.
type MutationDecision struct {
	Writer            MutationMode
	Mode              EnforcementMode
	RecommendedPolicy MutationPolicy
	ProviderPolicy    MutationPolicy
	FixturePolicy     MutationPolicy
	LocalPolicy       MutationPolicy
	Restricted        *RestrictedOperations `json:",omitempty"`
}

// RestrictedOperations describes planned operations limited by the recommended
// policy, not a simulated alternate transaction. Event corrections can still
// fill missing assists under positive_only; an absence vote is not necessarily
// a removal. CompletionCheck means eligibility would not be evaluated, not that
// the fixture would otherwise complete. Actual effects remain in MutationResult.
type RestrictedOperations struct {
	FixtureRefreshLimited   bool
	EventInserts            []uuid.UUID `json:",omitempty"`
	PresenceVotes           []uuid.UUID `json:",omitempty"`
	EventCorrectionsLimited []uuid.UUID `json:",omitempty"`
	AbsenceVotes            []uuid.UUID `json:",omitempty"`
	PlaceholderDeletes      []uuid.UUID `json:",omitempty"`
	CompletionCheck         bool
}

// DecideMutation composes all recommendation scopes independently of rollout.
// Callers must validate mode before opening a write transaction.
func DecideMutation(mode EnforcementMode, provider, fixture, local MutationPolicy) (MutationDecision, MutationPolicy) {
	d := MutationDecision{Mode: mode, RecommendedPolicy: RestrictPolicies(provider, fixture, local),
		ProviderPolicy: provider, FixturePolicy: fixture, LocalPolicy: local}
	if mode == EnforcementShadow {
		return d, PolicyTrusted
	}
	return d, d.RecommendedPolicy
}
