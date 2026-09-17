// provider_mutation_evidence.go records policy-limited plan operations without
// executing an alternate transaction or claiming hypothetical removal counts.
package pg

import "github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"

// restrictedActiveOperations records the scope of the recommendation, while
// the receipt's ordinary effect fields retain what actually committed.
func restrictedActiveOperations(plan providerintegrity.ActiveMutationPlan, policy providerintegrity.MutationPolicy) *providerintegrity.RestrictedOperations {
	if policy == providerintegrity.PolicyTrusted {
		return nil
	}
	out := &providerintegrity.RestrictedOperations{FixtureRefreshLimited: true}
	if policy == providerintegrity.PolicyRejected {
		for _, op := range plan.Inserts {
			if op.Event != nil {
				out.EventInserts = append(out.EventInserts, op.Event.ID)
			}
		}
		for _, op := range plan.Presences {
			out.PresenceVotes = append(out.PresenceVotes, op.EventID)
		}
	}
	for _, op := range plan.Updates {
		out.EventCorrectionsLimited = append(out.EventCorrectionsLimited, op.EventID)
	}
	for _, op := range plan.Absences {
		out.AbsenceVotes = append(out.AbsenceVotes, op.EventID)
	}
	out.PlaceholderDeletes = plan.UnknownDeletes
	// Only flag a completion check once the proposed terminal grace has elapsed.
	// Pending debounce/downstream rows may still prevent actual completion.
	f, completion := plan.Fixture, plan.Completion
	out.CompletionCheck = f.APIStatus.Terminal() && f.TerminalObservedAt != nil &&
		!completion.TerminalBefore.IsZero() && !completion.CompletedAt.IsZero() && completion.GraceSeconds > 0 &&
		!f.TerminalObservedAt.After(completion.TerminalBefore)
	return out
}

// restrictedFixtureOperations covers passive/ingest plans, including rejected
// initial inserts. positive_only leaves fresh historical inserts unchanged.
func restrictedFixtureOperations(plan providerintegrity.FixtureMutationPlan, policy providerintegrity.MutationPolicy) *providerintegrity.RestrictedOperations {
	if policy == providerintegrity.PolicyTrusted || (policy == providerintegrity.PolicyPositiveOnly && !plan.BaselineExists) {
		return nil
	}
	return &providerintegrity.RestrictedOperations{FixtureRefreshLimited: true}
}
