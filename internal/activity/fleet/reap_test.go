// reap_test.go ensures unavailable ownership evidence can never authorize deletion.
package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/vedantadhobley/found-footy/internal/infra/firefoxfleet"
)

// failingLeaseSource records whether ownership lookup was attempted.
type failingLeaseSource struct {
	err    error
	called bool
}

// ListLiveFleetEventIDs exposes a controlled failure without a database or daemon.
func (s *failingLeaseSource) ListLiveFleetEventIDs(context.Context) ([]uuid.UUID, error) {
	s.called = true
	return nil, s.err
}

// TestReapOwnershipUnavailable refuses to turn absent evidence into an empty keep set.
func TestReapOwnershipUnavailable(t *testing.T) {
	// The zero Fleet has no Docker client. Any accidental daemon access panics.
	a := &Activities{Fleet: &firefoxfleet.Fleet{}}
	if _, err := a.ReapOrphanedFirefox(context.Background(), ReapOrphanedFirefoxInput{MinAgeSecs: 120}); err == nil || !strings.Contains(err.Error(), "missing ownership repository") {
		t.Fatalf("missing repository: %v", err)
	}
	wantErr := errors.New("database unavailable")
	source := &failingLeaseSource{err: wantErr}
	a.LiveEvents = source
	if _, err := a.ReapOrphanedFirefox(context.Background(), ReapOrphanedFirefoxInput{MinAgeSecs: 120}); !errors.Is(err, wantErr) || !source.called {
		t.Fatalf("lookup failure: %v, called=%v", err, source.called)
	}
}

// TestReapDisabledFleet needs neither ownership lookup nor a Docker client.
func TestReapDisabledFleet(t *testing.T) {
	source := &failingLeaseSource{err: errors.New("must not query")}
	a := &Activities{LiveEvents: source}
	out, err := a.ReapOrphanedFirefox(context.Background(), ReapOrphanedFirefoxInput{MinAgeSecs: 120})
	if err != nil || len(out.Reaped) != 0 || source.called {
		t.Fatalf("disabled reaper: output=%v error=%v lookup=%v", out, err, source.called)
	}
}
