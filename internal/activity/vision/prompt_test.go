// Prompt provenance tests bind the effective request to its retained validation evidence.
package vision

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// TestValidateClipUsesAndFingerprintsEffectivePrompt protects both the default and operator override.
func TestValidateClipUsesAndFingerprintsEffectivePrompt(t *testing.T) {
	for _, override := range []string{"", "explicit prompt override"} {
		t.Run(override, func(t *testing.T) {
			response := framesJSON(
				map[string]any{"soccer": true, "clock": "46:35", "added": "+4"},
				map[string]any{"soccer": true, "clock": "46:37", "added": "+4"},
				map[string]any{"soccer": true, "clock": "46:39", "added": "+4"},
			)
			a, _, model := newActivities(t, response, nil)
			a.Cfg.Prompt = override
			out, err := a.ValidateClip(context.Background(), ValidateClipInput{
				EventID: uuid.New(), FixtureID: 1, StagingKey: "clip", APIElapsed: 45, APIExtra: 2,
				CaptureEvidence: true, MD5: strings.Repeat("ab", 16),
			})
			require.NoError(t, err)
			want := override
			if want == "" {
				want = dvision.DefaultPrompt
			}
			require.Len(t, model.lastReq.Messages, 1)
			require.Equal(t, want, model.lastReq.Messages[0].Content)
			require.NotNil(t, out.Evidence)
			require.Equal(t, contractDigest(want), out.Evidence.PromptSHA256)
			require.Equal(t, contractDigest(string(dvision.ResponseSchema)), out.Evidence.SchemaSHA256)
			require.Equal(t, "vision-evaluate-v5", out.Evidence.Evaluator)
			require.NotNil(t, model.lastReq.Temperature)
			require.Zero(t, *model.lastReq.Temperature)
		})
	}
}
