// Placement activity tests keep validation attached to the staged variant across retries.
package video

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// placementValidation supplies one accepted evaluation of the input's exact bytes.
func placementValidation(in CommitClipPlacementInput) *dvision.Evidence {
	clock := "22:10"
	frames := []dvision.FrameObservation{{Soccer: true, Clock: &clock}, {Soccer: true}, {Soccer: true}}
	expected := dvision.Expected{Elapsed: 23}
	return &dvision.Evidence{ID: uuid.New(), EventID: in.EventID, FixtureID: in.FixtureID, MD5: in.MD5,
		Version: 1, EvaluatedAt: time.Now().UTC(), Evaluator: dvision.EvaluatorVersion,
		PromptSHA256: strings.Repeat("a", 64), SchemaSHA256: strings.Repeat("b", 64),
		Expected: expected, ToleranceMinutes: 1, FramePositions: []float64{1, 2, 3}, FrameQuality: 3,
		Frames: frames, Evaluation: dvision.Evaluate(frames, expected, 1)}
}

// TestPlacementTransfersOwnValidation proves both winners and first-loss variants
// reach the repository with the same evidence through cleanup-tail retries.
func TestPlacementTransfersOwnValidation(t *testing.T) {
	for _, newWinner := range []bool{true, false} {
		name := "loser"
		if newWinner {
			name = "winner"
		}
		t.Run(name, func(t *testing.T) {
			a, s3, assets, _ := newPersist()
			store := &fakePlacementStore{assets: assets}
			a.Placements = store
			in := stdPlacementInput(uuid.New())
			in.NewWinner = newWinner
			if !newWinner {
				in.WinnerAssetID = uuid.New()
				assets.byID[in.WinnerAssetID] = &dvideo.Asset{ID: in.WinnerAssetID, EventID: in.EventID, FixtureID: in.FixtureID}
			}
			in.Validation = placementValidation(in)
			in.ExtractedMinute = in.Validation.Evaluation.MatchedMinute
			s3.deleteFailures = 1
			_, err := a.CommitClipPlacement(t.Context(), in)
			require.ErrorContains(t, err, "delete staging")
			_, err = a.CommitClipPlacement(t.Context(), in)
			require.NoError(t, err)
			require.Len(t, store.inputs, 2)
			require.Len(t, s3.copies, 1)
			for _, placed := range store.inputs {
				require.Same(t, in.Validation, placed.Validation)
				require.Equal(t, uuid.NewSHA1(uuid.NameSpaceOID, []byte(in.EventID.String()+":"+in.MD5)), placed.ObservedAssetID)
				require.Equal(t, in.ExtractedMinute, placed.ExtractedMinute)
			}
		})
	}
}

// TestPlacementRejectsValidationScopeBeforeCopy prevents invalid proof from
// producing even a prepared destination object or a repository invocation.
func TestPlacementRejectsValidationScopeBeforeCopy(t *testing.T) {
	for _, mutate := range []func(*CommitClipPlacementInput){
		func(in *CommitClipPlacementInput) { in.CaptureVariant = false },
		func(in *CommitClipPlacementInput) { in.Validation.EventID = uuid.New() },
		func(in *CommitClipPlacementInput) { in.Validation.FixtureID++ },
		func(in *CommitClipPlacementInput) { in.Validation.MD5 = strings.Repeat("ff", 16) },
	} {
		a, s3, assets, _ := newPersist()
		store := &fakePlacementStore{assets: assets}
		a.Placements = store
		in := stdPlacementInput(uuid.New())
		in.Validation = placementValidation(in)
		mutate(&in)
		_, err := a.CommitClipPlacement(t.Context(), in)
		require.Error(t, err)
		require.Empty(t, s3.copies)
		require.Empty(t, store.inputs)
	}
}
