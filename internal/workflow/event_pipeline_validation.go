// Legacy-candidate compatibility, vision, and perceptual-dedup callbacks for EventWorkflow.
package workflow

import (
	"github.com/google/uuid"
	"go.temporal.io/sdk/workflow"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

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
			p.logCandidatePhase(fallbackTweetURL, "legacy_video", "failed", p.timings[fallbackTweetURL].observedAt)
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
		if tweetURL != fallbackTweetURL {
			p.timings[tweetURL] = p.timings[fallbackTweetURL]
		}
		p.logCandidatePhase(tweetURL, "legacy_video", string(out.Outcome), p.timings[tweetURL].observedAt)
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
			tweetURL: tweetURL, md5: out.MD5,
			hashVersion: dvideo.NormalizeFrameHashVersion(out.HashVersion), frameHashes: out.FrameHashes,
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
			p.collapseExact(c, idx, isAsset)
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
		if !dvideo.CompatibleFrameHashVersions(c.hashVersion, p.assets[i].hashVersion) {
			continue // different preprocessing/sample contracts — incomparable
		}
		primaryMatch := dvideo.Match(
			c.frameHashes, p.assets[i].frameHashes,
			p.maxHamming, p.minRun, p.maxGaps,
		)
		longMatch := p.longMinRun > 0 && dvideo.Match(
			c.frameHashes, p.assets[i].frameHashes,
			p.longMaxHamming, p.longMinRun, p.longMaxGaps,
		)
		if primaryMatch || longMatch {
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

// collapseExact merges a byte-identical candidate onto a promoted or pending
// representative. A promoted representative is already a winner, so the
// follower can become duplicate immediately. A pending representative has no
// winner yet: FF-065 retains the follower URL until the shared validation path
// reaches its terminal result. Older histories keep the original immediate
// duplicate command sequence. Loser bytes are dropped in both cases.
func (p *pipeline) collapseExact(loser clip, idx int, isAsset bool) {
	if p.canceled() {
		return
	}
	if isAsset {
		winnerID := p.assets[idx].assetID
		p.bumpPopularity(winnerID, loser.popularity)
		p.duplicateExactCluster(loser, winnerID)
	} else {
		p.pending[idx].popularity += loser.popularity
		if p.deferExactFollowerOutcomes {
			p.pending[idx].exactFollowers = append(p.pending[idx].exactFollowers, loser.tweetURL)
		} else {
			p.duplicates++
			p.recordOutcome(loser.tweetURL, discoveryactivity.OutcomeDuplicate, "", nil)
		}
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
	c.visionStartedAt = workflow.Now(p.ctx)
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
		// bumped the LIVE pending entry's popularity and attached exact followers
		// since. Re-read both before resolving the shared terminal result.
		if pc, ok := p.removePending(c.stagingKey); ok {
			c.popularity = pc.popularity
			c.exactFollowers = pc.exactFollowers
		}

		var vout visionactivity.ValidateClipOutput
		if err := f.Get(p.ctx, &vout); err != nil {
			p.logCandidatePhase(c.tweetURL, "vision", "failed", c.visionStartedAt)
			// Vision infra-fail after retries — drop the clip + its staging.
			p.failExactCluster(c, "vision_error", nil)
			p.deleteStaging(c.stagingKey)
			return
		}
		p.logCandidatePhase(c.tweetURL, "vision", vout.Outcome, c.visionStartedAt,
			"frame_count", len(vout.Frames))

		switch vout.Outcome {
		case string(dvision.OutcomeVerified), string(dvision.OutcomeUnverified):
			c.verified = vout.Outcome == string(dvision.OutcomeVerified)
			p.dedupAndPromote(c, vout)
		default: // rejected — not soccer / screen recording / wrong clock
			detail := map[string]any{
				"soccer_votes": vout.SoccerVotes, "screen_votes": vout.ScreenVotes,
				"frame_count": len(vout.Frames), "frame_observations": vout.Frames,
				"clock_readings": vout.ClockReadings,
			}
			if vout.DetectedMinute != nil { // clock was read but didn't match — record it so the reject is triageable (#181)
				detail["detected_minute"], detail["detected_period"] = *vout.DetectedMinute, vout.DetectedPeriod
				detail["expected_minute"], detail["expected_period"] = vout.ExpectedMinute, vout.ExpectedPeriod
			}
			p.rejectExactCluster(c, vout.Reason, jsonDetail(detail))
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
	winnerID := p.assets[best].assetID
	p.duplicateExactCluster(c, winnerID)
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
