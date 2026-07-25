// Package wikipedia is the MediaWiki API adapter used by the alias-lookup
// pipeline for entity resolution via full-text search. Distinct from the
// wikidata adapter — Wikidata is the structured knowledge graph;
// Wikipedia is the encyclopedia. Different hosts (en.wikipedia.org vs
// www.wikidata.org), different indexes (CirrusSearch full-text vs
// wbsearchentities label prefix).
//
// Why this adapter exists: Wikidata's `wbsearchentities` is a
// prefix-only label + alias index and misses entities whose mention
// doesn't share a prefix with the canonical label (e.g. "Nice" doesn't
// hit "OGC Nice"). Wikipedia's CirrusSearch (ElasticSearch-backed
// full-text over article bodies) finds the correct article using
// context-augmented queries and cross-references back to the Wikidata
// QID via each article's `pageprops.wikibase_item`.
//
// Design ref: docs/design/proposals/alias-entity-resolution.md § "The
// general recipe".
package wikipedia
