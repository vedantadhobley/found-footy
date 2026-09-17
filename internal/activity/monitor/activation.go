// Staging-fixture polling and kickoff-driven activation activities.
package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/vedantadhobley/found-footy/internal/contract/auditlog"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// ActivateUpcomingInput carries the lookahead window (typically 30
// min). Fixtures in staging with kickoff <= now+lookahead get promoted
// to active so the batched poll starts covering them next cycle.
type ActivateUpcomingInput struct {
	Lookahead time.Duration
}

// ActivateUpcomingOutput reports how many fixtures got promoted +
// how many were considered (for observability).
type ActivateUpcomingOutput struct {
	Considered int
	Activated  int
	Errors     []string
}

// ActivateUpcoming reads staging fixtures with kickoff before
// now+Lookahead, promotes any that ShouldActivateNow returns true for.
// Uses the same domain predicate ingest uses at first-sight
// activation, so the rule stays in one place.
func (a *Activities) ActivateUpcoming(ctx context.Context, in ActivateUpcomingInput) (ActivateUpcomingOutput, error) {
	out := ActivateUpcomingOutput{}
	now := a.now()

	candidates, err := a.FixtureRepo.ListStagingBeforeKickoff(ctx, now.Add(in.Lookahead))
	if err != nil {
		return out, fmt.Errorf("monitor.ActivateUpcoming: list: %w", err)
	}
	out.Considered = len(candidates)

	for _, f := range candidates {
		if !f.ShouldActivateNow(now, in.Lookahead) {
			continue
		}
		if err := f.Activate(now); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("activate fixture=%d: %v", f.ID, err))
			continue
		}
		audit, err := fixtureActivatedAudit(f.ID, now, "kickoff_soon")
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("build activation audit fixture=%d: %v", f.ID, err))
			continue
		}
		store, err := a.fixtureAuditStore()
		if err != nil {
			return out, err
		}
		transitioned, err := store.TransitionWithAudit(ctx, f, audit)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("activate fixture=%d: %v", f.ID, err))
			continue
		}
		if transitioned {
			out.Activated++
		}
	}
	return out, nil
}

// ── PollStagingFixtures ───────────────────────────────────────

// PollStagingFixturesInput carries the ActivationWindow the workflow
// resolved for this cycle. Passed through PollStagingFixtures'
// kickoff-correction check.
type PollStagingFixturesInput struct {
	// ActivationWindow — same value ActivePollWorkflow's ActivateUpcoming
	// uses. A staging fixture whose kickoff was corrected into this
	// window by the vendor gets activated the same tick even if the
	// API status is still NS.
	ActivationWindow  time.Duration
	ObservationID     string
	ObservedAt        time.Time
	LegacyUnprotected bool
}

// PollStagingFixturesOutput reports what happened this tick.
// Considered is the raw count of staging fixtures polled; Polled is
// how many the API returned data for; MissedIDs counts vendor-side
// chunk failures; EmergencyActivated + KickoffActivated separate the
// two activation paths so operators can see which triggered.
type PollStagingFixturesOutput struct {
	Considered         int
	Polled             int
	MissedIDs          int
	EmergencyActivated int // API status flipped to Live() while still staging
	KickoffActivated   int // vendor-corrected kickoff pulled fixture into activation window
	Errors             []string
}

// PollStagingFixtures fires a batched /fixtures?ids= call against
// EVERY staging fixture (no bucket filter — StagingPollWorkflow's
// Temporal Schedule decides when to fire, not us), then reconciles
// each response. Two activation paths:
//
//   - Live() emergency: the API says the match is already playing
//     (or paused mid-play) while we still have it in staging. Ingest
//     had the wrong kickoff, or the vendor published a corrected one,
//     or the match started early. Activate immediately.
//   - Kickoff-corrected: the vendor pushed a new kickoff time that
//     brings the fixture inside our activation window. Same fix as
//     ActivePollWorkflow's ActivateUpcoming DB-only check but
//     works on API-side mutations ingest hasn't caught yet.
//
// If neither triggers, RecordStagingPoll updates APIStatus + Kickoff
// + last_polled_at without touching last_activity_at (matches Python
// — passive polls don't count as activity for frontend sort).
//
// Called by StagingPollWorkflow on its cron cadence (default 15 min).
// Cadence tunable at runtime via `temporal schedule update
// staging-poll-scheduled --cron ...` — this activity doesn't care
// about cadence, only about doing one full poll pass per invocation.
func (a *Activities) PollStagingFixtures(ctx context.Context, in PollStagingFixturesInput) (PollStagingFixturesOutput, error) {
	out := PollStagingFixturesOutput{}
	now := a.now()
	observedAt := in.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = now
	}

	candidates, err := a.FixtureRepo.ListByState(ctx, fixture.StateStaging)
	if err != nil {
		return out, fmt.Errorf("monitor.PollStagingFixtures: list: %w", err)
	}
	out.Considered = len(candidates)
	if len(candidates) == 0 {
		return out, nil
	}

	ids := make([]int64, len(candidates))
	for i, f := range candidates {
		ids[i] = f.ID
	}
	result, err := a.APIFootball.ListFixturesByIDs(ctx, ids)
	failedIDs := result.FailedIDs()
	if err != nil {
		// Catastrophic (all chunks failed / ctx cancelled) — surface the
		// error but keep the FailedIDs count so the workflow can log it.
		out.MissedIDs = len(failedIDs)
		return out, fmt.Errorf("monitor.PollStagingFixtures: fetch: %w", err)
	}
	out.MissedIDs = len(failedIDs)

	apiByID := make(map[int64]apifootball.APIFixture, len(result.Fixtures))
	for _, af := range result.Fixtures {
		apiByID[af.Fixture.ID] = af
	}

	for _, f := range candidates {
		af, ok := apiByID[f.ID]
		if !ok {
			// Missed this chunk — next tick picks it up naturally.
			continue
		}
		baseline := *f
		newStatus := fixture.APIStatus{
			Short: af.Fixture.Status.Short,
			Long:  af.Fixture.Status.Long,
		}
		newKickoff := af.Fixture.Date
		updateFixtureMetadataFromAPI(f, af)

		activationReason := ""
		switch {
		case newStatus.Live():
			// Emergency — API says the match is already live.
			// Refresh the fields Activate doesn't touch (kickoff, status)
			// FIRST via RecordStagingPoll, then transition.
			f.RecordStagingPoll(newStatus, newKickoff, observedAt)
			if err := f.Activate(now); err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("emergency activate fixture=%d: %v", f.ID, err))
				continue
			}
			activationReason = "already_started"

		default:
			// Non-Live status. Update the fields, then check if the
			// (possibly-corrected) kickoff pulls us into the activation
			// window.
			f.RecordStagingPoll(newStatus, newKickoff, observedAt)
			if f.ShouldActivateNow(now, in.ActivationWindow) {
				if err := f.Activate(now); err != nil {
					out.Errors = append(out.Errors, fmt.Sprintf("kickoff activate fixture=%d: %v", f.ID, err))
					continue
				}
				activationReason = "kickoff_correction"
			}
		}

		var activationAudit *auditlog.Record
		if activationReason != "" {
			audit, err := fixtureActivatedAudit(f.ID, now, activationReason)
			if err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("build activation audit fixture=%d: %v", f.ID, err))
				continue
			}
			activationAudit = &audit
		}

		applied := false
		if a.ProviderMutations != nil && !in.LegacyUnprotected {
			if in.ObservationID == "" {
				return out, fmt.Errorf("monitor.PollStagingFixtures: guarded observation ID is required")
			}
			local := providerintegrity.AssessFixture(providerintegrity.FixtureComparison{
				Stored: providerFixtureFacts(&baseline), Observed: providerFixtureFacts(f),
			}).Policy
			result, applyErr := a.ProviderMutations.ApplyFixture(ctx, providerintegrity.FixtureMutationPlan{
				Guard: providerintegrity.ObservationGuard{ObservationID: in.ObservationID, ObservedAt: observedAt},
				Mode:  providerintegrity.MutationModeStaging, Fixture: f,
				BaselineExists: true, BaselineLastPolled: cloneOptionalTime(baseline.LastPolledAt),
				LocalPolicy: local, Activate: activationReason != "", ActivationAudit: activationAudit,
			})
			err = applyErr
			applied = applyErr == nil && result.Applied && !result.Stale
		} else if activationReason != "" {
			store, storeErr := a.fixtureAuditStore()
			if storeErr != nil {
				return out, storeErr
			}
			applied, err = store.TransitionWithAudit(ctx, f, *activationAudit)
		} else {
			applied, err = a.FixtureRepo.RefreshStagingPoll(ctx, f)
		}
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("persist fixture=%d: %v", f.ID, err))
			continue
		}
		if !applied {
			continue
		}
		switch activationReason {
		case "already_started":
			out.EmergencyActivated++
		case "kickoff_correction":
			out.KickoffActivated++
		}
		out.Polled++
	}
	return out, nil
}
