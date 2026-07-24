// Tests for the Playwright↔on-disk cookie conversion — specifically the
// nil-SameSite crash (browser.go:231, string(*c.SameSite)) and the
// round-trip fidelity that keeps LoadCookies/ReplaceCookies from
// manufacturing that nil. See decisions.md / audit-2026-07-26.
package twitter

import (
	"testing"

	"github.com/mxschmitt/playwright-go"
)

// TestCookieFromPW_NilSameSiteDoesNotPanic — the crash: Playwright
// returns SameSite=nil for cookies added without the attribute (exactly
// what LoadCookies/ReplaceCookies produced), and the old code deref'd it
// unconditionally. This fires on the backup after every search.
func TestCookieFromPW_NilSameSiteDoesNotPanic(t *testing.T) {
	c := playwright.Cookie{
		Name:     "auth_token",
		Value:    "abc",
		Domain:   ".x.com",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: nil, // the crash trigger — must not panic
	}
	got := cookieFromPW(c) // would panic pre-fix
	if got.SameSite != "" {
		t.Errorf("nil SameSite should map to empty string, got %q", got.SameSite)
	}
	if got.Name != "auth_token" || !got.HTTPOnly || !got.Secure {
		t.Errorf("other fields not carried through: %+v", got)
	}
}

// TestCookieFromPW_PreservesSameSite — a set SameSite survives the read.
// playwright-go's SameSiteAttribute* constants are already *pointers*.
func TestCookieFromPW_PreservesSameSite(t *testing.T) {
	c := playwright.Cookie{Name: "ct0", Value: "x", SameSite: playwright.SameSiteAttributeLax}
	if got := cookieFromPW(c); got.SameSite != string(*playwright.SameSiteAttributeLax) {
		t.Errorf("SameSite = %q, want %q", got.SameSite, *playwright.SameSiteAttributeLax)
	}
}

// TestPwSameSite_RoundTrip — a SameSite read out (cookieFromPW) then
// loaded back in (pwSameSite) must survive, so the load path no longer
// re-creates the nil. Empty maps to nil (leave unset).
func TestPwSameSite_RoundTrip(t *testing.T) {
	for _, s := range []string{
		string(*playwright.SameSiteAttributeStrict),
		string(*playwright.SameSiteAttributeLax),
		string(*playwright.SameSiteAttributeNone),
	} {
		got := pwSameSite(s)
		if got == nil || string(*got) != s {
			t.Errorf("pwSameSite(%q) = %v, want non-nil %q", s, got, s)
		}
	}
	if pwSameSite("") != nil {
		t.Error(`pwSameSite("") should be nil (leave unset)`)
	}
	if pwSameSite("Garbage") != nil {
		t.Error("pwSameSite of an unrecognized value should be nil")
	}
}
