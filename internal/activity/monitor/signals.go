// signals.go — typed live-feed routing and change-detection helpers.
package monitor

import "time"

// FixtureFeedAction is the one valid publication decision for a reconciled
// fixture. The zero value means no live-feed work.
type FixtureFeedAction string

const (
	FixtureFeedNone   FixtureFeedAction = ""
	FixtureFeedStatus FixtureFeedAction = "status"
	FixtureFeedUpdate FixtureFeedAction = "update"
)

// markStatus selects the inline route unless a snapshot refresh already
// won. Reconcile may discover stronger evidence after classifying the status.
func (out *ReconcileFixtureOutput) markStatus() {
	if out.FeedAction != FixtureFeedUpdate {
		out.FeedAction = FixtureFeedStatus
	}
}

// markUpdate selects the authoritative snapshot route. It always wins over an
// earlier inline-presentation change from the same provider observation.
func (out *ReconcileFixtureOutput) markUpdate() {
	out.FeedAction = FixtureFeedUpdate
}

// intPtrChanged reports whether two *int differ in presence or value. Both nil →
// unchanged; exactly one nil → changed; both set → value compare.
func intPtrChanged(a, b *int) bool {
	switch {
	case a == nil && b == nil:
		return false
	case a == nil || b == nil:
		return true
	default:
		return *a != *b
	}
}

// boolPtrChanged reports whether two *bool differ in presence or value.
func boolPtrChanged(a, b *bool) bool {
	switch {
	case a == nil && b == nil:
		return false
	case a == nil || b == nil:
		return true
	default:
		return *a != *b
	}
}

// timePtrChanged compares nullable timestamps by instant.
func timePtrChanged(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a != b
	}
	return !a.Equal(*b)
}
