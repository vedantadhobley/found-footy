// query_builder.go — Twitter search query construction for the Discovery
// workflow. Called once per trackable event (goal / missed penalty / red
// card) to produce the query string that the twitter container's /search
// endpoint navigates to.
//
// Design ref: docs/design/proposals/twitter-search-query.md. The original
// "OR-everything + all-tokens" shape was REPLACED 2026-08-15 after the live
// MLS test surfaced a wrong-game clip for the Dorsch goal: the team's aliases
// had resolved to generic words + a different club ({inter,united,york,y9fc}),
// and OR-ing those bare tokens matched K-pop, Ariana Grande, and political spam
// — burying the real clips, which the aspect/clock filters then killed. See
// docs/decisions.md 2026-08-15 (twitter-search-query rework).
//
// Shape now — an OR of DISTINCTIVE terms only, never a bare generic word:
//
//	(surname OR "Canonical Team Name" OR ABBREV OR distinctiveAlias ...) filter:videos
//
// Empirically validated live (2026-08-15) against our own /search endpoint:
//   - the fix returned 13 all-relevant Toronto/MLS clips vs the old query's 5
//     (4 of them political spam);
//   - bare surname "Messi" (16) beats quoted "Lionel Messi" (14) — the surname
//     catches surname-only + nickname ("Leo Messi") tweets the phrase misses;
//   - for the Dorsch goal the clips carried NO player name at all — only the
//     team abbreviation "TFC" — so both the surname and the abbreviation must
//     be present for cross-goal recall.
//
// Trust `filter:videos` server-side + max_age_minutes client-side + V-phase
// LLM validation to filter the residual false positives.
//
// Own goals are handled implicitly: api-football reports the beneficiary team
// in event.team and the own-goal scorer in event.player. The query catches the
// celebrating side (team terms) and the scorer's surname.
package discovery

import (
	"errors"
	"strings"

	"github.com/vedantadhobley/found-footy/internal/domain/alias"
)

// QueryInput is the per-event payload the Discovery workflow hands to the
// query builder. Sourced from the fixture-event row + the team_aliases row
// for the scoring team.
type QueryInput struct {
	// PlayerName is event.player.name — the scoring player (goal, penalty
	// goal), the player who missed (missed penalty), the player sent off
	// (red card), or the own-goal scorer. Required; empty is caller error
	// (upstream debounce holds events until player is known, per D4b). Only
	// the SURNAME (last significant token) enters the query.
	PlayerName string

	// TeamCanonicalName is the api-football team.name for the scoring team
	// (or beneficiary for own goals). Emitted as a QUOTED phrase ("Toronto
	// FC") — never tokenized to bare words — plus a derived fan abbreviation.
	// This is the clean, always-correct team identity from the API, so the
	// query works even when the alias pipeline mis-resolved the team.
	TeamCanonicalName string

	// TeamCode is the api-football 3-letter code (e.g. "TOR"). Reserved;
	// not currently emitted — fans type the club abbreviation ("TFC"), which
	// the derived abbreviation approximates better than the API code.
	TeamCode string

	// TeamAliases is the curated set produced by alias.Resolver.Select for the
	// scoring team. Only DISTINCTIVE aliases are emitted: bare generic words
	// (united/city/real/york/…) and tokens already in the canonical name are
	// dropped; abbreviations (nyrb/mcfc) and nicknames are kept. Empty is legal
	// — the canonical name + derived abbreviation carry the team slot.
	TeamAliases []string

	// VideoOnly toggles the `filter:videos` server-side restriction (default
	// true via Build). False omits it — for a future text+video sentiment mode.
	VideoOnly bool
}

// QueryLengthWarnThreshold — chars above which a caller should log a warning
// (runaway alias generation). Real queries stay well under Twitter's ~500 cap.
const QueryLengthWarnThreshold = 400

// ErrEmptyQuery signals BuildTwitterQuery produced no tokens (player fully
// skip-listed AND no canonical/aliases). Callers skip the attempt per D4d —
// a bare `filter:videos` query would return every video tweet globally.
var ErrEmptyQuery = errors.New("discovery: query has no tokens")

// ErrEmptyPlayerName signals BuildTwitterQuery was called with an empty
// PlayerName. Upstream debounce holds events until the scorer is known (D4b);
// hitting this indicates a pipeline bug.
var ErrEmptyPlayerName = errors.New("discovery: player name is empty (D4b upstream invariant)")

// queryGenerics — team-name WORDS too generic to OR as a bare search token:
// "united" matches every United, "york" matches New-York-anything, "real"
// matches every Real. They stay valid ALIASES (team identity) but are never
// emitted standalone — the quoted canonical name + abbreviations carry the
// team. Seeded from the 2026-08-15 alias-contamination audit (tokens shared
// across many teams). Abbreviations (nyrb/mcfc) are NOT here — they're
// team-specific and stay.
var queryGenerics = map[string]struct{}{
	"united": {}, "city": {}, "real": {}, "red": {}, "new": {}, "york": {},
	"inter": {}, "union": {}, "sporting": {}, "racing": {}, "athletic": {},
	"blues": {}, "orange": {}, "county": {}, "town": {}, "rovers": {},
	"olympique": {}, "foot": {}, "mls": {}, "afc": {}, "fcb": {},
	"sportiva": {}, "societa": {}, "unione": {}, "verein": {}, "fussballclub": {},
}

func isQueryGeneric(tok string) bool {
	_, g := queryGenerics[strings.ToLower(tok)]
	return g
}

// clubSuffixes — trailing club-type words used to build the fan abbreviation
// ("Toronto FC" → TFC, "Orlando City SC" → OCSC).
var clubSuffixes = map[string]string{"fc": "FC", "sc": "SC", "cf": "CF"}

// deriveAbbrev builds the common fan abbreviation from the canonical name:
// initials of the significant (skiplist-filtered) tokens + a trailing club
// suffix if present. Returns only ≥3-char results — 2-char initials ("Man
// City" → MC, "Real Madrid" → RM) are too generic to search, and those teams'
// real abbreviations come from their (correctly-resolved) aliases anyway. Its
// job is to give WRONG-entity teams — Toronto FC resolved to York United's
// aliases, no real "TFC" — a usable term without waiting on a resolver fix.
func deriveAbbrev(canonical string) string {
	fields := strings.Fields(canonical)
	if len(fields) == 0 {
		return ""
	}
	suffix := clubSuffixes[strings.ToLower(fields[len(fields)-1])]
	var initials strings.Builder
	for _, tok := range alias.TokenizePlayerName(canonical) {
		if tok != "" {
			initials.WriteByte(tok[0])
		}
	}
	abbr := strings.ToUpper(initials.String()) + suffix
	if len(abbr) < 3 {
		return ""
	}
	return abbr
}

// BuildTwitterQuery composes the distinctive-terms OR query. Player slot is
// the surname; team slot is the quoted canonical name + derived abbreviation +
// distinctive aliases (bare generics and canonical-name duplicates dropped).
//
// Returns:
//   - (query, nil) on success
//   - ("", ErrEmptyPlayerName) if PlayerName is empty/whitespace
//   - ("", ErrEmptyQuery) if no terms survive (player fully skip-listed AND no
//     usable team terms)
func BuildTwitterQuery(in QueryInput) (string, error) {
	if strings.TrimSpace(in.PlayerName) == "" {
		return "", ErrEmptyPlayerName
	}

	var terms []string
	seen := make(map[string]struct{}, 16)
	add := func(term string) {
		if term == "" {
			return
		}
		key := strings.ToLower(strings.Trim(term, `"`))
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		terms = append(terms, term)
	}
	// quote wraps a multi-word term as a Twitter phrase; single words pass bare.
	quote := func(s string) string {
		if strings.ContainsRune(s, ' ') {
			return `"` + s + `"`
		}
		return s
	}

	// Player slot: the SURNAME only (last significant token). Empirically the
	// best single player term — the surname subsumes the full name (any
	// "Niklas Dorsch" tweet contains "Dorsch") and also catches surname-only +
	// nickname tweets a quoted full name misses. A bare first name would match
	// every namesake, and there's no team AND-gate here to filter that.
	if pt := alias.TokenizePlayerName(in.PlayerName); len(pt) > 0 {
		add(pt[len(pt)-1])
	}

	// Team slot: DISTINCTIVE terms only. Quoted canonical name + derived
	// abbreviation carry it even when the alias pipeline mis-resolved the team.
	canonTokens := make(map[string]struct{})
	if canon := strings.TrimSpace(in.TeamCanonicalName); canon != "" {
		add(quote(canon))
		add(deriveAbbrev(canon))
		for _, t := range alias.TokenizePlayerName(canon) {
			canonTokens[strings.ToLower(t)] = struct{}{}
		}
	}
	// Aliases: keep the distinctive ones (abbreviations, nicknames); drop bare
	// generics and any token already covered by the quoted canonical name.
	for _, a := range in.TeamAliases {
		if isQueryGeneric(a) {
			continue
		}
		if _, dup := canonTokens[strings.ToLower(a)]; dup {
			continue
		}
		add(quote(a))
	}

	if len(terms) == 0 {
		return "", ErrEmptyQuery
	}

	var b strings.Builder
	b.Grow(64 + len(terms)*12)
	b.WriteByte('(')
	b.WriteString(strings.Join(terms, " OR "))
	b.WriteByte(')')
	if in.VideoOnly {
		b.WriteString(" filter:videos")
	}
	return b.String(), nil
}

// Build applies the "video only by default" default. Matches Python (searches
// always include filter:videos).
func Build(playerName, teamCanonicalName string, teamAliases []string) (string, error) {
	return BuildTwitterQuery(QueryInput{
		PlayerName:        playerName,
		TeamCanonicalName: teamCanonicalName,
		TeamAliases:       teamAliases,
		VideoOnly:         true,
	})
}

// LengthWarn reports whether the query exceeds the warn threshold (400 chars).
func LengthWarn(query string) bool {
	return len(query) > QueryLengthWarnThreshold
}
