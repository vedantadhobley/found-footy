// Tests for the wire-boundary enum parsers. Case-normalization,
// unknown-value preservation, and Subst prefix-parse are the load-
// bearing behaviors — regressions here would silently drop event
// tracking or misroute fixture states.
package apifootball_test

import (
	"encoding/json"
	"testing"

	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// ── APIStatusCode ──────────────────────────────────────────────

func TestParseAPIStatusCode_KnownAllCasings(t *testing.T) {
	// The vendor could theoretically drift casing on any of these.
	// Parse must accept all three canonical forms.
	cases := []struct {
		in   string
		want apifootball.APIStatusCode
	}{
		{"NS", apifootball.StatusNotStarted},
		{"ns", apifootball.StatusNotStarted},
		{"  Ns  ", apifootball.StatusNotStarted}, // whitespace + mixed case
		{"1H", apifootball.StatusFirstHalf},
		{"1h", apifootball.StatusFirstHalf},
		{"FT", apifootball.StatusFullTime},
		{"PST", apifootball.StatusPostponed},
		{"CANC", apifootball.StatusCancelled},
		{"LIVE", apifootball.StatusLive},
	}
	for _, tc := range cases {
		got, known, err := apifootball.ParseAPIStatusCode(tc.in)
		if err != nil {
			t.Errorf("ParseAPIStatusCode(%q): err = %v", tc.in, err)
			continue
		}
		if !known {
			t.Errorf("ParseAPIStatusCode(%q): known=false, want true", tc.in)
		}
		if got != tc.want {
			t.Errorf("ParseAPIStatusCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseAPIStatusCode_Unknown(t *testing.T) {
	// Vendor may add new codes. Preserve as-is, return known=false so
	// callers can log + continue.
	got, known, err := apifootball.ParseAPIStatusCode("FUTURE_CODE_X")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if known {
		t.Errorf("known=true, want false")
	}
	if got != apifootball.APIStatusCode("FUTURE_CODE_X") {
		t.Errorf("got %q, want raw preserved", got)
	}
}

func TestParseAPIStatusCode_Empty(t *testing.T) {
	got, known, err := apifootball.ParseAPIStatusCode("")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if known || got != "" {
		t.Errorf("got (%q, known=%t), want (\"\", false)", got, known)
	}
}

func TestAPIStatusCode_UnmarshalJSON(t *testing.T) {
	// JSON-boundary path: enum arrives already canonicalized.
	var v apifootball.APIStatusCode
	if err := json.Unmarshal([]byte(`"pst"`), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v != apifootball.StatusPostponed {
		t.Errorf("got %q, want %q", v, apifootball.StatusPostponed)
	}
}

// ── APIEventType ───────────────────────────────────────────────

func TestParseAPIEventType_KnownAllCasings(t *testing.T) {
	// Python's config uses `subst` lowercase; vendor doc uses `Subst`
	// title case. Parse must accept both.
	cases := []struct {
		in   string
		want apifootball.APIEventType
	}{
		{"Goal", apifootball.EventTypeGoal},
		{"goal", apifootball.EventTypeGoal},
		{"GOAL", apifootball.EventTypeGoal},
		{"Card", apifootball.EventTypeCard},
		{"Subst", apifootball.EventTypeSubst},
		{"subst", apifootball.EventTypeSubst}, // Python's convention
		{"Var", apifootball.EventTypeVar},
	}
	for _, tc := range cases {
		got, known, err := apifootball.ParseAPIEventType(tc.in)
		if err != nil {
			t.Errorf("ParseAPIEventType(%q): err = %v", tc.in, err)
			continue
		}
		if !known {
			t.Errorf("ParseAPIEventType(%q): known=false, want true", tc.in)
		}
		if got != tc.want {
			t.Errorf("ParseAPIEventType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseAPIEventType_Unknown(t *testing.T) {
	got, known, err := apifootball.ParseAPIEventType("Injury")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if known {
		t.Errorf("known=true, want false")
	}
	if got != apifootball.APIEventType("Injury") {
		t.Errorf("got %q, want raw preserved", got)
	}
}

// ── APIEventDetail ─────────────────────────────────────────────

func TestParseAPIEventDetail_KnownAllCasings(t *testing.T) {
	cases := []struct {
		in   string
		want apifootball.APIEventDetail
	}{
		// Goals
		{"Normal Goal", apifootball.DetailNormalGoal},
		{"normal goal", apifootball.DetailNormalGoal}, // load-bearing: the previous case-sensitivity bug
		{"NORMAL GOAL", apifootball.DetailNormalGoal},
		{"Own Goal", apifootball.DetailOwnGoal},
		{"Penalty", apifootball.DetailPenalty},
		{"Missed Penalty", apifootball.DetailMissedPenalty},
		// Cards — Red card has vendor's exact lowercase 'c'; both casings must parse
		{"Yellow Card", apifootball.DetailYellowCard},
		{"yellow card", apifootball.DetailYellowCard},
		{"Red card", apifootball.DetailRedCard},
		{"Red Card", apifootball.DetailRedCard}, // vendor variant OR our typo — either way, normalize
		{"RED CARD", apifootball.DetailRedCard},
		// VAR
		{"Goal cancelled", apifootball.DetailGoalCancelled},
		{"Penalty confirmed", apifootball.DetailPenaltyConfirmed},
	}
	for _, tc := range cases {
		got, known, err := apifootball.ParseAPIEventDetail(tc.in)
		if err != nil {
			t.Errorf("ParseAPIEventDetail(%q): err = %v", tc.in, err)
			continue
		}
		if !known {
			t.Errorf("ParseAPIEventDetail(%q): known=false, want true", tc.in)
		}
		if got != tc.want {
			t.Errorf("ParseAPIEventDetail(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestParseAPIEventDetail_SubstitutionPrefix — the vendor sends
// "Substitution 1", "Substitution 2", ..., indexed per team. Our
// canonical form drops the number; downstream only cares that it's
// a substitution.
func TestParseAPIEventDetail_SubstitutionPrefix(t *testing.T) {
	for _, in := range []string{"Substitution 1", "Substitution 2", "Substitution 27", "substitution 1"} {
		got, known, err := apifootball.ParseAPIEventDetail(in)
		if err != nil {
			t.Errorf("ParseAPIEventDetail(%q): err = %v", in, err)
			continue
		}
		if !known {
			t.Errorf("ParseAPIEventDetail(%q): known=false, want true (Subst is known)", in)
		}
		if got != apifootball.DetailSubstitution {
			t.Errorf("ParseAPIEventDetail(%q) = %q, want %q", in, got, apifootball.DetailSubstitution)
		}
	}
}

func TestParseAPIEventDetail_Unknown(t *testing.T) {
	got, known, err := apifootball.ParseAPIEventDetail("Injury Stoppage")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if known {
		t.Errorf("known=true, want false")
	}
	if got != apifootball.APIEventDetail("Injury Stoppage") {
		t.Errorf("got %q, want raw preserved", got)
	}
}
