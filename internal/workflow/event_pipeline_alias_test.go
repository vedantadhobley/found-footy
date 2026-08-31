// event_pipeline_alias_test.go — Versioned canonical-alias and cadence state tests.
package workflow

import (
	"testing"

	"github.com/google/uuid"
)

func TestPipelineRedirectExactRootsMovesEveryLoserVariant(t *testing.T) {
	aID, bID, winnerID := uuid.New(), uuid.New(), uuid.New()
	p := &pipeline{
		canonicalExactAliases: true,
		exactRoots: map[string]uuid.UUID{
			"a-primary": aID,
			"a-retired": aID,
			"b-primary": bID,
		},
		assets: []clip{{md5: "winner", assetID: winnerID}},
	}

	p.redirectExactRoots([]uuid.UUID{aID, bID}, winnerID)

	for md5, got := range p.exactRoots {
		if got != winnerID {
			t.Errorf("exact root %q = %s, want %s", md5, got, winnerID)
		}
	}
	idx, isAsset, matched := p.matchMD5(clip{md5: "a-retired"})
	if !matched || !isAsset || idx != 0 {
		t.Fatalf("matchMD5 = (%d, %v, %v), want live winner index", idx, isAsset, matched)
	}
}

func TestPipelinePersistedFrameRateIsVersionGated(t *testing.T) {
	fps := 59.94
	candidate := clip{frameRate: &fps}

	legacy := pipeline{}
	if got := legacy.persistedFrameRate(candidate); got != nil {
		t.Fatalf("legacy frame rate = %v, want nil", *got)
	}

	current := pipeline{cadenceMetadata: true}
	if got := current.persistedFrameRate(candidate); got == nil || *got != fps {
		t.Fatalf("current frame rate = %v, want %v", got, fps)
	}
}
