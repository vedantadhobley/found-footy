// Atomic reversible selection preserves URLs, direct support and prior topology under the event lock.
package pg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
)

// CommitSelection rechecks the prepared snapshot under the shared event lock.
// It is an internal repository boundary, not a new workflow or external endpoint.
func (r *PlacementRepo) CommitSelection(ctx context.Context, request video.SelectionRequest) (video.SelectionResult, error) {
	var result video.SelectionResult
	digest, err := selectionRequestDigest(request)
	if err != nil {
		return result, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var removed bool
	if err := tx.QueryRow(ctx, `SELECT removed FROM events WHERE id=$1 AND fixture_id=$2 FOR UPDATE`, request.EventID, request.FixtureID).Scan(&removed); err != nil {
		return result, err
	}
	if removed {
		return result, video.ErrSelectionRemoved
	}
	var priorDigest string
	var body []byte
	err = tx.QueryRow(ctx, `SELECT request_hash,result FROM video_selection_commits WHERE id=$1`, request.ID).Scan(&priorDigest, &body)
	if err == nil {
		if priorDigest != digest {
			return result, fmt.Errorf("selection request ID reused with different content")
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return result, err
		}
		result.Replayed = true
		return result, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	snapshot, err := loadSelectionTx(ctx, tx, request.EventID, true)
	if err != nil {
		return result, err
	}
	fingerprint, err := snapshot.Fingerprint()
	if err != nil {
		return result, err
	}
	if fingerprint != request.SnapshotHash {
		return result, video.ErrSelectionStale
	}
	result.Plan, err = video.PlanSelection(snapshot, request.Policy, request.PreparedAssetIDs)
	if err != nil {
		return result, err
	}
	result.ShareIDs, err = applySelectionTx(ctx, tx, snapshot, result.Plan)
	if err != nil {
		return result, err
	}
	before, err := selectionBeforeJSON(snapshot)
	if err != nil {
		return result, err
	}
	body, err = json.Marshal(result)
	if err != nil {
		return result, err
	}
	policy, err := json.Marshal(request.Policy)
	if err != nil {
		return result, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO video_selection_commits
		(id,event_id,fixture_id,request_hash,snapshot_hash,policy,before_state,result)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, request.ID, request.EventID, request.FixtureID, digest, fingerprint, policy, before, body)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

// selectionRequestDigest binds retries to their policy, scope, snapshot and prepared assets.
func selectionRequestDigest(r video.SelectionRequest) (string, error) {
	decoded, err := hex.DecodeString(r.SnapshotHash)
	if r.ID == uuid.Nil || r.EventID == uuid.Nil || r.FixtureID <= 0 || err != nil || len(decoded) != 32 {
		return "", fmt.Errorf("incomplete selection request")
	}
	if err := r.Policy.Validate(); err != nil {
		return "", err
	}
	r.PreparedAssetIDs = slices.Clone(r.PreparedAssetIDs)
	sort.Slice(r.PreparedAssetIDs, func(i, j int) bool { return r.PreparedAssetIDs[i].String() < r.PreparedAssetIDs[j].String() })
	r.PreparedAssetIDs = slices.Compact(r.PreparedAssetIDs)
	body, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

// applySelectionTx applies standalone or incoming-placement selection atomically.
// Its caller must hold the event and snapshot row locks and use
// the domain planner's result, not an untrusted externally supplied plan.
func applySelectionTx(ctx context.Context, tx pgx.Tx, snapshot video.SelectionSnapshot, plan video.SelectionPlan) (map[uuid.UUID]string, error) {
	shares := make(map[uuid.UUID]string)
	nodes := make(map[uuid.UUID]video.SelectionNode)
	for _, node := range snapshot.Nodes {
		nodes[node.Asset.ID] = node
	}
	for _, owner := range plan.Owners {
		var successor *uuid.UUID
		if owner.AssetID != owner.KeeperID {
			successor = &owner.KeeperID
		}
		if _, err := tx.Exec(ctx, `UPDATE video_assets SET superseded_by=$3
			WHERE event_id=$1 AND id=$2 AND superseded_by IS DISTINCT FROM $3`, snapshot.EventID, owner.AssetID, successor); err != nil {
			return nil, err
		}
		// Outcome/detail/time describe the original placement. Only the current
		// credit pointer moves; the immutable selection receipt records why.
		if _, err := tx.Exec(ctx, `UPDATE event_search_candidates SET credited_asset_id=$3
			WHERE event_id=$1 AND observed_asset_id=$2 AND credited_asset_id IS DISTINCT FROM $3
			AND outcome_class IN ('promoted','duplicate','superseded')`, snapshot.EventID, owner.AssetID, owner.KeeperID); err != nil {
			return nil, err
		}
	}
	var rank int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(rank),0) FROM video_shares WHERE event_id=$1 AND state='active'`, snapshot.EventID).Scan(&rank); err != nil {
		return nil, err
	}
	for _, selected := range plan.Selected {
		if _, err := tx.Exec(ctx, `UPDATE video_assets SET popularity=$3 WHERE event_id=$1 AND id=$2 AND popularity IS DISTINCT FROM $3`, snapshot.EventID, selected.AssetID, selected.Popularity); err != nil {
			return nil, err
		}
		node := nodes[selected.AssetID]
		if node.Share != nil && node.Share.State == video.ShareStateActive {
			shares[selected.AssetID] = node.Share.ID
			continue
		}
		rank++
		if node.Share != nil {
			if node.Share.State != video.ShareStateSuperseded {
				return nil, fmt.Errorf("cannot restore removed share")
			}
			if _, err := tx.Exec(ctx, `UPDATE video_shares SET state='active',rank=$3
				WHERE event_id=$1 AND asset_id=$2 AND state='superseded'`, snapshot.EventID, selected.AssetID, rank); err != nil {
				return nil, err
			}
			shares[selected.AssetID] = node.Share.ID
			continue
		}
		share, err := video.NewShare(selected.AssetID, snapshot.EventID, selected.Verified, selected.ExtractedMinute, rank, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO video_shares (id,asset_id,event_id,timestamp_verified,extracted_minute,state,rank,created_at)
			VALUES($1,$2,$3,$4,$5,'active',$6,$7)`, share.ID, share.AssetID, share.EventID, share.TimestampVerified, share.ExtractedMinute, share.Rank, share.CreatedAt); err != nil {
			return nil, err
		}
		shares[selected.AssetID] = share.ID
	}
	return shares, nil
}

// selectionBeforeJSON preserves prior edges, counts and share/evaluation identity
// without duplicating dense hashes, video bytes or model observations in receipts.
func selectionBeforeJSON(s video.SelectionSnapshot) ([]byte, error) {
	type beforeNode struct {
		AssetID                  uuid.UUID
		SupersededBy             *uuid.UUID
		Popularity, ExactSources int
		Share                    *video.Share
		ValidationID             *uuid.UUID
	}
	counts := make(map[uuid.UUID]int)
	for _, source := range s.Sources {
		counts[source.ObservedAssetID]++
	}
	before := make([]beforeNode, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		row := beforeNode{AssetID: n.Asset.ID, SupersededBy: n.Asset.SupersededBy, Popularity: n.Asset.Popularity, ExactSources: counts[n.Asset.ID], Share: n.Share}
		if n.Validation != nil {
			row.ValidationID = &n.Validation.ID
		}
		before = append(before, row)
	}
	return json.Marshal(before)
}

var _ video.SelectionRepo = (*PlacementRepo)(nil)
