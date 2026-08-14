// signals.go — change-detection helpers for the N4 reconcile classification.
// ReconcileFixture snapshots the API-mutable fields before the Update* calls
// and diffs after; these compare the nil-able pointers so the poll workflow can
// partition each fixture into fixture.clock (minute moved) vs fixture.update
// (something structural changed). See decisions.md 2026-08-14.
package monitor

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

// derefInt returns *p, or 0 if p is nil.
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
