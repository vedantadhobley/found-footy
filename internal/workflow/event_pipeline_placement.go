// Atomic accepted-candidate placement for new EventWorkflow histories.
package workflow

import (
	"fmt"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	ddiscovery "github.com/vedantadhobley/found-footy/internal/domain/discovery"
	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// dedupAndCommit applies the same category-scoped matcher and keeper policy as
// the compatibility path, but commits the complete accepted result once.
func (p *pipeline) dedupAndCommit(c clip, vout visionactivity.ValidateClipOutput) {
	matched := p.matchAssets(c)
	if len(matched) == 0 {
		out, ok := p.commitClipPlacement(c, vout, true, uuid.Nil, nil)
		if !ok {
			return
		}
		c.assetID = out.WinnerAssetID
		p.assets = append(p.assets, c)
		if c.verified {
			p.verified++
		} else {
			p.unverified++
		}
		p.duplicates += len(c.exactFollowers)
		return
	}

	best := matched[0]
	for _, idx := range matched[1:] {
		if dvideo.IsUpgrade(p.assets[idx].quality(), p.assets[best].quality()) {
			best = idx
		}
	}

	if dvideo.IsUpgrade(c.quality(), p.assets[best].quality()) {
		loserIDs := p.assetIDsAt(matched)
		out, ok := p.commitClipPlacement(c, vout, true, uuid.Nil, loserIDs)
		if !ok {
			return
		}
		for _, idx := range matched {
			c.popularity += p.assets[idx].popularity
		}
		p.dropAssets(loserIDs)
		c.assetID = out.WinnerAssetID
		p.assets = append(p.assets, c)
		if c.verified {
			p.verified++
		} else {
			p.unverified++
		}
		p.duplicates += len(c.exactFollowers)
		p.superseded += len(loserIDs)
		return
	}

	winnerID := p.assets[best].assetID
	var loserIDs []uuid.UUID
	for _, idx := range matched {
		if idx != best {
			loserIDs = append(loserIDs, p.assets[idx].assetID)
		}
	}
	if _, ok := p.commitClipPlacement(c, vout, false, winnerID, loserIDs); !ok {
		return
	}
	popularity := 1 + len(c.exactFollowers)
	for _, idx := range matched {
		if idx != best {
			popularity += p.assets[idx].popularity
		}
	}
	p.assets[best].popularity += popularity
	p.dropAssets(loserIDs)
	p.duplicates += 1 + len(c.exactFollowers)
	p.superseded += len(loserIDs)
}

// commitClipPlacement blocks on the single durable placement activity. A
// successful return means every candidate is terminal and attributed, the
// public clip set/ranking inputs are committed, and staging cleanup completed.
func (p *pipeline) commitClipPlacement(
	c clip,
	vout visionactivity.ValidateClipOutput,
	newWinner bool,
	winnerID uuid.UUID,
	loserIDs []uuid.UUID,
) (videoactivity.CommitClipPlacementOutput, bool) {
	var out videoactivity.CommitClipPlacementOutput
	if p.canceled() {
		return out, false
	}
	candidates, ok := p.placementCandidates(c, newWinner, winnerID)
	if !ok {
		return out, false
	}
	startedAt := workflow.Now(p.ctx)
	err := workflow.ExecuteActivity(p.persistCtx,
		(*videoactivity.PersistActivities).CommitClipPlacement,
		videoactivity.CommitClipPlacementInput{
			EventID: p.in.EventID, FixtureID: p.in.FixtureID,
			NewWinner: newWinner, WinnerAssetID: winnerID,
			LoserAssetIDs: loserIDs, Candidates: candidates,
			StagingKey: c.stagingKey, MD5: c.md5,
			HashVersion: c.hashVersion, FrameHashes: c.frameHashes,
			Width: c.width, Height: c.height, DurationMS: c.durationMS,
			FileSizeBytes: c.fileSizeBytes, Bitrate: c.bitrate,
			FrameRate: p.persistedFrameRate(c),
			Verified:  c.verified, ExtractedMinute: vout.MatchedMinute,
		}).Get(p.persistCtx, &out)
	if err != nil {
		p.logCandidatePhase(c.tweetURL, "placement", "failed", startedAt)
		if temporal.IsCanceledError(err) {
			return out, false
		}
		p.setTerminalError(fmt.Errorf("commit clip placement for %s: %w", c.tweetURL, err))
		return out, false
	}
	placementState := "passed"
	if out.EventRemoved {
		placementState = "discarded"
	}
	p.logCandidatePhase(c.tweetURL, "placement", placementState, startedAt,
		"asset_id", out.WinnerAssetID.String(), "winner_created", out.WinnerCreated,
		"losers", len(loserIDs), "candidate_count", len(candidates),
		"event_removed", out.EventRemoved)
	for _, candidate := range candidates {
		ownership := p.candidates[candidate.Evidence.TweetURL]
		ownership.state = ddiscovery.CandidateTerminal
		p.candidates[candidate.Evidence.TweetURL] = ownership
		candidateOutcome := string(candidate.Outcome)
		if out.EventRemoved {
			candidateOutcome = string(discoverycontract.OutcomeRejected)
		}
		p.logCandidatePhase(candidate.Evidence.TweetURL, "terminal_persist", "passed", startedAt,
			"candidate_outcome", candidateOutcome)
	}
	if out.EventRemoved {
		return out, false
	}
	p.rememberExactRoot(c.md5, out.WinnerAssetID)
	p.redirectExactRoots(loserIDs, out.WinnerAssetID)
	if out.Announce {
		p.publishEventVideo(c.tweetURL, "placement")
	}
	return out, true
}

// persistedFrameRate keeps changed activity payloads behind a Temporal
// version marker. Existing histories retain their original command sequence;
// new histories durably carry the already-probed cadence metadata.
func (p *pipeline) persistedFrameRate(c clip) *float64 {
	if !p.cadenceMetadata {
		return nil
	}
	return c.frameRate
}

func (p *pipeline) placementCandidates(c clip, newWinner bool, winnerID uuid.UUID) ([]videoactivity.PlacementCandidateInput, bool) {
	urls := make([]string, 0, 1+len(c.exactFollowers))
	urls = append(urls, c.tweetURL)
	urls = append(urls, c.exactFollowers...)
	if c.popularity != len(urls) {
		p.setTerminalError(fmt.Errorf("candidate cluster popularity %d does not match %d owned URLs", c.popularity, len(urls)))
		return nil, false
	}
	if newWinner {
		winnerID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(p.in.EventID.String()+":"+c.md5))
	}
	out := make([]videoactivity.PlacementCandidateInput, 0, len(urls))
	for i, tweetURL := range urls {
		ownership, exists := p.candidates[tweetURL]
		if !exists {
			p.setTerminalError(fmt.Errorf("candidate evidence missing for placement %s", tweetURL))
			return nil, false
		}
		outcome := discoverycontract.OutcomeDuplicate
		detail := jsonDetail(map[string]any{"winner_asset_id": winnerID.String()})
		if newWinner && i == 0 {
			outcome = discoverycontract.OutcomePromoted
			detail = jsonDetail(map[string]any{"asset_id": winnerID.String(), "verified": c.verified})
		}
		out = append(out, videoactivity.PlacementCandidateInput{
			Evidence: ownership.evidence, Outcome: outcome, Detail: detail,
		})
	}
	return out, true
}

func (p *pipeline) dropAssets(ids []uuid.UUID) {
	if len(ids) == 0 {
		return
	}
	drop := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		drop[id] = struct{}{}
	}
	kept := p.assets[:0]
	for _, asset := range p.assets {
		if _, found := drop[asset.assetID]; !found {
			kept = append(kept, asset)
		}
	}
	p.assets = kept
}
