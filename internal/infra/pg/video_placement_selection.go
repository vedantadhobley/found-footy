// Incoming placement and reversible selection share one event-locked commit and retry receipt.
package pg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
)

// placementSelectionReceipt binds the original accepted decision to its repair.
// Current state is deliberately not embedded: consumers reload it after retries.
type placementSelectionReceipt struct {
	video.SelectionResult
	Placement video.ClipPlacementResult
}

// placementSelectionDigest excludes preparation state and activity wall time.
// Retries may see later topology, but may never change the accepted input/policy.
func placementSelectionDigest(in video.ClipPlacement) (string, error) {
	if in.ObservedAssetID == uuid.Nil || (in.Winner != nil || in.Variant != nil) && in.Validation == nil {
		return "", fmt.Errorf("selection placement requires exact attribution and new-variant validation")
	}
	if in.Selection.EventID != in.EventID || in.Selection.FixtureID != in.FixtureID {
		return "", fmt.Errorf("selection placement scope mismatch")
	}
	if _, err := selectionRequestDigest(*in.Selection); err != nil {
		return "", err
	}
	request := *in.Selection
	request.SnapshotHash, request.PreparedAssetIDs = "", nil
	in.Selection = &request
	in.CommittedAt = time.Time{}
	if in.Winner != nil {
		copy := *in.Winner
		copy.FirstSeenAt = time.Time{}
		in.Winner = &copy
	}
	if in.Variant != nil {
		copy := *in.Variant
		copy.FirstSeenAt = time.Time{}
		in.Variant = &copy
	}
	body, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("selected-placement-v1:"), body...))
	return hex.EncodeToString(sum[:]), nil
}

// commitPlacementSelectionTx runs after the removed-event gate. A receipt is
// checked before placement because its old winner/loser pointers may now differ.
func commitPlacementSelectionTx(ctx context.Context, tx pgx.Tx, in video.ClipPlacement) (video.ClipPlacementResult, error) {
	var out video.ClipPlacementResult
	digest, err := placementSelectionDigest(in)
	if err != nil {
		return out, err
	}
	request := in.Selection
	var priorDigest string
	var body []byte
	err = tx.QueryRow(ctx, `SELECT request_hash,result FROM video_selection_commits WHERE id=$1`, request.ID).Scan(&priorDigest, &body)
	if err == nil {
		if digest != priorDigest {
			return out, fmt.Errorf("selection placement ID reused with different content")
		}
		var receipt placementSelectionReceipt
		if err := json.Unmarshal(body, &receipt); err != nil {
			return out, err
		}
		receipt.Replayed = true
		out = receipt.Placement
		out.Selection = &receipt.SelectionResult
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	before, err := loadSelectionTx(ctx, tx, in.EventID, true)
	if err != nil {
		return out, err
	}
	fingerprint, err := before.Fingerprint()
	if err != nil {
		return out, err
	}
	if fingerprint != request.SnapshotHash {
		return out, video.ErrSelectionStale
	}
	// Retention revokes shares before deleting bytes. Never mint another share
	// into an already revoked media set, including a never-public hidden node.
	for _, node := range before.Nodes {
		if node.Asset.SupersededBy == nil && (node.Asset.ObjectReclaimedAt != nil || node.Share == nil || node.Share.State != video.ShareStateActive) {
			return out, video.ErrSelectionMedia
		}
	}
	out, err = applyClipPlacementTx(ctx, tx, in)
	if err != nil {
		return out, err
	}
	if out.ObservedAssetCreated && !slices.Contains(request.PreparedAssetIDs, in.ObservedAssetID) {
		return out, video.ErrSelectionMedia
	}
	after, err := loadSelectionTx(ctx, tx, in.EventID, true)
	if err != nil {
		return out, err
	}
	selected := video.SelectionResult{}
	selected.Plan, err = video.PlanSelection(after, request.Policy, request.PreparedAssetIDs)
	if errors.Is(err, video.ErrSelectionCredits) {
		// Missing old attribution must not strand an otherwise accepted source.
		// Preserve ordinary placement; record that no graph repair was applied.
		selected.Plan = video.SelectionPlan{Version: video.SelectionVersion}
		selected.Skipped = video.SelectionIncompleteCredits
	} else if err != nil {
		return out, err
	} else {
		selected.ShareIDs, err = applySelectionTx(ctx, tx, after, selected.Plan)
		if err != nil {
			return out, err
		}
	}
	oldState, err := selectionBeforeJSON(before)
	if err != nil {
		return out, err
	}
	body, err = json.Marshal(placementSelectionReceipt{SelectionResult: selected, Placement: out})
	if err != nil {
		return out, err
	}
	policy, err := json.Marshal(request.Policy)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO video_selection_commits
		(id,event_id,fixture_id,request_hash,snapshot_hash,policy,before_state,result)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, request.ID, in.EventID, in.FixtureID, digest, fingerprint, policy, oldState, body)
	out.Selection = &selected
	return out, err
}
