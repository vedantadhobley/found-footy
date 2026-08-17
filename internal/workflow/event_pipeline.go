// event_pipeline.go — the EventWorkflow consumer engine (#164c-b): the
// serialized Selector queue that drains download, dense-hash, and vision
// activities, running exact-byte ownership → hash → perceptual dedup → promote
// → rank per unique clip. The legacy VideoWorkflow child remains replayable.
//
// All state (assets / pending / hashing / inFlight) lives in the `pipeline`
// struct and
// is mutated only inside the Selector callbacks + the producer's
// spawnCandidate —
// which, because Temporal coroutines are cooperatively scheduled (one runs at
// a time, yielding only at Get/Sleep/Select/Await), are automatically
// race-free. That single-threadedness IS the serialization; no locks.
//
// The lone step that MUST be serial is dedup (match against assets∪pending):
// two clips deciding "am I a dup?" simultaneously would both slip through.
// Everything else runs in parallel across distinct content. Exact-byte
// arrivals share one dense hash claim. See the FF-022 decision record.
package workflow

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	livefeedactivity "github.com/vedantadhobley/found-footy/internal/activity/livefeed"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	ddiscovery "github.com/vedantadhobley/found-footy/internal/domain/discovery"
	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// clip is a candidate's fingerprint + metadata held in workflow memory for
// the event's lifetime. assets holds kept unique clips; pending holds
// deduped-new clips whose vision is still in flight (closes the dedup race).
type clip struct {
	tweetURL      string
	md5           string
	frameHashes   []uint64
	stagingKey    string
	width, height int
	durationMS    int
	fileSizeBytes int64
	bitrate       *int
	popularity    int       // accumulated sightings: own (1) + md5-dups collapsed while pending (#180)
	verified      bool      // vision verdict; set at promote — the dedup category (verified↔verified only)
	assetID       uuid.UUID // set once promoted
}

// hashClaim serializes dense hashing for one exact MD5. The primary owns the
// active HashVideo call; waiting candidates are byte-identical fallbacks. A
// failed primary hands ownership to the next candidate instead of losing the
// cluster or reusing a potentially bad staging object.
type hashClaim struct {
	primary clip
	waiting []clip
}

// pipeline holds the consumer's state + the pre-built activity contexts.
type pipeline struct {
	ctx      workflow.Context
	log      log.Logger
	in       EventWorkflowInput
	selector workflow.Selector

	// dedup thresholds (from the start-of-workflow config read → deterministic)
	maxHamming, minRun, maxGaps int
	terminalVideoFailures       bool
	preHashMD5Claim             bool
	durableCandidates           bool

	// activity option ctxs
	downloadCtx workflow.Context
	hashCtx     workflow.Context
	visionCtx   workflow.Context
	persistCtx  workflow.Context

	// state — mutated only in callbacks / spawnCandidate (single-threaded)
	assets      []clip
	pending     []clip
	hashing     map[string]*hashClaim
	inFlight    int
	searchDone  bool
	searchErr   error
	terminalErr error
	childSeq    int
	candidates  map[string]candidateOwnership

	// outcome counters (for the workflow output / logs)
	spawned, passed, rejectedClips, duplicates, verified, unverified, superseded, failed int
}

func newPipeline(ctx workflow.Context, in EventWorkflowInput, cfg pipelineConfig, log log.Logger) *pipeline {
	return &pipeline{
		ctx:        ctx,
		log:        log,
		in:         in,
		selector:   workflow.NewSelector(ctx),
		maxHamming: cfg.maxHamming, minRun: cfg.minRun, maxGaps: cfg.maxGaps,
		terminalVideoFailures: cfg.terminalVideoFailures,
		preHashMD5Claim:       cfg.preHashMD5Claim,
		durableCandidates:     cfg.durableCandidates,
		downloadCtx:           videoDownloadActivityContext(ctx),
		hashCtx:               videoHashActivityContext(ctx),
		visionCtx: workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 3 * time.Minute, // vision is slow (multi-frame VLM)
			HeartbeatTimeout:    time.Minute,
			RetryPolicy:         &temporal.RetryPolicy{InitialInterval: 2 * time.Second, BackoffCoefficient: 2, MaximumAttempts: 3},
		}),
		persistCtx: workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 2 * time.Minute, // S3 copy + pg writes
			RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2, MaximumAttempts: 5},
		}),
		hashing:    make(map[string]*hashClaim),
		candidates: make(map[string]candidateOwnership),
	}
}

type pipelineConfig struct {
	maxHamming, minRun, maxGaps int
	terminalVideoFailures       bool
	preHashMD5Claim             bool
	durableCandidates           bool
}

// candidateOwnership joins immutable evidence to the workflow-local lifecycle
// state. Only a successful terminal UPSERT may advance a durable candidate to
// CandidateTerminal.
type candidateOwnership struct {
	evidence ddiscovery.CandidateEvidence
	state    ddiscovery.CandidateState
}

// restoreAssets seeds the serialized consumer with durable live assets from a
// prior failed EventWorkflow execution. Without this, a replacement run would
// forget its exact/perceptual dedup set and could treat an already-surfaced
// clip as new. The activity projection is rank ordered, which preserves the
// existing winner order until a later promotion rebalances it.
func (p *pipeline) restoreAssets(restored []videoactivity.RestoredEventAsset) {
	for _, asset := range restored {
		popularity := asset.Popularity
		if popularity < 1 {
			popularity = 1
		}
		p.assets = append(p.assets, clip{
			md5: asset.MD5, frameHashes: asset.FrameHashes,
			width: asset.Width, height: asset.Height, durationMS: asset.DurationMS,
			fileSizeBytes: asset.FileSizeBytes, bitrate: asset.Bitrate,
			popularity: popularity, verified: asset.Verified, assetID: asset.AssetID,
		})
	}
}

// spawnCandidate takes workflow ownership of one candidate and starts its
// processing immediately. FF-022 schedules DownloadAndStage directly so
// EventWorkflow can claim its MD5 before dense hashing. The child workflow
// branch preserves pre-FF-022 histories.
func (p *pipeline) spawnCandidate(gctx workflow.Context, evidence ddiscovery.CandidateEvidence) {
	if gctx.Err() != nil {
		return
	}
	tweetURL := evidence.TweetURL
	if tweetURL == "" {
		return
	}
	if p.durableCandidates {
		p.candidates[tweetURL] = candidateOwnership{
			evidence: evidence,
			state:    ddiscovery.CandidateInFlight,
		}
	}
	p.spawned++
	if p.preHashMD5Claim {
		fut := workflow.ExecuteActivity(p.downloadCtx,
			(*videoactivity.Activities).DownloadAndStage,
			videoactivity.DownloadAndStageInput{
				EventID: p.in.EventID, FixtureID: p.in.FixtureID, TweetURL: tweetURL,
			})
		p.inFlight++
		p.selector.AddFuture(fut, p.onDownloadDone(tweetURL))
		return
	}
	p.childSeq++
	cctx := workflow.WithChildOptions(gctx, workflow.ChildWorkflowOptions{
		WorkflowID:               fmt.Sprintf("video-%s-%d", p.in.EventID, p.childSeq),
		WorkflowExecutionTimeout: 10 * time.Minute,
	})
	fut := workflow.ExecuteChildWorkflow(cctx, VideoWorkflow, VideoWorkflowInput{
		EventID:   p.in.EventID,
		FixtureID: p.in.FixtureID,
		TweetURL:  tweetURL,
	})
	p.inFlight++
	p.selector.AddFuture(fut, p.onVideoDone(tweetURL))
}

// onDownloadDone claims an exact MD5 before scheduling dense extraction. A
// duplicate of a kept/pending clip collapses immediately; a duplicate of an
// active hash waits behind its claimant without consuming an ffmpeg slot.
func (p *pipeline) onDownloadDone(tweetURL string) func(workflow.Future) {
	return func(f workflow.Future) {
		p.inFlight--
		if p.canceled() {
			return
		}

		var out videoactivity.DownloadAndStageOutput
		if err := f.Get(p.ctx, &out); err != nil {
			p.failed++
			p.log.Warn("candidate download failed after retries", "tweet_url", tweetURL, "err", err)
			p.recordOutcome(tweetURL, discoveryactivity.OutcomeFailed, string(VideoFailureDownload), nil)
			return
		}
		if out.Outcome == videoactivity.OutcomeRejected {
			p.rejectedClips++
			p.recordOutcome(tweetURL, discoveryactivity.OutcomeRejected, out.RejectReason, nil)
			return
		}
		if out.Outcome != videoactivity.OutcomePassed || out.MD5 == "" || out.StagingKey == "" {
			p.failed++
			p.recordOutcome(tweetURL, discoveryactivity.OutcomeFailed,
				string(VideoFailureInvalidChildOutput),
				jsonDetail(map[string]any{"outcome": out.Outcome, "md5_present": out.MD5 != "", "staging_present": out.StagingKey != ""}))
			p.deleteStaging(out.StagingKey)
			return
		}

		c := clipFromDownload(tweetURL, out)
		if idx, isAsset, matched := p.matchMD5(c); matched {
			p.duplicates++
			p.collapse(c, idx, isAsset)
			p.recordOutcome(c.tweetURL, discoveryactivity.OutcomeDuplicate, "", nil)
			return
		}
		if claim, exists := p.hashing[c.md5]; exists {
			claim.waiting = append(claim.waiting, c)
			return
		}

		p.hashing[c.md5] = &hashClaim{primary: c}
		p.fireHash(c.md5)
	}
}

// fireHash schedules the one active dense extraction for an exact-byte claim.
func (p *pipeline) fireHash(md5 string) {
	if p.canceled() {
		return
	}
	claim, ok := p.hashing[md5]
	if !ok {
		return
	}
	fut := workflow.ExecuteActivity(p.hashCtx,
		(*videoactivity.Activities).HashVideo,
		videoactivity.HashVideoInput{StagingKey: claim.primary.stagingKey})
	p.inFlight++
	p.selector.AddFuture(fut, p.onHashDone(md5))
}

// onHashDone releases a successful claim to vision, or transfers a failed
// claim to the next exact-byte staging object. Only candidates whose own hash
// attempt fails receive hash_error; untried waiters remain recoverable.
func (p *pipeline) onHashDone(md5 string) func(workflow.Future) {
	return func(f workflow.Future) {
		p.inFlight--
		if p.canceled() {
			return
		}
		claim, ok := p.hashing[md5]
		if !ok {
			return
		}

		var out videoactivity.HashVideoOutput
		if err := f.Get(p.ctx, &out); err != nil {
			failed := claim.primary
			p.failed++
			p.log.Warn("candidate hash failed after retries",
				"tweet_url", failed.tweetURL, "staging_key", failed.stagingKey, "err", err)
			p.recordOutcome(failed.tweetURL, discoveryactivity.OutcomeFailed, string(VideoFailureHash), nil)
			p.deleteStaging(failed.stagingKey)
			if len(claim.waiting) == 0 {
				delete(p.hashing, md5)
				return
			}
			claim.primary = claim.waiting[0]
			claim.waiting = claim.waiting[1:]
			p.fireHash(md5)
			return
		}

		winner := claim.primary
		winner.frameHashes = out.FrameHashes
		p.passed++
		for _, duplicate := range claim.waiting {
			winner.popularity += duplicate.popularity
			p.duplicates++
			p.recordOutcome(duplicate.tweetURL, discoveryactivity.OutcomeDuplicate, "", nil)
			p.deleteStaging(duplicate.stagingKey)
		}
		delete(p.hashing, md5)
		p.pending = append(p.pending, winner)
		p.fireVision(winner)
	}
}

// clipFromDownload converts the staged activity result into workflow-owned
// state without carrying activity structs through later callbacks.
func clipFromDownload(tweetURL string, out videoactivity.DownloadAndStageOutput) clip {
	c := clip{
		tweetURL: tweetURL, md5: out.MD5, stagingKey: out.StagingKey,
		width: out.Width, height: out.Height, durationMS: out.DurationMS,
		fileSizeBytes: out.SizeBytes, popularity: 1,
	}
	if out.Bitrate != 0 {
		b := out.Bitrate
		c.bitrate = &b
	}
	return c
}

// finishSearch closes the producer side on every normal or error exit. The
// consumer owns the workflow's return path and therefore also owns propagating
// this error after already-ready callbacks have drained.
func (p *pipeline) finishSearch(err error) {
	p.searchErr = err
	p.searchDone = true
}

// run drives the consumer loop until the producer's search is done AND nothing
// is in flight. HasPending + Await keeps us from blocking on Select when
// nothing is ready but the producer is still working (or the event had zero
// candidates and completes immediately). Await errors are terminal: retrying
// Await on a canceled context returns immediately and creates a workflow-task
// busy loop.
func (p *pipeline) run() error {
	for {
		if p.searchDone && p.inFlight == 0 {
			if p.terminalErr != nil {
				return p.terminalErr
			}
			if p.durableCandidates {
				for tweetURL, candidate := range p.candidates {
					if candidate.state != ddiscovery.CandidateTerminal {
						return fmt.Errorf("candidate %s ended in non-terminal state %q", tweetURL, candidate.state)
					}
				}
			}
			return p.searchErr
		}
		if p.selector.HasPending() {
			p.selector.Select(p.ctx) // runs exactly one callback
			continue
		}
		if err := workflow.Await(p.ctx, func() bool {
			return p.selector.HasPending() || (p.searchDone && p.inFlight == 0)
		}); err != nil {
			return err
		}
	}
}

// onVideoDone handles a completed VideoWorkflow child: dedup, then fire vision
// for a genuinely-new clip. Runs in the consumer coroutine (via Select).
func (p *pipeline) onVideoDone(fallbackTweetURL string) func(workflow.Future) {
	return func(f workflow.Future) {
		p.inFlight-- // decrement FIRST — every path below must not skip this
		if p.canceled() {
			return
		}

		var out VideoWorkflowOutput
		if err := f.Get(p.ctx, &out); err != nil {
			p.failed++
			p.log.Warn("video child failed", "tweet_url", fallbackTweetURL, "err", err)
			if p.terminalVideoFailures {
				p.recordOutcome(fallbackTweetURL, discoveryactivity.OutcomeFailed,
					string(VideoFailureUnexpectedChild), nil)
			}
			return
		}

		tweetURL := out.TweetURL
		if tweetURL == "" {
			tweetURL = fallbackTweetURL
		}
		switch out.Outcome {
		case VideoOutcomeRejected:
			p.rejectedClips++ // hard-filter / geo / deleted — nothing was staged
			p.recordOutcome(tweetURL, discoveryactivity.OutcomeRejected, out.RejectReason, nil)
			return
		case VideoOutcomeFailed:
			p.failed++
			reason := out.FailureReason
			if reason == "" {
				reason = VideoFailureInvalidChildOutput
			}
			p.recordOutcome(tweetURL, discoveryactivity.OutcomeFailed, string(reason), nil)
			p.deleteStaging(out.StagingKey)
			return
		case VideoOutcomePassed:
			p.passed++
		default:
			p.failed++
			p.recordOutcome(tweetURL, discoveryactivity.OutcomeFailed,
				string(VideoFailureInvalidChildOutput),
				jsonDetail(map[string]any{"outcome": string(out.Outcome)}))
			p.deleteStaging(out.StagingKey)
			return
		}

		c := clip{
			tweetURL: tweetURL, md5: out.MD5, frameHashes: out.FrameHashes,
			stagingKey: out.StagingKey, width: out.Width, height: out.Height,
			durationMS: out.DurationMS, fileSizeBytes: out.SizeBytes, popularity: 1,
		}
		if out.Bitrate != 0 {
			b := out.Bitrate
			c.bitrate = &b
		}

		// GATE DEDUP — md5-exact ONLY. Perceptual dedup is category-scoped and runs
		// POST-vision (a clip's verified/unverified category is unknown until vision;
		// decisions.md 2026-08-09). md5-identical bytes are the same clip in every
		// respect — same category — so collapsing them here is always safe.
		if idx, isAsset, matched := p.matchMD5(c); matched {
			p.duplicates++
			p.collapse(c, idx, isAsset)
			p.recordOutcome(c.tweetURL, discoveryactivity.OutcomeDuplicate, "", nil)
			return
		}

		// md5-unique → reserve its pending slot (so a later md5-dup collapses onto
		// it) and fire vision. Perceptual dedup + which-to-keep run when vision lands.
		p.pending = append(p.pending, c)
		p.fireVision(c)
	}
}

// matchMD5 reports whether c is byte-identical to a kept or in-flight clip.
// md5-exact is the ONLY dedup safe before vision: identical bytes are the same
// clip in every respect (same category, quality, frames). Returns the matched
// index into the relevant list and isAsset — true for a promoted asset (has a
// DB row), false for a still-pending clip (votes accumulate in memory, #180).
func (p *pipeline) matchMD5(c clip) (idx int, isAsset, matched bool) {
	for i := range p.assets {
		if p.assets[i].md5 == c.md5 {
			return i, true, true
		}
	}
	for i := range p.pending {
		if p.pending[i].md5 == c.md5 {
			return i, false, true // matched a not-yet-promoted (pending) clip
		}
	}
	return -1, false, false
}

// matchAssets returns the indices of ALL kept assets in c's OWN category
// (verified↔verified, unverified↔unverified) that c perceptually matches.
// Category scoping is load-bearing: one broadcast yields visually-similar frames
// across DIFFERENT goals, so an unverified different-moment clip can dHash-match
// the verified clip of THIS goal — the clock (category) is the only ground-truth
// pinning a clip to this goal (decisions.md 2026-08-09). Never early-returns:
// dHash isn't transitive, so a clip can bridge two assets that don't match each
// other, and every bridged asset must consolidate.
func (p *pipeline) matchAssets(c clip) []int {
	var out []int
	for i := range p.assets {
		if p.assets[i].verified != c.verified {
			continue // different pool — never compared
		}
		if dvideo.Match(c.frameHashes, p.assets[i].frameHashes, p.maxHamming, p.minRun, p.maxGaps) {
			out = append(out, i)
		}
	}
	return out
}

// quality projects a clip's download-time metadata into the dedup comparator.
func (c clip) quality() dvideo.ClipQuality {
	return dvideo.ClipQuality{
		DurationMS: c.durationMS, Bitrate: c.bitrate, Width: c.width, Height: c.height,
	}
}

// collapse merges an md5-exact duplicate onto its winner. Against a promoted
// asset the loser's votes go straight to the DB row; against a still-pending
// clip (no row yet) they accumulate IN MEMORY on that pending clip and ride
// into its popularity when it promotes (#180). Loser bytes are dropped either
// way.
func (p *pipeline) collapse(loser clip, idx int, isAsset bool) {
	if p.canceled() {
		return
	}
	if isAsset {
		p.bumpPopularity(p.assets[idx].assetID, loser.popularity)
	} else {
		p.pending[idx].popularity += loser.popularity
	}
	p.deleteStaging(loser.stagingKey)
}

// fireVision runs the single multi-frame validation call for a unique clip.
func (p *pipeline) fireVision(c clip) {
	if p.canceled() {
		return
	}
	extra := 0
	if p.in.Extra != nil {
		extra = *p.in.Extra
	}
	fut := workflow.ExecuteActivity(p.visionCtx,
		(*visionactivity.Activities).ValidateClip,
		visionactivity.ValidateClipInput{
			EventID: p.in.EventID, FixtureID: p.in.FixtureID,
			StagingKey: c.stagingKey, APIElapsed: p.in.Minute, APIExtra: extra,
		})
	p.inFlight++
	p.selector.AddFuture(fut, p.onVisionDone(c))
}

// onVisionDone returns the callback for a clip's vision verdict.
func (p *pipeline) onVisionDone(c clip) func(workflow.Future) {
	return func(f workflow.Future) {
		p.inFlight--
		if p.canceled() {
			return
		}
		// The closure captured `c` by value at fireVision; gate md5-dups may have
		// bumped the LIVE pending entry's popularity since. Re-read it (#180).
		if pc, ok := p.removePending(c.stagingKey); ok {
			c.popularity = pc.popularity
		}

		var vout visionactivity.ValidateClipOutput
		if err := f.Get(p.ctx, &vout); err != nil {
			// Vision infra-fail after retries — drop the clip + its staging.
			p.failed++
			p.recordOutcome(c.tweetURL, discoveryactivity.OutcomeFailed, "vision_error", nil)
			p.deleteStaging(c.stagingKey)
			return
		}

		switch vout.Outcome {
		case string(dvision.OutcomeVerified), string(dvision.OutcomeUnverified):
			c.verified = vout.Outcome == string(dvision.OutcomeVerified)
			p.dedupAndPromote(c, vout)
		default: // rejected — not soccer / screen recording / wrong clock
			p.rejectedClips++
			detail := map[string]any{"soccer_votes": vout.SoccerVotes, "screen_votes": vout.ScreenVotes, "frame_count": len(vout.Frames)}
			if vout.DetectedMinute != nil { // clock was read but didn't match — record it so the reject is triageable (#181)
				detail["detected_minute"], detail["detected_period"] = *vout.DetectedMinute, vout.DetectedPeriod
				detail["expected_minute"], detail["expected_period"] = vout.ExpectedMinute, vout.ExpectedPeriod
			}
			p.recordOutcome(c.tweetURL, discoveryactivity.OutcomeRejected, vout.Reason, jsonDetail(detail))
			p.deleteStaging(c.stagingKey)
		}
	}
}

// dedupAndPromote is the POST-vision dedup + which-to-keep step (#171). It
// perceptually dedups the vision-passed clip WITHIN its own category pool, then
// either promotes it (unique, or the cluster's quality winner) or collapses it
// (a better clip already exists). dHash isn't transitive, so a clip can match
// several assets at once (a bridge) — all of them consolidate onto the single
// winner, popularity merging. Verified vs unverified never mix (matchAssets
// scopes by category); across pools it's pure ranking, verified always above.
func (p *pipeline) dedupAndPromote(c clip, vout visionactivity.ValidateClipOutput) {
	matched := p.matchAssets(c)
	if len(matched) == 0 {
		p.promote(c, vout) // unique in its pool
		return
	}

	// Highest-quality existing asset in the cluster (ties keep the lower index →
	// stable, no churn).
	best := matched[0]
	for _, idx := range matched[1:] {
		if dvideo.IsUpgrade(p.assets[idx].quality(), p.assets[best].quality()) {
			best = idx
		}
	}

	if dvideo.IsUpgrade(c.quality(), p.assets[best].quality()) {
		// c is a meaningful upgrade over the best incumbent → c wins the pool.
		// Promote it, then supersede every matched asset onto it.
		losers := p.assetIDsAt(matched)
		winnerID, ok := p.promote(c, vout)
		if !ok {
			return
		}
		p.supersede(winnerID, losers)
		return
	}

	// An existing asset wins. c collapses onto it (bump + drop); any OTHER
	// matched assets (a bridge c revealed) also consolidate onto that winner.
	p.duplicates++
	p.recordOutcome(c.tweetURL, discoveryactivity.OutcomeDuplicate, "", nil)
	winnerID := p.assets[best].assetID
	var losers []uuid.UUID
	for _, idx := range matched {
		if idx != best {
			losers = append(losers, p.assets[idx].assetID)
		}
	}
	p.bumpPopularity(winnerID, c.popularity)
	p.deleteStaging(c.stagingKey)
	p.supersede(winnerID, losers)
}

// promote copies the clip staging→assets, records asset+share+rank, and adds it
// to the kept set so later candidates dedup against it. Returns the new asset id.
func (p *pipeline) promote(c clip, vout visionactivity.ValidateClipOutput) (uuid.UUID, bool) {
	if p.canceled() {
		return uuid.Nil, false
	}
	var pout videoactivity.PromoteAndPersistOutput
	err := workflow.ExecuteActivity(p.persistCtx,
		(*videoactivity.PersistActivities).PromoteAndPersist,
		videoactivity.PromoteAndPersistInput{
			EventID: p.in.EventID, FixtureID: p.in.FixtureID,
			StagingKey: c.stagingKey, MD5: c.md5, FrameHashes: c.frameHashes,
			Width: c.width, Height: c.height, DurationMS: c.durationMS,
			FileSizeBytes: c.fileSizeBytes, Bitrate: c.bitrate,
			Popularity: c.popularity,
			Verified:   c.verified, ExtractedMinute: vout.MatchedMinute,
		}).Get(p.persistCtx, &pout)
	if err != nil {
		if temporal.IsCanceledError(err) {
			return uuid.Nil, false
		}
		p.failed++
		p.recordOutcome(c.tweetURL, discoveryactivity.OutcomeFailed, "promote_error", nil)
		p.log.Warn("promote failed", "tweet_url", c.tweetURL, "err", err)
		return uuid.Nil, false
	}
	c.assetID = pout.AssetID
	p.assets = append(p.assets, c)
	if c.verified {
		p.verified++
	} else {
		p.unverified++
	}
	p.recordOutcome(c.tweetURL, discoveryactivity.OutcomePromoted, "",
		jsonDetail(map[string]any{"asset_id": pout.AssetID.String(), "verified": c.verified}))
	// A completed promotion with a durable share is ready to announce. This
	// includes a retry that found the share created by its failed prior attempt:
	// the workflow never observed that attempt and still owes the dirty signal.
	if pout.Minted {
		p.publishEventVideo()
	}
	return pout.AssetID, true
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
	p.publishEventVideo()

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
func (p *pipeline) publishEventVideo() {
	if p.canceled() {
		return
	}
	_ = workflow.ExecuteActivity(p.persistCtx,
		(*livefeedactivity.Activities).PublishEventVideo,
		livefeedactivity.EventVideoInput{EventID: p.in.EventID, FixtureID: p.in.FixtureID}).Get(p.persistCtx, nil)
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
		err := workflow.ExecuteActivity(p.persistCtx,
			(*discoveryactivity.Activities).UpsertCandidateOutcome,
			discoveryactivity.UpsertCandidateOutcomeInput{
				Evidence: candidate.evidence, Outcome: outcome,
				RejectReason: reason, Detail: detail,
			}).Get(p.persistCtx, nil)
		if err != nil {
			p.setTerminalError(fmt.Errorf("persist terminal candidate %s: %w", tweetURL, err))
			return
		}
		candidate.state = ddiscovery.CandidateTerminal
		p.candidates[tweetURL] = candidate
		return
	}
	_ = workflow.ExecuteActivity(p.persistCtx,
		(*discoveryactivity.Activities).RecordCandidateOutcome,
		discoveryactivity.RecordCandidateOutcomeInput{
			EventID: p.in.EventID, TweetURL: tweetURL,
			Outcome: outcome, RejectReason: reason, Detail: detail,
		}).Get(p.persistCtx, nil)
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
