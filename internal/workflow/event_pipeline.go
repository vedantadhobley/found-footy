// event_pipeline.go — the EventWorkflow consumer engine (#164c-b): the
// serialized Selector queue that drains completed VideoWorkflow children and
// vision activities, running dedup → vision → promote → rank per unique clip.
//
// All state (assets / pending / inFlight) lives in the `pipeline` struct and
// is mutated only inside the Selector callbacks + the producer's spawnChild —
// which, because Temporal coroutines are cooperatively scheduled (one runs at
// a time, yielding only at Get/Sleep/Select/Await), are automatically
// race-free. That single-threadedness IS the serialization; no locks.
//
// The lone step that MUST be serial is dedup (match against assets∪pending):
// two clips deciding "am I a dup?" simultaneously would both slip through.
// Everything else — download/hash (in the child) and vision (fired async) —
// runs in parallel. See docs/design/v-phase-orchestration.md.
package workflow

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
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
	assetID       uuid.UUID // set once promoted
}

// pipeline holds the consumer's state + the pre-built activity contexts.
type pipeline struct {
	ctx      workflow.Context
	log      log.Logger
	in       EventWorkflowInput
	selector workflow.Selector

	// dedup thresholds (from the start-of-workflow config read → deterministic)
	maxHamming, minRun, maxGaps int

	// activity option ctxs
	visionCtx  workflow.Context
	persistCtx workflow.Context

	// state — mutated only in callbacks / spawnChild (single-threaded)
	assets     []clip
	pending    []clip
	inFlight   int
	searchDone bool
	childSeq   int

	// outcome counters (for the workflow output / logs)
	spawned, passed, rejectedClips, duplicates, verified, unverified, failed int
}

func newPipeline(ctx workflow.Context, in EventWorkflowInput, cfg pipelineConfig, log log.Logger) *pipeline {
	return &pipeline{
		ctx:       ctx,
		log:       log,
		in:        in,
		selector:  workflow.NewSelector(ctx),
		maxHamming: cfg.maxHamming, minRun: cfg.minRun, maxGaps: cfg.maxGaps,
		visionCtx: workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 3 * time.Minute, // vision is slow (multi-frame VLM)
			HeartbeatTimeout:    time.Minute,
			RetryPolicy:         &temporal.RetryPolicy{InitialInterval: 2 * time.Second, BackoffCoefficient: 2, MaximumAttempts: 3},
		}),
		persistCtx: workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 2 * time.Minute, // S3 copy + pg writes
			RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2, MaximumAttempts: 5},
		}),
	}
}

type pipelineConfig struct{ maxHamming, minRun, maxGaps int }

// spawnChild launches a VideoWorkflow child for one candidate and registers
// its future on the Selector. Called from the producer coroutine.
func (p *pipeline) spawnChild(gctx workflow.Context, tweetURL string) {
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
	p.spawned++
	p.selector.AddFuture(fut, p.onVideoDone)
}

// run drives the consumer loop until the producer's search is done AND nothing
// is in flight. HasPending + Await keeps us from blocking on Select when
// nothing is ready but the producer is still working (or the event had zero
// candidates and completes immediately).
func (p *pipeline) run() {
	for {
		if p.searchDone && p.inFlight == 0 {
			return
		}
		if p.selector.HasPending() {
			p.selector.Select(p.ctx) // runs exactly one callback
			continue
		}
		_ = workflow.Await(p.ctx, func() bool {
			return p.selector.HasPending() || (p.searchDone && p.inFlight == 0)
		})
	}
}

// onVideoDone handles a completed VideoWorkflow child: dedup, then fire vision
// for a genuinely-new clip. Runs in the consumer coroutine (via Select).
func (p *pipeline) onVideoDone(f workflow.Future) {
	p.inFlight-- // decrement FIRST — every path below must not skip this

	var out VideoWorkflowOutput
	if err := f.Get(p.ctx, &out); err != nil {
		p.failed++
		p.log.Warn("video child failed", "err", err)
		return
	}
	if out.Outcome != "passed" {
		p.rejectedClips++ // hard-filter / geo / deleted — nothing was staged
		return
	}
	p.passed++

	c := clip{
		tweetURL: out.TweetURL, md5: out.MD5, frameHashes: out.FrameHashes,
		stagingKey: out.StagingKey, width: out.Width, height: out.Height,
		durationMS: out.DurationMS, fileSizeBytes: out.SizeBytes,
	}
	if out.Bitrate != 0 {
		b := out.Bitrate
		c.bitrate = &b
	}

	// DEDUP — the one serial step. Compare against everything kept OR
	// pending-vision (the pending set closes the async-vision race).
	if idx, matched := p.matchExisting(c); matched {
		p.duplicates++
		p.collapse(c, idx)
		return
	}

	// New unique clip → reserve its slot in pending, then fire vision async.
	p.pending = append(p.pending, c)
	p.fireVision(c)
}

// matchExisting reports whether c perceptually matches an already-kept or
// pending clip (md5 exact first, then the offset/gap-tolerant frame-window).
// Returns the assets-index of the match (or -1 if the match is in pending).
func (p *pipeline) matchExisting(c clip) (assetIdx int, matched bool) {
	for i := range p.assets {
		if p.assets[i].md5 == c.md5 || dvideo.Match(c.frameHashes, p.assets[i].frameHashes, p.maxHamming, p.minRun, p.maxGaps) {
			return i, true
		}
	}
	for i := range p.pending {
		if p.pending[i].md5 == c.md5 || dvideo.Match(c.frameHashes, p.pending[i].frameHashes, p.maxHamming, p.minRun, p.maxGaps) {
			return -1, true // matched a not-yet-promoted clip
		}
	}
	return -1, false
}

// collapse merges a duplicate onto the winner: bump the winner's popularity
// (only if it's an already-inserted asset) and drop the loser's staging object.
func (p *pipeline) collapse(loser clip, assetIdx int) {
	if assetIdx >= 0 && p.assets[assetIdx].assetID != uuid.Nil {
		_ = workflow.ExecuteActivity(p.persistCtx,
			(*videoactivity.PersistActivities).BumpAssetPopularity,
			videoactivity.BumpAssetPopularityInput{AssetID: p.assets[assetIdx].assetID}).Get(p.persistCtx, nil)
	}
	p.deleteStaging(loser.stagingKey)
}

// fireVision runs the single multi-frame validation call for a unique clip.
func (p *pipeline) fireVision(c clip) {
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
		p.removePending(c.stagingKey)

		var vout visionactivity.ValidateClipOutput
		if err := f.Get(p.ctx, &vout); err != nil {
			// Vision infra-fail after retries — drop the clip + its staging.
			p.failed++
			p.deleteStaging(c.stagingKey)
			return
		}

		switch vout.Outcome {
		case string(dvision.OutcomeVerified), string(dvision.OutcomeUnverified):
			p.promote(c, vout)
		default: // rejected — not soccer / screen recording / wrong clock
			p.rejectedClips++
			p.deleteStaging(c.stagingKey)
		}
	}
}

// promote copies the clip staging→assets and records asset+share+rank, then
// adds it to the kept set so later candidates dedup against it.
func (p *pipeline) promote(c clip, vout visionactivity.ValidateClipOutput) {
	var pout videoactivity.PromoteAndPersistOutput
	err := workflow.ExecuteActivity(p.persistCtx,
		(*videoactivity.PersistActivities).PromoteAndPersist,
		videoactivity.PromoteAndPersistInput{
			EventID: p.in.EventID, FixtureID: p.in.FixtureID,
			StagingKey: c.stagingKey, MD5: c.md5, FrameHashes: c.frameHashes,
			Width: c.width, Height: c.height, DurationMS: c.durationMS,
			FileSizeBytes: c.fileSizeBytes, Bitrate: c.bitrate,
			Verified: vout.Outcome == string(dvision.OutcomeVerified), ExtractedMinute: vout.MatchedMinute,
		}).Get(p.persistCtx, &pout)
	if err != nil {
		p.failed++
		p.log.Warn("promote failed", "tweet_url", c.tweetURL, "err", err)
		return
	}
	c.assetID = pout.AssetID
	p.assets = append(p.assets, c)
	if vout.Outcome == string(dvision.OutcomeVerified) {
		p.verified++
	} else {
		p.unverified++
	}
}

func (p *pipeline) deleteStaging(key string) {
	if key == "" {
		return
	}
	_ = workflow.ExecuteActivity(p.persistCtx,
		(*videoactivity.PersistActivities).DeleteStaging,
		videoactivity.DeleteStagingInput{StagingKey: key}).Get(p.persistCtx, nil)
}

func (p *pipeline) removePending(stagingKey string) {
	for i := range p.pending {
		if p.pending[i].stagingKey == stagingKey {
			p.pending = append(p.pending[:i], p.pending[i+1:]...)
			return
		}
	}
}
