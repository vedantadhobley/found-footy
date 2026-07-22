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
	"fmt"
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
	// scoring team (or beneficiary for own goals). Used as the
	// D4c fallback when TeamAliases is empty — tokenizes the
	// canonical name to seed the team slot with something rather
	// than skipping to empty-team.
	TeamCanonicalName string

	// TeamAliases is the curated set produced by alias.Resolver.Select
	// for the scoring team. Primary source for the team slot.
	// Empty is legal (Nice-class NoMatch teams) — fallback via
	// TeamCanonicalName kicks in.
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
// twitter-search-query.md D1. Deduplicates tokens (player + team
// slots can overlap — e.g. a player named "Argentina Sanchez"),
// preserves first-seen order for deterministic output that makes
// tests + observability logs comparable.
//
// Returns:
//   - (query, nil) on success
//   - ("", ErrEmptyPlayerName) if in.PlayerName is empty
//   - ("", ErrEmptyQuery) if all slots produce no tokens
func BuildTwitterQuery(in QueryInput) (string, error) {
	if strings.TrimSpace(in.PlayerName) == "" {
		return "", ErrEmptyPlayerName
	}

	// Player slot — expand via the shared tokenizer.
	playerTokens := alias.TokenizePlayerName(in.PlayerName)

	// Team slot — prefer the curated alias set from the pipeline.
	// D4c fallback: if empty, tokenize the canonical name and use
	// those tokens. If canonical is also empty (upstream shouldn't
	// let this happen), we end up with just player tokens.
	teamTokens := in.TeamAliases
	if len(teamTokens) == 0 && in.TeamCanonicalName != "" {
		teamTokens = alias.TokenizePlayerName(in.TeamCanonicalName)
	}

	// Union player + team tokens with dedup + order preservation.
	// Player tokens go first so player identity leads in the query
	// (helps debug readability; Twitter's OR is commutative).
	tokens := make([]string, 0, len(playerTokens)+len(teamTokens))
	seen := make(map[string]struct{}, cap(tokens))
	for _, tok := range playerTokens {
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		tokens = append(tokens, tok)
	}
	for _, tok := range teamTokens {
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		tokens = append(tokens, tok)
	}

	if len(tokens) == 0 {
		return "", ErrEmptyQuery
	}

	// Compose: (tok1 OR tok2 OR ... OR tokN) [filter:videos]
	// Parens wrap the OR group so `filter:videos` ANDs against the
	// whole disjunction, not just the last term. filter:videos is
	// appended based on VideoOnly; omitted otherwise for future
	// sentiment mode.
	var b strings.Builder
	b.Grow(64 + len(tokens)*10)
	b.WriteByte('(')
	for i, tok := range tokens {
		if i > 0 {
			b.WriteString(" OR ")
		}
		b.WriteString(tok)
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

// String helper for a debug envelope. Not exported — used by tests.
func debugSummary(in QueryInput, out string, err error) string {
	if err != nil {
		return fmt.Sprintf("query build failed: player=%q, err=%v", in.PlayerName, err)
	}
	return fmt.Sprintf("query=%q, len=%d (warn=%v)", out, len(out), LengthWarn(out))
}
