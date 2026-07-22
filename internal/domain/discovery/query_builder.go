// query_builder.go — Twitter search query construction for the
// Discovery workflow. Called once per trackable event (goal / missed
// penalty / red card) to produce the query string that the twitter
// container's /search endpoint navigates to.
//
// Design ref: docs/rebuild/proposals/twitter-search-query.md
// (signed off 2026-07-16, empirically validated 2026-07-22 via the
// Tottenham M. Fernandes goal end-to-end test). Deviations from the
// signed spec live in docs/decisions.md.
//
// Shape:
//
//	(playerTok1 OR playerTok2 OR ... OR alias1 OR alias2 OR ...) filter:videos
//
// Recall-first — every player token AND every team alias enter the
// OR chain. No event-type vocabulary (D3 confirmed by 2026-07-22
// empirical test; adding tokens like `goal`/`scored` didn't rescue
// any legitimate Tottenham-goal tweets that (player OR team) missed).
// Trust `filter:videos` server-side + max_age_minutes client-side +
// V-phase LLM validation to filter false positives.
//
// Own goals are handled implicitly: api-football reports the
// beneficiary team (score-increased) in event.team and the defender
// who scored into their own net in event.player. Query builder takes
// both at face value — the resulting query catches both the celebrating
// side (via team aliases) and the scorer's name.
package discovery

import (
	"errors"
	"strings"

	"github.com/vedantadhobley/found-footy/internal/domain/alias"
)

// QueryInput is the per-event payload the Discovery workflow hands
// to the query builder. Sourced from the fixture-event row + the
// team_aliases row for the scoring team.
type QueryInput struct {
	// PlayerName is event.player.name — the scoring player (goal,
	// penalty goal), the player who missed (missed penalty), the
	// player sent off (red card), or the defender whose own-goal
	// benefited the reported team. Required; empty is caller error
	// (upstream debounce should hold events until player is known,
	// per twitter-search-query.md D4b).
	PlayerName string

	// TeamCanonicalName is the api-football team.name for the
	// scoring team (or beneficiary for own goals). ALWAYS tokenized
	// and unioned with TeamAliases. Not just a fallback — the
	// canonical name is the team's api-reported identity and needs
	// to be in the query regardless of whether the alias pipeline
	// happened to include it. Example: "Bayer Leverkusen" — if the
	// ≥2-lang threshold dropped `bayer` (only present in the German
	// Wikidata label), merging canonical here guarantees it's still
	// in the query. Dedup collapses the common case where aliases
	// already contain the canonical tokens.
	TeamCanonicalName string

	// TeamAliases is the curated set produced by alias.Resolver.Select
	// for the scoring team. Unioned with the canonical-name tokens
	// (see TeamCanonicalName docstring). Empty is legal (Nice-class
	// NoMatch teams) — canonical tokens carry the team slot in
	// that case.
	TeamAliases []string

	// VideoOnly toggles the `filter:videos` server-side restriction.
	// True (default via the Build helper) — search is restricted to
	// tweets carrying video, matching Python's current behavior.
	// False — omit the filter, useful for future sentiment-analysis
	// mode that fetches the same corpus for text + video (not yet
	// implemented; renamed from SentimentMode per user note
	// 2026-07-22).
	VideoOnly bool
}

// QueryLengthWarnThreshold is the number of characters above which
// a caller should log an observability warning. Runaway alias
// generation would show up here — real queries in the 2026-07-22
// empirical test topped out around 220 chars for the most-aliased
// events, well below this bound. See twitter-search-query.md D1
// length observation.
const QueryLengthWarnThreshold = 400

// ErrEmptyQuery signals that BuildTwitterQuery couldn't produce
// any tokens (both player-name tokenization and team aliases
// yielded nothing, AND canonical-name fallback also produced no
// tokens). Callers treat as "skip this attempt without calling
// Twitter" per twitter-search-query.md D4d — a bare `filter:videos`
// query would return every video tweet globally.
var ErrEmptyQuery = errors.New("discovery: query has no tokens")

// ErrEmptyPlayerName signals that BuildTwitterQuery was called with
// PlayerName == "". Upstream (debounce) is supposed to hold events
// until the scoring player is known, per D4b; hitting this in
// practice indicates a pipeline bug.
var ErrEmptyPlayerName = errors.New("discovery: player name is empty (D4b upstream invariant)")

// BuildTwitterQuery composes the OR-everything query string per
// twitter-search-query.md D1. Player tokens and team tokens are kept
// as separate concerns — dedup happens WITHIN the team slot (canonical
// name + curated aliases can produce overlapping tokens like `liverpool`
// in both) but NOT across slots (a player name overlapping a team
// alias is a data-pipeline anomaly, not the query builder's problem).
// Emit order: player tokens first, then team tokens.
//
// Returns:
//   - (query, nil) on success
//   - ("", ErrEmptyPlayerName) if in.PlayerName is empty or whitespace
//   - ("", ErrEmptyQuery) if both slots produce zero tokens (only fires
//     when player fully skip-lists AND team has no canonical/aliases —
//     a compound edge case)
func BuildTwitterQuery(in QueryInput) (string, error) {
	if strings.TrimSpace(in.PlayerName) == "" {
		return "", ErrEmptyPlayerName
	}

	// Player slot — expand via the shared tokenizer. Independent of
	// the team slot below; no cross-slot dedup (player name overlapping
	// a team alias is a data-pipeline concern, not the query builder's).
	playerTokens := alias.TokenizePlayerName(in.PlayerName)

	// Team slot — union of canonical-name tokens + curated aliases,
	// deduped WITHIN the team slot only. See TeamCanonicalName
	// docstring for the "Bayer Leverkusen" rationale (always merge
	// canonical, don't just fallback). One set for the team unit.
	teamTokens := make([]string, 0, 16)
	teamSeen := make(map[string]struct{}, 16)
	for _, tok := range alias.TokenizePlayerName(in.TeamCanonicalName) {
		if _, dup := teamSeen[tok]; dup {
			continue
		}
		teamSeen[tok] = struct{}{}
		teamTokens = append(teamTokens, tok)
	}
	for _, tok := range in.TeamAliases {
		if _, dup := teamSeen[tok]; dup {
			continue
		}
		teamSeen[tok] = struct{}{}
		teamTokens = append(teamTokens, tok)
	}

	// Emit order: player tokens first, then team tokens. Human-
	// readable debug logs lead with player identity.
	if len(playerTokens) == 0 && len(teamTokens) == 0 {
		return "", ErrEmptyQuery
	}

	// Compose: (tok1 OR tok2 OR ... OR tokN) [filter:videos]
	// Player tokens lead, then team tokens. Parens wrap the OR group
	// so `filter:videos` ANDs against the whole disjunction, not just
	// the last term. filter:videos is appended based on VideoOnly;
	// omitted otherwise for future sentiment mode.
	var b strings.Builder
	b.Grow(64 + (len(playerTokens)+len(teamTokens))*10)
	b.WriteByte('(')
	first := true
	writeTok := func(tok string) {
		if !first {
			b.WriteString(" OR ")
		}
		b.WriteString(tok)
		first = false
	}
	for _, tok := range playerTokens {
		writeTok(tok)
	}
	for _, tok := range teamTokens {
		writeTok(tok)
	}
	b.WriteByte(')')
	if in.VideoOnly {
		b.WriteString(" filter:videos")
	}
	return b.String(), nil
}

// Build is the ergonomic wrapper that applies the "video only by
// default" default for callers that don't explicitly set VideoOnly.
// Matches Python's current behavior (searches always include
// filter:videos). Post-sentiment-mode-implementation callers can
// use BuildTwitterQuery directly with VideoOnly=false.
func Build(playerName, teamCanonicalName string, teamAliases []string) (string, error) {
	return BuildTwitterQuery(QueryInput{
		PlayerName:        playerName,
		TeamCanonicalName: teamCanonicalName,
		TeamAliases:       teamAliases,
		VideoOnly:         true,
	})
}

// LengthWarn reports whether the query exceeds the warn threshold
// (400 chars per twitter-search-query.md D1). Callers should log an
// observability event when this returns true — signal that alias
// generation produced an unusually long OR chain that might blow
// past Twitter's ~500 char query limit.
func LengthWarn(query string) bool {
	return len(query) > QueryLengthWarnThreshold
}
