// Durable promotion, publication, cleanup, and terminal-outcome effects for EventWorkflow.
package workflow

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	livefeedactivity "github.com/vedantadhobley/found-footy/internal/activity/livefeed"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	ddiscovery "github.com/vedantadhobley/found-footy/internal/domain/discovery"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// promote copies the clip staging→assets, records asset+share+rank, and adds it
// to the kept set so later candidates dedup against it. Returns the new asset id.
func (p *pipeline) promote(c clip, vout visionactivity.ValidateClipOutput) (uuid.UUID, bool) {
	if p.canceled() {
		return uuid.Nil, false
	}
	startedAt := workflow.Now(p.ctx)
	var pout videoactivity.PromoteAndPersistOutput
	err := workflow.ExecuteActivity(p.persistCtx,
		(*videoactivity.PersistActivities).PromoteAndPersist,
		videoactivity.PromoteAndPersistInput{
			EventID: p.in.EventID, FixtureID: p.in.FixtureID,
			StagingKey: c.stagingKey, MD5: c.md5,
			HashVersion: c.hashVersion, FrameHashes: c.frameHashes,
			Width: c.width, Height: c.height, DurationMS: c.durationMS,
			FileSizeBytes: c.fileSizeBytes, Bitrate: c.bitrate,
			Popularity: c.popularity,
			Verified:   c.verified, ExtractedMinute: vout.MatchedMinute,
		}).Get(p.persistCtx, &pout)
	if err != nil {
		p.logCandidatePhase(c.tweetURL, "promotion", "failed", startedAt)
		if temporal.IsCanceledError(err) {
			return uuid.Nil, false
		}
		p.failExactCluster(c, "promote_error", nil)
		p.log.Warn("promote failed", "tweet_url", c.tweetURL, "err", err)
		return uuid.Nil, false
	}
	p.logCandidatePhase(c.tweetURL, "promotion", "passed", startedAt,
		"asset_id", pout.AssetID.String(), "minted", pout.Minted)
	c.assetID = pout.AssetID
	p.assets = append(p.assets, c)
	if c.verified {
		p.verified++
	} else {
		p.unverified++
	}
	p.recordOutcome(c.tweetURL, discoveryactivity.OutcomePromoted, "",
		jsonDetail(map[string]any{"asset_id": pout.AssetID.String(), "verified": c.verified}))
	if len(c.exactFollowers) > 0 {
		p.duplicates += len(c.exactFollowers)
		detail := jsonDetail(map[string]any{"winner_asset_id": pout.AssetID.String()})
		for _, tweetURL := range c.exactFollowers {
			p.recordOutcome(tweetURL, discoveryactivity.OutcomeDuplicate, "", detail)
		}
	}
	// A completed promotion with a durable share is ready to announce. This
	// includes a retry that found the share created by its failed prior attempt:
	// the workflow never observed that attempt and still owes the dirty signal.
	if pout.Minted {
		p.publishEventVideo(c.tweetURL, "promotion")
	}
	return pout.AssetID, true
}

// rejectExactCluster gives every byte-identical candidate the representative's
// deterministic content verdict. Identical bytes cannot produce a different
// validation result, but none is a duplicate unless an asset actually wins.
func (p *pipeline) rejectExactCluster(c clip, reason string, detail json.RawMessage) {
	p.rejectedClips += 1 + len(c.exactFollowers)
	p.recordOutcome(c.tweetURL, discoveryactivity.OutcomeRejected, reason, detail)
	for _, tweetURL := range c.exactFollowers {
		p.recordOutcome(tweetURL, discoveryactivity.OutcomeRejected, reason, detail)
	}
}

// failExactCluster gives every byte-identical candidate the representative's
// exhausted infrastructure result without multiplying hash, vision, or
// promotion retry budgets for interchangeable copies.
func (p *pipeline) failExactCluster(c clip, reason string, detail json.RawMessage) {
	p.failed += 1 + len(c.exactFollowers)
	p.recordOutcome(c.tweetURL, discoveryactivity.OutcomeFailed, reason, detail)
	for _, tweetURL := range c.exactFollowers {
		p.recordOutcome(tweetURL, discoveryactivity.OutcomeFailed, reason, detail)
	}
}

// duplicateExactCluster closes a representative and all retained exact-byte
// followers onto an existing winner. The winner id makes every duplicate row
// independently auditable.
func (p *pipeline) duplicateExactCluster(c clip, winnerID uuid.UUID) {
	p.duplicates += 1 + len(c.exactFollowers)
	var detail json.RawMessage
	if p.deferExactFollowerOutcomes {
		detail = jsonDetail(map[string]any{"winner_asset_id": winnerID.String()})
	}
	p.recordOutcome(c.tweetURL, discoveryactivity.OutcomeDuplicate, "", detail)
	for _, tweetURL := range c.exactFollowers {
		p.recordOutcome(tweetURL, discoveryactivity.OutcomeDuplicate, "", detail)
	}
}

// supersede consolidates loser assets onto winner via the SupersedeAssets
// activity (superseded_by chain + popularity merge + retire loser shares +
// reclaim bytes + rebalance), then drops the losers from the in-memory kept set
// so they stop matching later clips. A failed activity leaves the losers in
// place (a visible duplicate) rather than corrupting state — the DB is the
// arbiter and the activity is retried.
func (p *pipeline) supersede(winnerID uuid.UUID, loserIDs []uuid.UUID) {
	if len(loserIDs) == 0 || p.canceled() {
		return
	}
	if err := workflow.ExecuteActivity(p.persistCtx,
		(*videoactivity.PersistActivities).SupersedeAssets,
		videoactivity.SupersedeAssetsInput{
			EventID: p.in.EventID, WinnerAssetID: winnerID, LoserAssetIDs: loserIDs,
		}).Get(p.persistCtx, nil); err != nil {
		if temporal.IsCanceledError(err) {
			return
		}
		p.log.Warn("supersede failed", "winner", winnerID.String(), "losers", len(loserIDs), "err", err)
		return
	}
	p.superseded += len(loserIDs)
	// The winner-select collapse changed this event's surfaced set → announce.
	p.publishEventVideo("", "supersede")

	lose := make(map[uuid.UUID]bool, len(loserIDs))
	for _, id := range loserIDs {
		lose[id] = true
	}
	// #181: each loser was promoted earlier, now retired → superseded. Map its
	// asset id back to the candidate tweet_url from the still-intact kept set.
	winner := winnerID.String()
	for _, a := range p.assets {
		if lose[a.assetID] {
			p.recordOutcome(a.tweetURL, discoveryactivity.OutcomeSuperseded, "",
				jsonDetail(map[string]any{"winner_asset_id": winner}))
		}
	}
	kept := p.assets[:0]
	for _, a := range p.assets {
		if !lose[a.assetID] {
			kept = append(kept, a)
		}
	}
	p.assets = kept
}

// bumpPopularity records a collapse onto an already-inserted asset (nil-safe).
// bumpPopularity adds n votes to an existing asset's popularity (nil/n<1 safe).
func (p *pipeline) bumpPopularity(assetID uuid.UUID, n int) {
	if assetID == uuid.Nil || n < 1 || p.canceled() {
		return
	}
	_ = workflow.ExecuteActivity(p.persistCtx,
		(*videoactivity.PersistActivities).BumpAssetPopularity,
		videoactivity.BumpAssetPopularityInput{AssetID: assetID, Count: n}).Get(p.persistCtx, nil)
}

// assetIDsAt collects the asset ids at the given p.assets indices.
func (p *pipeline) assetIDsAt(idxs []int) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(idxs))
	for _, i := range idxs {
		ids = append(ids, p.assets[i].assetID)
	}
	return ids
}

func (p *pipeline) deleteStaging(key string) {
	if key == "" || p.canceled() {
		return
	}
	_ = workflow.ExecuteActivity(p.persistCtx,
		(*videoactivity.PersistActivities).DeleteStaging,
		videoactivity.DeleteStagingInput{StagingKey: key}).Get(p.persistCtx, nil)
}

// publishEventVideo fires the event.video dirty-signal for this event
// (best-effort: a lost ping heals on the frontend's next refetch, so failure
// is swallowed, never propagated). Called only AFTER a promote/supersede has
// durably committed a clip-set change — the workflow blocks on that activity
// before reaching here — so a consumer that refetches on the signal always
// sees the new state. See decisions.md 2026-08-14 (N3).
func (p *pipeline) publishEventVideo(tweetURL, cause string) {
	if p.canceled() {
		return
	}
	startedAt := workflow.Now(p.ctx)
	err := workflow.ExecuteActivity(p.persistCtx,
		(*livefeedactivity.Activities).PublishEventVideo,
		livefeedactivity.EventVideoInput{EventID: p.in.EventID, FixtureID: p.in.FixtureID}).Get(p.persistCtx, nil)
	now := workflow.Now(p.ctx)
	outcome := "passed"
	if err != nil {
		outcome = "failed"
	}
	emitWorkflowMeasurement(p.log, vocabulary.ActionEventPublishMeasured,
		"event video publication measured",
		"event_id", p.in.EventID,
		"fixture_id", p.in.FixtureID,
		"tweet_url", tweetURL,
		"cause", cause,
		"outcome", outcome,
		"duration_ms", elapsedMilliseconds(startedAt, now),
		"event_elapsed_ms", elapsedMilliseconds(p.startedAt, now))
}

// recordOutcome persists a candidate's terminal fate. FF-034 histories use one
// evidence-carrying UPSERT and treat failure as a workflow error; only a
// successful activity advances workflow ownership to terminal. Older histories
// retain RecordCandidateOutcome's best-effort UPDATE command sequence.
func (p *pipeline) recordOutcome(tweetURL string, outcome discoveryactivity.CandidateOutcome, reason string, detail json.RawMessage) {
	if tweetURL == "" || p.canceled() {
		return
	}
	if p.durableCandidates {
		candidate, ok := p.candidates[tweetURL]
		if !ok {
			p.setTerminalError(fmt.Errorf("candidate evidence missing for %s", tweetURL))
			return
		}
		startedAt := workflow.Now(p.ctx)
		err := workflow.ExecuteActivity(p.persistCtx,
			(*discoveryactivity.Activities).UpsertCandidateOutcome,
			discoveryactivity.UpsertCandidateOutcomeInput{
				Evidence: candidate.evidence, Outcome: outcome,
				RejectReason: reason, Detail: detail,
			}).Get(p.persistCtx, nil)
		if err != nil {
			p.logCandidatePhase(tweetURL, "terminal_persist", "failed", startedAt,
				"candidate_outcome", string(outcome))
			p.setTerminalError(fmt.Errorf("persist terminal candidate %s: %w", tweetURL, err))
			return
		}
		p.logCandidatePhase(tweetURL, "terminal_persist", "passed", startedAt,
			"candidate_outcome", string(outcome),
			"candidate_elapsed_ms", elapsedMilliseconds(p.timings[tweetURL].observedAt, workflow.Now(p.ctx)))
		candidate.state = ddiscovery.CandidateTerminal
		p.candidates[tweetURL] = candidate
		return
	}
	startedAt := workflow.Now(p.ctx)
	err := workflow.ExecuteActivity(p.persistCtx,
		(*discoveryactivity.Activities).RecordCandidateOutcome,
		discoveryactivity.RecordCandidateOutcomeInput{
			EventID: p.in.EventID, TweetURL: tweetURL,
			Outcome: outcome, RejectReason: reason, Detail: detail,
		}).Get(p.persistCtx, nil)
	terminalOutcome := "passed"
	if err != nil {
		terminalOutcome = "failed"
	}
	p.logCandidatePhase(tweetURL, "terminal_persist", terminalOutcome, startedAt,
		"candidate_outcome", string(outcome),
		"candidate_elapsed_ms", elapsedMilliseconds(p.timings[tweetURL].observedAt, workflow.Now(p.ctx)))
}

// setTerminalError retains the first durability failure. Later callbacks may
// finish their own cleanup, but the parent may not report success afterward.
func (p *pipeline) setTerminalError(err error) {
	if err == nil || p.terminalErr != nil {
		return
	}
	p.terminalErr = err
	p.log.Error("candidate terminal persistence failed", "err", err)
}

// canceled is checked before scheduling every follow-on activity. Cancellation
// cleanup belongs to the monitor's destroy path, so EventWorkflow must not emit
// new commands after its root context closes.
func (p *pipeline) canceled() bool {
	return p.ctx.Err() != nil
}

// jsonDetail marshals a small map for outcome_detail; a marshal failure (not
// possible for these shapes) yields a nil detail rather than a panic.
func jsonDetail(m map[string]any) json.RawMessage {
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}

// removePending drops and returns the pending entry for stagingKey. The
// returned clip is the LIVE entry, whose popularity may have grown from gate
// md5-dups while vision was in flight (#180). ok=false if it was already gone.
func (p *pipeline) removePending(stagingKey string) (clip, bool) {
	for i := range p.pending {
		if p.pending[i].stagingKey == stagingKey {
			pc := p.pending[i]
			p.pending = append(p.pending[:i], p.pending[i+1:]...)
			return pc, true
		}
	}
	return clip{}, false
}
