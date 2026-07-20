// Package alias owns team-alias derivation for Twitter goal-video
// search queries. Two phases: Ingest inserts placeholder rows keyed by
// API-Football team_id; the resolution activity runs a deterministic
// Wikidata pipeline (fuzzy QID lookup + multilingual alias fetch +
// P1449/P1549 + word-processing rules) and populates aliases with a
// 30-day TTL. No LLM. Design ref:
// docs/rebuild/proposals/team-aliases.md.
package alias
