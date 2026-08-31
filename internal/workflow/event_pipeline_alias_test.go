// event_pipeline_alias_test.go — FF-080 canonical exact-variant state tests.
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
