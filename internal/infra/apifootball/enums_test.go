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
	got, known, err := apifootball.ParseAPIStatusCode("future_code_x")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if known {
		t.Errorf("known=true, want false")
	}
	if got != apifootball.APIStatusCode("future_code_x") {
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
	got, known, err := apifootball.ParseAPIEventType("injury")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if known {
		t.Errorf("known=true, want false")
	}
	if got != apifootball.APIEventType("injury") {
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
	got, known, err := apifootball.ParseAPIEventDetail("injury stoppage")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if known {
		t.Errorf("known=true, want false")
	}
	if got != apifootball.APIEventDetail("injury stoppage") {
		t.Errorf("got %q, want raw preserved", got)
	}
}

// TestParseAPIEventDetail_RealVendorRedCardCasing — vendor DOC says
// "Red card" (lowercase c) but LIVE emission observed as "Red Card"
// (title case). Parse must accept both; canonical matches real
// emission per decisions.md 2026-07-09 real-data audit.
func TestParseAPIEventDetail_RealVendorRedCardCasing(t *testing.T) {
	for _, in := range []string{"Red Card", "Red card", "RED CARD", "red card"} {
		got, known, err := apifootball.ParseAPIEventDetail(in)
		if err != nil || !known {
			t.Errorf("ParseAPIEventDetail(%q): err=%v known=%v", in, err, known)
			continue
		}
		if got != apifootball.DetailRedCard {
			t.Errorf("ParseAPIEventDetail(%q) = %q, want %q", in, got, apifootball.DetailRedCard)
		}
	}
	// Canonical is lowercase per the 2026-07-09 lowercase-canonical
	// policy — real vendor emission uses "Red Card" but we normalize
	// to a uniform lowercase internal representation regardless of
	// vendor casing drift.
	if string(apifootball.DetailRedCard) != "red card" {
		t.Errorf("DetailRedCard canonical = %q, want %q", apifootball.DetailRedCard, "red card")
	}
}

// ── APICardComment ─────────────────────────────────────────────

func TestParseAPICardComment_KnownAllCasings(t *testing.T) {
	// Real vendor emission observed 2026-07-09 across WC + club fixtures.
	cases := []struct {
		in   string
		want apifootball.APICardComment
	}{
		{"Foul", apifootball.CardCommentFoul},
		{"foul", apifootball.CardCommentFoul},
		{"FOUL", apifootball.CardCommentFoul},
		{"  Foul  ", apifootball.CardCommentFoul}, // whitespace
		{"Argument", apifootball.CardCommentArgument},
		{"Roughing", apifootball.CardCommentRoughing},
		{"Unsportsmanlike conduct", apifootball.CardCommentUnsportsmanlikeConduct},
		{"unsportsmanlike CONDUCT", apifootball.CardCommentUnsportsmanlikeConduct},
		{"Serious foul", apifootball.CardCommentSeriousFoul},
	}
	for _, tc := range cases {
		got, known, err := apifootball.ParseAPICardComment(tc.in)
		if err != nil {
			t.Errorf("ParseAPICardComment(%q): err = %v", tc.in, err)
			continue
		}
		if !known {
			t.Errorf("ParseAPICardComment(%q): known=false, want true", tc.in)
		}
		if got != tc.want {
			t.Errorf("ParseAPICardComment(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseAPICardComment_EmptyIsLegal(t *testing.T) {
	// Vendor emits nil/empty for cards without a documented reason.
	// Not an error — just no known enum value.
	got, known, err := apifootball.ParseAPICardComment("")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if known || got != "" {
		t.Errorf("got (%q, known=%t), want (\"\", false)", got, known)
	}
}

func TestParseAPICardComment_Unknown(t *testing.T) {
	got, known, err := apifootball.ParseAPICardComment("handball")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if known {
		t.Errorf("known=true, want false")
	}
	if got != apifootball.APICardComment("handball") {
		t.Errorf("got %q, want raw preserved", got)
	}
}

// ── APIGoalComment ─────────────────────────────────────────────

func TestParseAPIGoalComment_PenaltyShootout(t *testing.T) {
	// Real vendor emission on shootout goals (verified against WC
	// fixture 1576805, 2026-07-07).
	for _, in := range []string{"Penalty Shootout", "penalty shootout", "PENALTY SHOOTOUT"} {
		got, known, err := apifootball.ParseAPIGoalComment(in)
		if err != nil || !known {
			t.Errorf("ParseAPIGoalComment(%q): err=%v known=%v", in, err, known)
			continue
		}
		if got != apifootball.GoalCommentPenaltyShootout {
			t.Errorf("ParseAPIGoalComment(%q) = %q, want %q", in, got, apifootball.GoalCommentPenaltyShootout)
		}
	}
}

func TestHasPenaltyShootoutComment(t *testing.T) {
	cases := []struct {
		comments string
		want     bool
	}{
		{"Penalty Shootout", true},          // exact match, canonical
		{"penalty shootout", true},          // lowercased
		{"PENALTY SHOOTOUT", true},          // upper
		{"contains Penalty Shootout text", true}, // substring — future-proof
		{"", false},                         // empty
		{"Foul", false},                     // wrong marker
		{"Normal Goal", false},              // detail leaking in
	}
	for _, tc := range cases {
		got := apifootball.HasPenaltyShootoutComment(tc.comments)
		if got != tc.want {
			t.Errorf("HasPenaltyShootoutComment(%q) = %v, want %v", tc.comments, got, tc.want)
		}
	}
}

// ── VAR event parsing verification ─────────────────────────────

// TestParseVarEvent_EndToEnd — verifies that a VAR event JSON payload
// (canonical vendor shape) decodes cleanly to typed enums via
// UnmarshalJSON. We don't currently trigger downstream workflows on
// VAR events, but the parsing path MUST work so the events can be
// stored / logged / eventually acted on.
func TestParseVarEvent_EndToEnd(t *testing.T) {
	cases := []struct {
		name       string
		json       string
		wantType   apifootball.APIEventType
		wantDetail apifootball.APIEventDetail
	}{
		{
			name:       "goal cancelled",
			json:       `{"type": "Var", "detail": "Goal cancelled"}`,
			wantType:   apifootball.EventTypeVar,
			wantDetail: apifootball.DetailGoalCancelled,
		},
		{
			name:       "penalty confirmed",
			json:       `{"type": "Var", "detail": "Penalty confirmed"}`,
			wantType:   apifootball.EventTypeVar,
			wantDetail: apifootball.DetailPenaltyConfirmed,
		},
		{
			name:       "goal cancelled casing variant",
			json:       `{"type": "var", "detail": "GOAL CANCELLED"}`,
			wantType:   apifootball.EventTypeVar,
			wantDetail: apifootball.DetailGoalCancelled,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var e apifootball.APIFixtureEvent
			if err := json.Unmarshal([]byte(tc.json), &e); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if e.Type != tc.wantType {
				t.Errorf("Type = %q, want %q", e.Type, tc.wantType)
			}
			if e.Detail != tc.wantDetail {
				t.Errorf("Detail = %q, want %q", e.Detail, tc.wantDetail)
			}
		})
	}
}
