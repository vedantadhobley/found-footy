// Candidate intake, exact-byte ownership, and dense-hash callbacks for EventWorkflow.
package workflow

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/workflow"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	ddiscovery "github.com/vedantadhobley/found-footy/internal/domain/discovery"
	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// spawnCandidate takes workflow ownership of one candidate and starts its
// processing immediately. FF-022 schedules DownloadAndStage directly so
// EventWorkflow can claim its MD5 before dense hashing. The child workflow
// branch preserves pre-FF-022 histories.
func (p *pipeline) spawnCandidate(gctx workflow.Context, evidence discoverycontract.CandidateEvidence, recovered bool) {
	if gctx.Err() != nil {
		return
	}
	tweetURL := evidence.TweetURL
	if tweetURL == "" {
		return
	}
	if _, exists := p.timings[tweetURL]; !exists {
		p.timings[tweetURL] = candidateTiming{
			observedAt: workflow.Now(p.ctx), searchAttempt: evidence.SearchAttempt, recovered: recovered,
		}
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

// logCandidatePhase emits one correlated stage measurement. Duration is the
// workflow-observed wall time, including Temporal queueing and activity
// retries. It is an operational latency signal, not an activity CPU timer.
func (p *pipeline) logCandidatePhase(tweetURL, phase, outcome string, startedAt time.Time, fields ...interface{}) {
	now := workflow.Now(p.ctx)
	timing := p.timings[tweetURL]
	base := []interface{}{
		"event_id", p.in.EventID,
		"fixture_id", p.in.FixtureID,
		"tweet_url", tweetURL,
		"search_attempt", timing.searchAttempt,
		"recovered", timing.recovered,
		"phase", phase,
		"outcome", outcome,
		"duration_ms", elapsedMilliseconds(startedAt, now),
		"event_elapsed_ms", elapsedMilliseconds(p.startedAt, now),
	}
	emitWorkflowMeasurement(p.log, vocabulary.ActionEventCandidateMeasured,
		"event candidate stage measured", append(base, fields...)...)
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
			p.logCandidatePhase(tweetURL, "download", "failed", p.timings[tweetURL].observedAt)
			p.failed++
			p.log.Warn("candidate download failed after retries", "tweet_url", tweetURL, "err", err)
			p.recordOutcome(tweetURL, discoveryactivity.OutcomeFailed, string(VideoFailureDownload), nil)
			return
		}
		if out.Outcome == videoactivity.OutcomeRejected {
			p.logCandidatePhase(tweetURL, "download", "rejected", p.timings[tweetURL].observedAt,
				"reason", out.RejectReason)
			p.rejectedClips++
			p.recordOutcome(tweetURL, discoveryactivity.OutcomeRejected, out.RejectReason, nil)
			return
		}
		if out.Outcome != videoactivity.OutcomePassed || out.MD5 == "" || out.StagingKey == "" {
			p.logCandidatePhase(tweetURL, "download", "invalid", p.timings[tweetURL].observedAt)
			p.failed++
			p.recordOutcome(tweetURL, discoveryactivity.OutcomeFailed,
				string(VideoFailureInvalidChildOutput),
				jsonDetail(map[string]any{"outcome": out.Outcome, "md5_present": out.MD5 != "", "staging_present": out.StagingKey != ""}))
			p.deleteStaging(out.StagingKey)
			return
		}
		p.logCandidatePhase(tweetURL, "download", "passed", p.timings[tweetURL].observedAt,
			"size_bytes", out.SizeBytes, "duration_media_ms", out.DurationMS)

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
	claim.startedAt = workflow.Now(p.ctx)
	fut := workflow.ExecuteActivity(p.hashCtx,
		(*videoactivity.Activities).HashVideo,
		videoactivity.HashVideoInput{StagingKey: claim.primary.stagingKey})
	p.inFlight++
	p.selector.AddFuture(fut, p.onHashDone(md5))
}

// onHashDone releases a successful claim to vision, closes every identical
// claimant on a deterministic content reject, or transfers an infrastructure-
// failed claim to the next exact-byte staging object. Only a candidate whose
// own hash attempt errors receives hash_error; untried waiters remain
// recoverable.
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
			p.logCandidatePhase(failed.tweetURL, "hash", "failed", claim.startedAt)
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
		if out.Outcome == videoactivity.OutcomeRejected {
			p.rejectHashClaim(md5, out.RejectReason, claim)
			return
		}
		if out.Outcome != "" && out.Outcome != videoactivity.OutcomePassed {
			p.failHashClaim(md5, string(VideoFailureInvalidChildOutput), claim)
			return
		}
		// A non-empty outcome is produced by the versioned FF-041 activity.
		// Enforce the workflow-side invariant too, while blank legacy activity
		// results retain their replay behavior.
		if out.Outcome == videoactivity.OutcomePassed && len(out.FrameHashes) < p.minRun {
			p.rejectHashClaim(md5, videoactivity.RejectInsufficientHashFrames, claim)
			return
		}

		winner := claim.primary
		p.logCandidatePhase(winner.tweetURL, "hash", "passed", claim.startedAt,
			"frame_count", len(out.FrameHashes))
		winner.hashVersion = dvideo.NormalizeFrameHashVersion(out.HashVersion)
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

// rejectHashClaim closes every byte-identical claimant on one deterministic
// hash rejection. Retrying another staging object cannot change the result for
// identical bytes, so no ffmpeg slot is spent on guaranteed duplicate work.
func (p *pipeline) rejectHashClaim(md5, reason string, claim *hashClaim) {
	clips := append([]clip{claim.primary}, claim.waiting...)
	for _, c := range clips {
		p.logCandidatePhase(c.tweetURL, "hash", "rejected", claim.startedAt,
			"reason", reason)
		p.rejectedClips++
		p.recordOutcome(c.tweetURL, discoveryactivity.OutcomeRejected, reason, nil)
		p.deleteStaging(c.stagingKey)
	}
	delete(p.hashing, md5)
}

// failHashClaim closes every claimant when a successful activity response is
// structurally invalid. This is an infrastructure/code failure, not a content
// verdict, and another copy of identical bytes cannot repair the response.
func (p *pipeline) failHashClaim(md5, reason string, claim *hashClaim) {
	clips := append([]clip{claim.primary}, claim.waiting...)
	for _, c := range clips {
		p.logCandidatePhase(c.tweetURL, "hash", "failed", claim.startedAt,
			"reason", reason)
		p.failed++
		p.recordOutcome(c.tweetURL, discoveryactivity.OutcomeFailed, reason, nil)
		p.deleteStaging(c.stagingKey)
	}
	delete(p.hashing, md5)
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
