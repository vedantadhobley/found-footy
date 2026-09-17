// Constructed boundary and visibility tests pin the experiment independently of production counts.
package main

import (
	"reflect"
	"testing"
	"time"

	vision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// observation builds explicit test data; absence is null, not fabricated digits.
func observation(clock, added, secondary string, period vision.Period) vision.FrameObservation {
	f := vision.FrameObservation{Soccer: true}
	if clock != "" {
		f.Clock = &clock
	}
	if added != "" {
		f.Added = &added
	}
	if secondary != "" {
		f.StoppageClock = &secondary
	}
	if period != "" {
		f.Period = &period
	}
	return f
}

// repeated isolates policy rules without claiming these constructed frames came from footage.
func repeated(f vision.FrameObservation) []vision.FrameObservation {
	return []vision.FrameObservation{f, f, f}
}

// TestFrozenAllowancesAndPeriods ensures neither five minutes nor the second half is special.
func TestFrozenAllowancesAndPeriods(t *testing.T) {
	for _, allowance := range []string{"+1", "+2", "+5", "+10"} {
		for _, c := range []struct {
			clock    string
			period   vision.Period
			boundary int
		}{
			{"45:00", vision.PeriodFirstHalf, 45}, {"90:00", vision.PeriodSecondHalf, 90},
			{"105:00", vision.PeriodExtraFirst, 105}, {"120:00", vision.PeriodExtraSecond, 120},
			{"45:00", vision.PeriodSecondHalf, 90}, {"15:00", vision.PeriodExtraFirst, 105},
			{"15:00", vision.PeriodExtraSecond, 120}, {"30:00", vision.PeriodExtraSecond, 120},
		} {
			t.Run(string(c.period)+c.clock+allowance, func(t *testing.T) {
				frames := repeated(observation(c.clock, allowance, "", c.period))
				exp := vision.Expected{Elapsed: c.boundary, Extra: 6}
				if before := vision.Evaluate(frames, exp, 1); before.Outcome != vision.OutcomeUnverified {
					t.Fatalf("baseline: %+v", before)
				}
				after, excluded := evaluateControl(frames, exp, 1, true)
				if after.Outcome != vision.OutcomeUnverified || after.MatchedMinute != nil || len(excluded) != 3 {
					t.Fatalf("after=%+v excluded=%v", after, excluded)
				}
			})
		}
	}
}

// TestBoundaryLowerBound prevents soft acceptance of a clearly too-late period boundary.
func TestBoundaryLowerBound(t *testing.T) {
	frames := repeated(observation("90:00", "+2", "", vision.PeriodSecondHalf))
	exp := vision.Expected{Elapsed: 46}
	naive, _ := evaluateControl(frames, exp, 1, false)
	bounded, _ := evaluateControl(frames, exp, 1, true)
	if naive.Outcome != vision.OutcomeUnverified || bounded.Outcome != vision.OutcomeRejected {
		t.Fatalf("naive=%+v bounded=%+v", naive, bounded)
	}
	// A normal-time ordinal 90 window reaches the boundary at tolerance one;
	// uncertain elapsed time is not sufficient to reject or verify that case.
	near, _ := evaluateControl(frames, vision.Expected{Elapsed: 90}, 1, true)
	if near.Outcome != vision.OutcomeUnverified {
		t.Fatalf("near=%+v", near)
	}
}

// TestUnchangedPaths keeps broader parser defects and valid timers out of this isolated policy.
func TestUnchangedPaths(t *testing.T) {
	for _, c := range []struct {
		name string
		f    vision.FrameObservation
		exp  vision.Expected
	}{
		{"real secondary", observation("90:00", "+5", "05:15", vision.PeriodSecondHalf), vision.Expected{Elapsed: 90, Extra: 6}},
		{"running allowance outside frozen control", observation("90:37", "+4", "", vision.PeriodSecondHalf), vision.Expected{Elapsed: 90, Extra: 4}},
		{"running secondary outside frozen control", observation("95:15", "+5", "05:00", vision.PeriodSecondHalf), vision.Expected{Elapsed: 90, Extra: 6}},
		{"wrong half", observation("90:00", "+5", "", vision.PeriodSecondHalf), vision.Expected{Elapsed: 45, Extra: 5}},
		{"bare minute not proof of zero seconds", observation("90", "+5", "", vision.PeriodSecondHalf), vision.Expected{Elapsed: 90, Extra: 5}},
		{"compact", observation("90+4", "", "", vision.PeriodSecondHalf), vision.Expected{Elapsed: 90, Extra: 5}},
		{"missing clock", observation("", "", "", ""), vision.Expected{Elapsed: 90, Extra: 5}},
		{"unknown API", observation("90:00", "+5", "", vision.PeriodSecondHalf), vision.Expected{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			frames := repeated(c.f)
			before := vision.Evaluate(frames, c.exp, 1)
			after, excluded := evaluateControl(frames, c.exp, 1, true)
			if len(excluded) != 0 || !reflect.DeepEqual(before, after) {
				t.Fatalf("changed unrelated path: before=%+v after=%+v", before, after)
			}
		})
	}
}

// TestMixedFrames keeps usable matches, decisive mismatches and content votes independent.
func TestMixedFrames(t *testing.T) {
	frozen := observation("90:00", "+5", "", vision.PeriodSecondHalf)
	missing := observation("", "", "", "")
	for _, c := range []struct {
		name   string
		frames []vision.FrameObservation
		want   vision.Outcome
	}{
		{"one exact match", []vision.FrameObservation{frozen, observation("95:15", "", "", vision.PeriodSecondHalf), missing}, vision.OutcomeVerified},
		{"contradictory exact", []vision.FrameObservation{frozen, observation("80:15", "", "", vision.PeriodSecondHalf), missing}, vision.OutcomeRejected},
		{"no other clock", []vision.FrameObservation{frozen, missing, missing}, vision.OutcomeUnverified},
		{"repeated exact", repeated(observation("95:15", "", "", vision.PeriodSecondHalf)), vision.OutcomeVerified},
		{"reverse replay", []vision.FrameObservation{observation("95:30", "", "", vision.PeriodSecondHalf), observation("95:15", "", "", vision.PeriodSecondHalf), observation("95:05", "", "", vision.PeriodSecondHalf)}, vision.OutcomeVerified},
	} {
		t.Run(c.name, func(t *testing.T) {
			original := append([]vision.FrameObservation(nil), c.frames...)
			after, _ := evaluateControl(c.frames, vision.Expected{Elapsed: 90, Extra: 6}, 1, true)
			if after.Outcome != c.want {
				t.Fatalf("after=%+v", after)
			}
			if !reflect.DeepEqual(original, c.frames) {
				t.Fatal("raw observations mutated")
			}
		})
	}
	for _, gate := range []string{"soccer", "screen"} {
		f := frozen
		if gate == "soccer" {
			f.Soccer = false
		} else {
			f.Screen = true
		}
		after, excluded := evaluateControl(repeated(f), vision.Expected{Elapsed: 90, Extra: 6}, 1, true)
		if after.Outcome != vision.OutcomeRejected || len(excluded) != 0 {
			t.Fatalf("content gate bypassed: %+v", after)
		}
	}
}

// TestPublicProjection pins singleton protection, category ranking and stable tie breaks.
func TestPublicProjection(t *testing.T) {
	clips := []row{{ID: "verified", EventID: "event", Verified: true, Popularity: 1}, {ID: "popular", EventID: "event", Popularity: 5}}
	before := publicRanks(clips)
	if before["verified"] != 1 || before["popular"] != 2 {
		t.Fatalf("before=%v", before)
	}
	clips[0].Verified = false
	after := publicRanks(clips)
	if after["verified"] != 0 || after["popular"] != 1 {
		t.Fatalf("after=%v", after)
	}
	clips = []row{{ID: "a", EventID: "event", Popularity: 2, FileSize: 10, At: time.Unix(1, 0)},
		{ID: "b", EventID: "event", Popularity: 2, FileSize: 10, At: time.Unix(1, 0)},
		{ID: "c", EventID: "event", Popularity: 2, FileSize: 20, At: time.Unix(2, 0)}}
	if ranks := publicRanks(clips); ranks["c"] != 1 || ranks["a"] != 2 || ranks["b"] != 3 {
		t.Fatalf("ties=%v", ranks)
	}
}
