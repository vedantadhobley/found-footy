// query_builder.go — Twitter search query construction for the Discovery
// workflow. Called once per trackable event (goal / missed penalty / red
// card) to produce the query string that the twitter container's /search
// endpoint navigates to.
//
// Design ref: docs/design/proposals/twitter-search-query.md, then two live-MLS
// reworks logged in docs/decisions.md:
//   - 2026-08-15: replaced the original "OR-everything + all-tokens" shape after
//     a wrong-game clip surfaced for the Dorsch goal — the team's resolved
//     aliases were generic/wrong-entity words ({inter,united,york,y9fc}) that
//     matched K-pop, Ariana Grande, and political spam.
//   - 2026-08-16: DISCONNECTED the resolved aliases entirely (measured
//     net-negative — the junk terms crowd official clips out of the live-search
//     scroll budget), fixed the derived abbreviation, and moved the player slot
//     to all-tokens.
//
// Shape now — an OR of DISTINCTIVE terms, never a bare generic word:
//
//	(playerToken OR playerToken … OR "Canonical Team Name" OR ABBREV) filter:videos
//
// Player = ALL significant name tokens (minus a trailing generational suffix),
// OR'd — reliable surname extraction isn't possible across name cultures
// (Alexander-Arnold → the compound, Son Heung-min → family-name-first), so every
// token is included and the quoted team name anchors the query. Team = the
// quoted canonical name (from api-football, always correct) + a derived fan
// abbreviation (initials + club suffix: "Toronto FC" → TFC). Resolved aliases
// are no longer emitted.
//
// Trust `filter:videos` server-side + max_age_minutes client-side + V-phase
// LLM validation to filter the residual false positives.
//
// Own goals are handled implicitly: api-football reports the beneficiary team
// in event.team and the own-goal scorer in event.player. The query catches the
// celebrating side (team terms) and the scorer's name tokens.
package discovery

import (
	"errors"
	"strings"

	"github.com/gosimple/unidecode"

	"github.com/vedantadhobley/found-footy/internal/domain/alias"
)

// QueryInput is the per-event payload the Discovery workflow hands to the
// query builder. Sourced from the fixture-event row + the team_aliases row
// for the scoring team.
type QueryInput struct {
	// PlayerName is event.player.name — the scoring player (goal, penalty
	// goal), the player who missed (missed penalty), the player sent off
	// (red card), or the own-goal scorer. Required; empty is caller error
	// (upstream debounce holds events until player is known, per D4b). ALL
	// significant name tokens (minus a trailing generational suffix) enter the
	// query, OR'd — extraction of "the surname" isn't consistent across name
	// cultures, so inclusion is used instead (see decisions.md 2026-08-16).
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
// the initials of ALL its words (unidecoded — never skip-list filtered) plus a
// trailing club suffix if present. "Los Angeles FC" → LAFC, "Toronto FC" → TFC,
// "Orlando City SC" → OCSC. Returns only ≥3-char results — 2-char initials
// ("Man City" → MC, "Real Madrid" → RM) are too generic to search.
//
// This is a PRIMARY team term now that the resolved aliases are disconnected
// (see decisions.md 2026-08-16). It must use strings.Fields, NOT the
// player-name tokenizer: the tokenizer skip-lists articles, which turned "Los
// Angeles FC" into "AFC" (colliding with AFC Ajax) — the exact bug this fix
// removes. Where it returns "" (FC-PREFIXED names like "FC Cincinnati", whose
// initials collapse below 3 chars), the quoted canonical name carries the team.
func deriveAbbrev(canonical string) string {
	fields := strings.Fields(unidecode.Unidecode(canonical))
	if len(fields) == 0 {
		return ""
	}
	suffix := ""
	initialFields := fields
	if s, ok := clubSuffixes[strings.ToLower(fields[len(fields)-1])]; ok {
		suffix = s
		initialFields = fields[:len(fields)-1]
	}
	var initials strings.Builder
	for _, f := range initialFields {
		initials.WriteByte(f[0]) // f is ASCII after unidecode; first byte = first letter
	}
	abbr := strings.ToUpper(initials.String()) + suffix
	if len(abbr) < 3 {
		return ""
	}
	return abbr
}

// genSuffixes — trailing generational suffixes that are never the searched
// surname. "Vinícius Júnior" → the surname is "vinicius", not "junior" (which
// matches every player named Junior). Deliberately narrow: filho/neto/sr are
// omitted because they're commonly real surnames (e.g. the keeper Neto), and a
// bare "jr"/"sr" is already dropped by the tokenizer's ≤2-char filter. See
// decisions.md 2026-08-16.
var genSuffixes = map[string]struct{}{
	"junior": {}, "jr": {}, "jnr": {},
}

// stripGenSuffix drops trailing generational-suffix tokens so the surname is
// the significant name. Never strips to empty — a mononym that IS a suffix word
// ("Neto", "Junior") keeps its single token.
func stripGenSuffix(toks []string) []string {
	for len(toks) > 1 {
		if _, ok := genSuffixes[toks[len(toks)-1]]; ok {
			toks = toks[:len(toks)-1]
			continue
		}
		break
	}
	return toks
}

// quoteTerm wraps a multi-word term as a Twitter phrase ("Toronto FC"); single
// words pass bare. Shared by QueryTerms so canonical names + multi-word aliases
// phrase-match instead of OR-ing their loose words.
func quoteTerm(s string) string {
	if strings.ContainsRune(s, ' ') {
		return `"` + s + `"`
	}
	return s
}

// QueryTerms returns the distinct player terms and team terms the query is
// built from — deduped (case-insensitive, ignoring surrounding quotes), in
// emission order. player is the surname (last significant token); team is the
// quoted canonical name + derived abbreviation + distinctive aliases (bare
// generics and canonical-name duplicates dropped).
//
// Exposed so experiments can recombine the SAME terms under different query
// STRUCTURES (OR-of-all vs player-AND-team) without re-implementing — and
// drifting from — the extraction logic. See scripts/probe_search.
// BuildTwitterQuery is exactly the OR of player ∪ team.
func QueryTerms(in QueryInput) (player, team []string) {
	seen := make(map[string]struct{}, 16)
	// take returns the term if it's new (dedup key = lowercased, de-quoted),
	// else "" so the caller skips it.
	take := func(term string) string {
		if term == "" {
			return ""
		}
		key := strings.ToLower(strings.Trim(term, `"`))
		if _, dup := seen[key]; dup {
			return ""
		}
		seen[key] = struct{}{}
		return term
	}

	// Player slot: ALL significant name tokens (minus a trailing generational
	// suffix), each OR'd into the query. Extracting "the one surname" can't be
	// consistent across name cultures — last-token gives "arnold" for
	// Alexander-Arnold and "min" for Son Heung-min (Korean family name is
	// first). Including every token guarantees the meaningful one is present.
	// The tokenizer already drops the API's abbreviated first initial ("M.
	// Salah" → salah), so for the common case this IS just the surname; the
	// residual cost is a common first name for the minority of un-abbreviated
	// names ("Mohamed Salah" → mohamed OR salah), accepted as low-stakes (the
	// player slot is a minor contributor — the quoted team name is the
	// workhorse). Generational suffix stripped so "Vinícius Júnior" → vinicius,
	// not junior. See decisions.md 2026-08-16.
	for _, tok := range stripGenSuffix(alias.TokenizePlayerName(in.PlayerName)) {
		if t := take(tok); t != "" {
			player = append(player, t)
		}
	}

	// Team slot: DISTINCTIVE terms only. Quoted canonical name + derived
	// abbreviation carry it even when the alias pipeline mis-resolved the team.
	canonTokens := make(map[string]struct{})
	if canon := strings.TrimSpace(in.TeamCanonicalName); canon != "" {
		if t := take(quoteTerm(canon)); t != "" {
			team = append(team, t)
		}
		if t := take(deriveAbbrev(canon)); t != "" {
			team = append(team, t)
		}
		for _, tok := range alias.TokenizePlayerName(canon) {
			canonTokens[strings.ToLower(tok)] = struct{}{}
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
		if t := take(quoteTerm(a)); t != "" {
			team = append(team, t)
		}
	}
	return player, team
}

// BuildTwitterQuery composes the distinctive-terms OR query — the OR of the
// player and team terms from QueryTerms. Player slot is the surname; team slot
// is the quoted canonical name + derived abbreviation (+ distinctive aliases
// when the caller supplies them; prod now passes nil — see decisions.md
// 2026-08-16).
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
	player, team := QueryTerms(in)
	terms := append(append(make([]string, 0, len(player)+len(team)), player...), team...)
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
