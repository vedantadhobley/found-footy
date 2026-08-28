// Durable audit emissions, downstream spawn, and API-event mapping helpers.
package monitor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/contract/auditlog"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

func (a *Activities) fixtureAuditStore() (auditedFixtureRepo, error) {
	repo, ok := a.FixtureRepo.(auditedFixtureRepo)
	if !ok {
		return nil, fmt.Errorf("monitor: fixture repository lacks atomic audit support")
	}
	return repo, nil
}

func (a *Activities) eventAuditStore() (auditedEventRepo, error) {
	repo, ok := a.EventRepo.(auditedEventRepo)
	if !ok {
		return nil, fmt.Errorf("monitor: event repository lacks atomic audit support")
	}
	return repo, nil
}

func eventDetectedAudit(evID uuid.UUID, fixtureID int64, e *event.Event) (auditlog.Record, error) {
	payload := auditlog.EventDetectedPayload{
		EventID:    evID,
		FixtureID:  fixtureID,
		EventType:  string(e.Type),
		Detail:     string(e.Detail),
		Minute:     e.Minute,
		Extra:      e.Extra,
		PlayerName: playerName(e.Player),
		TeamID:     int64(e.Team.ID),
		TeamName:   e.Team.Name,
		Counter:    1,
	}
	return auditlog.New(auditlog.KindEventDetected, evID, fixtureID, payload)
}

func eventStableAudit(evID uuid.UUID, fixtureID int64, e *event.Event) (auditlog.Record, error) {
	payload := auditlog.EventStablePayload{
		EventID:    evID,
		FixtureID:  fixtureID,
		EventType:  string(e.Type),
		Detail:     string(e.Detail),
		Minute:     e.Minute,
		Extra:      e.Extra,
		PlayerName: playerName(e.Player),
		TeamID:     int64(e.Team.ID),
		TeamName:   e.Team.Name,
	}
	return auditlog.New(auditlog.KindEventStable, evID, fixtureID, payload)
}

func fixtureActivatedAudit(fixtureID int64, activatedAt time.Time, reason string) (auditlog.Record, error) {
	payload := auditlog.FixtureActivatedPayload{
		FixtureID:   fixtureID,
		ActivatedAt: activatedAt,
		Reason:      reason,
	}
	return auditlog.New(auditlog.KindFixtureActivated, uuid.Nil, fixtureID, payload)
}

func eventRemovedAudit(evID uuid.UUID, fixtureID int64, removedAt time.Time) (auditlog.Record, error) {
	payload := auditlog.EventRemovedPayload{
		EventID:   evID,
		FixtureID: fixtureID,
		RemovedAt: removedAt,
		Reason:    "debounce_zero",
	}
	return auditlog.New(auditlog.KindEventRemoved, evID, fixtureID, payload)
}

func (a *Activities) fixtureCompletedAudit(
	f *fixture.Fixture,
	completedAt time.Time,
	assessment fixture.CompletionAssessment,
	providerParity *bool,
) (auditlog.Record, error) {
	if f.TerminalObservedAt == nil {
		return auditlog.Record{}, fmt.Errorf("fixture %d completed without terminal observation", f.ID)
	}
	payload := auditlog.FixtureCompletedPayload{
		FixtureID:                f.ID,
		TerminalObservedAt:       *f.TerminalObservedAt,
		CompletedAt:              completedAt,
		GraceSeconds:             int64(a.TerminalGracePeriod / time.Second),
		ProviderScoreEventParity: providerParity,
		DurableScoreEventParity:  assessment.DurableScoreEventParity,
		PenaltyResultDecided:     penaltyResultDecided(f),
	}
	return auditlog.New(auditlog.KindFixtureCompleted, uuid.Nil, f.ID, payload)
}

// penaltyResultDecided is nil outside PEN and otherwise records whether the
// current shootout score identifies a winner. It is completion audit evidence,
// not a permanent gate.
func penaltyResultDecided(f *fixture.Fixture) *bool {
	if f.APIStatus.Short != apifootball.StatusPenaltyDone {
		return nil
	}
	decided := f.HomePenalty != nil && f.AwayPenalty != nil && *f.HomePenalty != *f.AwayPenalty
	return &decided
}

// playerName returns the Player's name or empty string if unknown.
// Payloads prefer empty string over a nullable field for JSON
// simplicity; downstream code gates on Player.Known() when needed.
func playerName(p event.Player) string {
	if p.Name == nil {
		return ""
	}
	return *p.Name
}

// registerAndSpawnEvent is the atomic register-on-flip step from
// the 2026-07-16 spawn rule. Both operations are idempotent (INSERT ON
// CONFLICT DO NOTHING for the row; failed-only deterministic identity for the
// spawn), so retry-after-partial-crash is safe. The spawner also owns FF-025's
// conservative stale-running recovery. Nil-safe: no-op if either EventRepo or
// Spawner is missing.
func (a *Activities) registerAndSpawnEvent(ctx context.Context, existing *event.Event, domainEv *event.Event, fixtureID int64) error {
	if a.EventRepo == nil || a.Spawner == nil {
		return nil
	}
	// Never spawn a search for an unknown player — there's no player token to
	// build a Twitter query from (Player.Known() contract). Placeholders are
	// pinned at debounce 0 so they never reach here via the trigger flip, but
	// the recovery pass also calls this, so guard explicitly.
	if !domainEv.Player.Known() {
		return nil
	}
	workflowID := fmt.Sprintf("event-%s", existing.ID)

	// Row insert first — must exist before the spawn returns so the
	// completion check in the same/next cycle sees "downstream pending."
	if err := a.EventRepo.RegisterDownstreamWorkflow(ctx, existing.ID, "discovery", workflowID); err != nil {
		// Skip the spawn to avoid an untracked workflow. The caller records
		// the error without failing reconciliation; next cycle re-attempts.
		return fmt.Errorf("register downstream: %w", err)
	}

	in := discoverycontract.EventWorkflowInput{
		EventID:     existing.ID,
		FixtureID:   fixtureID,
		PlayerName:  playerName(domainEv.Player),
		TeamName:    domainEv.Team.Name,
		TeamID:      int64(domainEv.Team.ID),
		Minute:      domainEv.Minute,
		Extra:       domainEv.Extra,
		FirstSeenAt: existing.FirstSeenAt,
	}
	if err := a.Spawner.SpawnEvent(ctx, workflowID, in); err != nil {
		// The pending row exists. Surface the failure in Reconcile output;
		// the next recovery pass retries without completing the checklist.
		return fmt.Errorf("spawn downstream: %w", err)
	}
	return nil
}

// buildDomainEvent constructs an event.Event from the API payload
// with the caller-supplied seq. Seq is assigned by the reconcile
// identity matcher, which reuses active stored sequences and allocates above
// the complete active + removed history.
func (a *Activities) buildDomainEvent(f *fixture.Fixture, apiEv apifootball.APIFixtureEvent, domainType event.Type, seq int, now time.Time) (*event.Event, string, error) {
	teamID := apiEv.Team.ID
	teamName := apiEv.Team.Name
	if teamID == 0 {
		return nil, "", errors.New("apiEv missing team_id")
	}
	player := event.Player{ID: apiEv.Player.ID, Name: apiEv.Player.Name}

	minute := apiEv.Time.Elapsed
	var extra *int
	if apiEv.Time.Extra != nil {
		extra = apiEv.Time.Extra
	}
	// Empty detail fallback — vendor sometimes omits detail on early
	// event updates. Fall back to the domain type's string rep so the
	// row still has something searchable.
	detail := apiEv.Detail
	if detail == "" {
		detail = apifootball.APIEventDetail(string(domainType))
	}
	e := event.New(
		f.ID,
		event.Team{ID: teamID, Name: teamName},
		player,
		domainType,
		detail,
		minute,
		extra,
		seq,
		now,
	)
	// Assist is non-identity metadata (not part of NaturalKey), so it's set
	// after construction. nil/nil when the vendor reports no assister.
	e.Assist = event.Player{ID: apiEv.Assist.ID, Name: apiEv.Assist.Name}
	return e, e.NaturalKey, nil
}

// trackableType — thin wrapper around event.TrackableEventType that
// handles the nullable Comments field. Filtering logic lives in the
// domain layer per event_config.py's role in Python.
func trackableType(apiEv apifootball.APIFixtureEvent) event.Type {
	comments := ""
	if apiEv.Comments != nil {
		comments = *apiEv.Comments
	}
	t, _ := event.TrackableEventType(apiEv.Type, apiEv.Detail, comments)
	return t
}
