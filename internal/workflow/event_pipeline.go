// event_pipeline.go — the EventWorkflow consumer engine (#164c-b): the
// serialized Selector queue that drains download, dense-hash, and vision
// activities, running exact-byte ownership → hash → vision → category-scoped
// perceptual dedup → atomic placement per unique clip. The legacy VideoWorkflow
// child remains replayable.
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
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	ddiscovery "github.com/vedantadhobley/found-footy/internal/domain/discovery"
	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// clip is a candidate's fingerprint + metadata held in workflow memory for
// the event's lifetime. assets holds kept unique clips; pending holds
// deduped-new clips whose vision is still in flight (closes the dedup race).
type clip struct {
	tweetURL        string
	md5             string
	hashVersion     dvideo.FrameHashVersion
	frameHashes     []uint64
	stagingKey      string
	width, height   int
	durationMS      int
	fileSizeBytes   int64
	bitrate         *int
	popularity      int       // accumulated sightings: own (1) + md5-dups collapsed while pending (#180)
	exactFollowers  []string  // byte-identical candidate URLs awaiting the representative's terminal result
	verified        bool      // vision verdict; set at promote — the dedup category (verified↔verified only)
	assetID         uuid.UUID // set once promoted
	visionStartedAt time.Time // workflow-observed start of the vision activity
}

// hashClaim serializes dense hashing for one exact MD5. The primary owns the
// active HashVideo call; waiting candidates are byte-identical fallbacks. A
// failed primary hands ownership to the next candidate instead of losing the
// cluster or reusing a potentially bad staging object.
type hashClaim struct {
	primary   clip
	waiting   []clip
	startedAt time.Time
}

// pipeline holds the consumer's state + the pre-built activity contexts.
type pipeline struct {
	ctx       workflow.Context
	log       log.Logger
	in        EventWorkflowInput
	selector  workflow.Selector
	startedAt time.Time

	// Dedup policy (from the start-of-workflow config read → deterministic).
	// Zero longMinRun disables the sustained route for pre-policy histories.
	maxHamming, minRun, maxGaps             int
	longMaxHamming, longMinRun, longMaxGaps int
	terminalVideoFailures                   bool
	preHashMD5Claim                         bool
	durableCandidates                       bool
	durableDownloadFailures                 bool
	deferExactFollowerOutcomes              bool
	atomicPlacement                         bool

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
	timings     map[string]candidateTiming

	// outcome counters (for the workflow output / logs)
	spawned, passed, rejectedClips, duplicates, verified, unverified, superseded, failed int
}

func newPipeline(ctx workflow.Context, in EventWorkflowInput, cfg pipelineConfig, log log.Logger) *pipeline {
	return &pipeline{
		ctx:        ctx,
		log:        log,
		in:         in,
		selector:   workflow.NewSelector(ctx),
		startedAt:  cfg.startedAt,
		maxHamming: cfg.maxHamming, minRun: cfg.minRun, maxGaps: cfg.maxGaps,
		longMaxHamming: cfg.longMaxHamming, longMinRun: cfg.longMinRun, longMaxGaps: cfg.longMaxGaps,
		terminalVideoFailures:      cfg.terminalVideoFailures,
		preHashMD5Claim:            cfg.preHashMD5Claim,
		durableCandidates:          cfg.durableCandidates,
		durableDownloadFailures:    cfg.durableDownloadFailures,
		deferExactFollowerOutcomes: cfg.deferExactFollowerOutcomes,
		atomicPlacement:            cfg.atomicPlacement,
		downloadCtx:                videoDownloadActivityContext(ctx),
		hashCtx:                    videoHashActivityContext(ctx),
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
		timings:    make(map[string]candidateTiming),
	}
}

type pipelineConfig struct {
	maxHamming, minRun, maxGaps             int
	longMaxHamming, longMinRun, longMaxGaps int
	terminalVideoFailures                   bool
	preHashMD5Claim                         bool
	durableCandidates                       bool
	durableDownloadFailures                 bool
	deferExactFollowerOutcomes              bool
	atomicPlacement                         bool
	startedAt                               time.Time
}

// candidateOwnership joins immutable evidence to the workflow-local lifecycle
// state. Only a successful terminal UPSERT may advance a durable candidate to
// CandidateTerminal.
type candidateOwnership struct {
	evidence discoverycontract.CandidateEvidence
	state    ddiscovery.CandidateState
}

// candidateTiming is workflow-local measurement state. It is never persisted
// and never participates in a command or acceptance decision.
type candidateTiming struct {
	observedAt    time.Time
	searchAttempt int
	recovered     bool
}

// restoreAssets seeds the serialized consumer with durable live assets from a
// prior failed EventWorkflow execution. Without this, a replacement run would
// forget its exact/perceptual dedup set and could treat an already-surfaced
// clip as new. The activity projection uses current public evidence order,
// preserving deterministic winner preference across recovery.
func (p *pipeline) restoreAssets(restored []videoactivity.RestoredEventAsset) {
	for _, asset := range restored {
		popularity := asset.Popularity
		if popularity < 1 {
			popularity = 1
		}
		p.assets = append(p.assets, clip{
			md5: asset.MD5, hashVersion: dvideo.NormalizeFrameHashVersion(asset.HashVersion),
			frameHashes: asset.FrameHashes,
			width:       asset.Width, height: asset.Height, durationMS: asset.DurationMS,
			fileSizeBytes: asset.FileSizeBytes, bitrate: asset.Bitrate,
			popularity: popularity, verified: asset.Verified, assetID: asset.AssetID,
		})
	}
}
