// Running-clock allowance evidence corrects period inference without adding elapsed time.
package vision

// runningStoppageRecognitionMinutes bounds unlabelled period inference, not
// actual stoppage duration. Fifteen minutes keeps adjacent ET recognition
// windows disjoint and avoids relabelling ordinary later-half clocks when an
// allowance is announced before the next boundary. Explicit period labels
// retain longer stoppage readings; the announced allowance is never a ceiling.
const runningStoppageRecognitionMinutes = 15

// withRunningStoppage recognizes a positive allowance beside an unlabelled
// continuous clock just beyond an end boundary. It never changes the minute,
// invents a relative offset, overrides explicit evidence, or consults API time.
// Exact end-boundary clocks remain owned by frozenBoundary composition.
func withRunningStoppage(r ClockReading, added *string) ClockReading {
	if added == nil || r.PeriodPinned || r.Ambiguous || r.Stoppage {
		return r
	}
	allowance, ok := parseAddedField(*added)
	if !ok || allowance <= 0 {
		return r
	}
	for _, end := range []int{45, 90, 105, 120} {
		if r.Minute > end && r.Minute < end+runningStoppageRecognitionMinutes {
			r.Period = periodOf(end)
			r.PeriodPinned = true
			r.Stoppage = true
			return r
		}
	}
	return r
}
