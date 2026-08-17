# Alias entity-resolution — experiment plan

> **Retired experiment log.** Approach C shipped, but the entire
> Wikipedia→Wikidata resolver was removed on 2026-08-16 after its resolved
> aliases proved net-negative for live Twitter recall. Do not revive this
> design without new measured evidence. Current discovery uses deterministic
> text operations; see the newest entries in
> [`../../decisions.md`](../../decisions.md).

**Cross-refs:**

- [`team-aliases.md`](./team-aliases.md) — SHIPPED alias pipeline (lookup + selection). This proposal targets the LOOKUP step's entity-resolution recall, not the selection pipeline.
- [`../architecture.md`](../../architecture.md) § "Lookup pipeline" — as-shipped ledger of `internal/domain/alias/lookup*.go`.

## The problem

Everything downstream of resolution — skip-list curation, description keyword filters, P31 accept/reject sets — is downstream compensation for **imperfect entity resolution**. If we could reliably match `(team name, country)` → correct Wikidata QID, most of the hardcoded compensation disappears; we just consume Wikidata's own structured aliases at face value.

Concrete failure cases:

- **Nice (France)** — Wikidata's canonical labels are `{en: "OGC Nice", fr: "OGC Nice", de: "OGC Nizza"}`. `wbsearchentities` is a label + alias PREFIX index. None of our 9 variants (`Nice FC`, `Nice football club`, `Nice`) share a prefix with `OGC Nice`, so Q185163 is never returned as a candidate. No downstream filter can save this.
- **Sponsor-prefixed clubs** — Red Bull Salzburg vs "Salzburg", Bayer Leverkusen vs "Leverkusen". Works today only because api-football happens to give us the full sponsor-prefixed form.
- **Non-Latin-script teams (planned expansion)** — Al-Ahly = الأهلي, Vissel Kobe = ヴィッセル神戸, São Paulo FC = São Paulo FC. Wikidata's primary label is often in the team's native script.

Expanding coverage to MLS / J-League / Brasileirão / Saudi Pro / Chinese Super League makes this worse. Today's 37/38 rate will drop; the hardcoded skip-list bloats as we hit non-Latin scripts.

## Why this problem is hard in principle — and the standard pattern for solving it

Entity resolution against a knowledge base is a classical CS problem. The academic name is **Named Entity Linking (NEL)** — given a text mention like "Nice football club", return the correct entity URI in the KB. Standard NEL pipelines have three stages:

1. **Mention detection** — decide that a string is an entity mention. (We skip this — API-Football already gives us the mention as `team.name`.)
2. **Candidate generation** — retrieve possible KB entities the mention could refer to. **Fuzzy retrieval problem — this is where we've been stuck.**
3. **Candidate disambiguation / ranking** — score candidates by context, return the best.

Historical NEL systems (DBpedia Spotlight 2011, AIDA 2011, REL 2020) all use variants of the same recipe: run a fuzzy full-text retrieval for candidate generation, then use context features (surrounding text, coreference, structural cues) to rank.

**Where Wikidata's `wbsearchentities` falls short as a candidate generator:**

- It's a **label + alias prefix index**. Only searches short curated strings; doesn't search article body or contextual mentions.
- **Prefix matching** — "Nice FC" doesn't hit "OGC Nice" because the prefix mismatches. Wikidata search doesn't fuzz across missing prefixes even with lots of shared tokens.
- **No context weighting** — it doesn't know that a query mentioning "football club" should prefer football-typed entities.

**Why Wikipedia's full-text search (CirrusSearch, backed by ElasticSearch) is genuinely better for this class of problem:**

- Indexes **article body text**, not just short labels. Every paraphrase, historical name, mention of "Nice" in a football context is in the index.
- Uses **BM25 relevance scoring** — favors articles where the query terms are dense and topically central. An article titled "OGC Nice" that mentions "Nice" 100 times in a football context outranks the "Nice (city)" article even though both contain "Nice".
- **Field-boosted scoring** — matches in the title weigh more than the intro, intro more than body. A query for "Nice football club France" ranks OGC Nice at #1 because the term set clusters densely near the article title + infobox.
- **Redirect + disambiguation graph** is embedded — Wikipedia editors have added inbound anchor text ("Nice Ligue 1", "OGC", "Nice French football") as prose across thousands of articles. CirrusSearch indexes all of that.
- **Cross-lingual sitelinks** — every article has a `pageprops.wikibase_item` field pointing to the Wikidata QID. **Wikipedia's fuzzy index bridges cleanly back to the structured KB.**

Framing it in one sentence: **Wikipedia is an enormous crowd-sourced redirect table from any plausible mention of an entity to its canonical article, and CirrusSearch is the retrieval engine over that table.** Wikidata is a downstream structured extract; using it directly for entity resolution loses all the paraphrase density.

### The general recipe (applies beyond football)

For any structured-KB entity resolution task where the target is Wikidata / DBpedia / any Wikimedia-linked graph:

1. **Query construction** — build a full-text query combining the entity mention with the strongest disambiguating context you have (domain type, geography, date range).
2. **Full-text retrieval against Wikipedia** — `en.wikipedia.org/w/api.php?action=query&list=search&srsearch={query}`. Returns article titles ranked by BM25.
3. **Extract structured identifier from top hit** — `pageprops.wikibase_item` gives the Wikidata QID. Combine `list=search` with `generator=search` + `prop=pageprops` for a one-request lookup.
4. **Verify the type** — batch-check the retrieved QID against the KB's own type ontology (`P31` in Wikidata) to catch outright category mistakes.
5. **(Optional) Language fallback** — if the English-language search misses, retry against the country's native Wikipedia edition (`fr.wikipedia.org`, `de.wikipedia.org`, etc.). Wikidata `sitelinks` bridge back to the same QID.

Steps 2 + 3 + 4 are compressible into ~2 HTTP round-trips. That's the whole entity resolution.

## Approach A — Multi-language `wbsearchentities` (TESTED, REJECTED)

**Hypothesis (2026-07-21 pre-test):** searching Wikidata's `wbsearchentities` in `language=fr` would rank Q185163 (OGC Nice) higher because Wikidata's per-language search index would prioritize French-primary entities for a French team.

**Result:**

```
wbsearchentities  language=en  Nice          → Q33959 (city), Q16878069, Q105620905, Q3339608, Q3633431
wbsearchentities  language=en  Nice FC       → (no results)
wbsearchentities  language=en  Nice football club → (no results)
wbsearchentities  language=fr  Nice          → Q33959 (city), Q16878069, Q3339608, Q105620905, Q28419401
wbsearchentities  language=fr  Nice FC       → (no results)
wbsearchentities  language=fr  Nice football club → (no results)
wbsearchentities  language=fr  OGC Nice      → Q185163 OGC Nice   [requires "OGC" in query]
```

**Verdict: rejected.** The failure isn't a language-index limitation; it's a prefix-matching limitation. `wbsearchentities` requires the query to share a prefix with the target's label. Whether we search `en` or `fr`, we need the actual token `OGC` in the query — which is orphaned data (not present in any api-football field we could feed).

**Lesson learned:** `wbsearchentities` is not a fuzzy full-text retriever. It's a label/alias prefix lookup. For NEL where the mention doesn't share a prefix with the target's canonical label, we need a different retrieval engine.

## Approach C — Wikipedia full-text search as entity resolver (RECOMMENDED)

**Corrected framing (2026-07-21):** the original doc pitched C as "scrape Wikipedia infoboxes for richer nicknames." That was much narrower than the real leverage. The actual play is to use **Wikipedia's CirrusSearch as our candidate-generation engine**, then pass the resolved QID through the existing Wikidata pipeline for alias extraction.

### Empirical verification (probed 2026-07-21)

Wikipedia full-text search with football-context hints:

```
en.wikipedia.org  Nice football club France        → OGC Nice (top hit)                        ✓
en.wikipedia.org  Nice Ligue 1                     → 2026–27 Ligue 1 | OGC Nice (position 2)   ✓
fr.wikipedia.org  Nice football club               → Olympique Gymnaste Club de Nice (fr title) ✓
en.wikipedia.org  Vissel Kobe                      → Vissel Kobe (top hit)                     ✓
en.wikipedia.org  Sao Paulo "football club"        → São Paulo FC (top hit)                    ✓
en.wikipedia.org  Sporting "Portugal" football     → Sporting CP (top hit)                     ✓
en.wikipedia.org  Al Ahly "football club"          → Al Ahli SC (Amman)                        ✗
                                                     (wrong Al-Ahli; needs country in query)
```

The one miss (Al-Ahly) is fixable by adding the country term to the query: `Al Ahly Egypt football club` should surface the Egyptian club. That's a query-construction issue, not a fundamental limit.

**And the QID extraction works end-to-end** — verified against the OGC Nice article:

```
en.wikipedia.org  page="OGC Nice"  →  pageprops.wikibase_item = Q185163
```

Which is exactly the QID we've been failing to reach via `wbsearchentities`.

### Proposed implementation

> **Note (as-built):** this is the *experiment's* proposal — the shipped code
> (`internal/domain/alias/lookup_club.go` / `lookup_national.go`; as-built
> summary in [`../architecture.md`](../../architecture.md)) diverged in
> specifics: the query templates differ (`{name} {country} football club` for
> clubs, `{country} men's national football team` for nationals), the
> native-language fallback + city-scoring sketched below were **not built**, and
> the `Hit` struct shape differs. Treat this section as design rationale, not the
> current contract.

**New adapter — `internal/infra/wikipedia/`:**

```go
// SearchAndResolve searches Wikipedia's CirrusSearch index and returns
// each hit's title + Wikidata QID (from pageprops.wikibase_item) in a
// single HTTP round-trip using generator=search + prop=pageprops.
func (c *Client) SearchAndResolve(ctx context.Context, query string, opts SearchOpts) ([]Hit, error)

type Hit struct {
    Title       string  // Wikipedia article title
    WikidataQID string  // Q1543 extracted from pageprops.wikibase_item; may be "" if none
    Snippet     string  // Search excerpt for observability
    Score       float64 // CirrusSearch's own relevance score
}

type SearchOpts struct {
    Language string  // "en" default; can retry in country lang on miss
    Limit    int     // typically 5
}
```

**One-request compose (CirrusSearch supports this):**

```
GET /w/api.php
  ?action=query
  &list=search
  &srsearch={query}
  &srlimit=5
  &generator=search
  &prop=pageprops
  &format=json
```

Returns article titles AND their `pageprops.wikibase_item` in one shot.

**Wire into `resolveClub` / `resolveNational`:**

Replace the 9-variant `wbsearchentities` stack with a single Wikipedia query:

```
Club:      "{name} football club {country}"      (fallback: "{name} {country} football")
National:  "{country} national football team"    (nationals are already unambiguous)
```

Then:
1. `wikipedia.SearchAndResolve` — returns candidate `(title, qid)` pairs
2. Filter to hits with a non-empty QID
3. Batch P31 verify (existing code) — kills TV channels, museums, etc. even if Wikipedia's ranking got confused
4. Existing scoring on survivors (city short-circuit still relevant for edge cases)
5. `wikidata.GetEntity` for the winner → aliases (unchanged)

**Fallback:** if the English Wikipedia query returns no P31-passing hit, retry in the country's native Wikipedia edition. Requires an ISO 3166 → ISO 639 mapping (200 entries, one-time hardcode).

### Cost estimate

- 1-2 Wikipedia HTTP calls per cache-miss team (English + possibly country language)
- 1 batch P31 SPARQL (existing)
- 1 Wikidata `GetEntity` for aliases (existing)
- **Total: 3-4 HTTP per team lifetime**, down from ~10 today (9 wbsearchentities + P31 + GetEntity)

Wikipedia's rate limits are generous (documented at 200 req/s per IP for CirrusSearch, much higher than Wikidata's anonymous burst window for `wbsearchentities`). The current 500 ms throttle can probably shrink to 100 ms.

### What this fixes vs what it doesn't

**Fixes:**
- Nice (Q185163) — Wikipedia body-text search finds OGC Nice for "Nice football club France"
- São Paulo, Sporting CP, Vissel Kobe — expansion candidates work with straightforward queries
- Sponsor-prefixed clubs (Bayer Leverkusen, Red Bull Salzburg) — Wikipedia articles use the sponsor-prefixed form as canonical
- Diacritic mismatches (Sao Paulo ↔ São Paulo) — CirrusSearch's stemming handles this

**Doesn't fix:**
- Ambiguous names across countries (Al-Ahly Egypt vs Al-Ahli Amman) — need country term in query as disambiguator
- Genuinely obscure teams not on Wikipedia at all — same NoMatch outcome as today, but now really "not indexed anywhere"

### Introduces one new hardcoding: query template

`{name} football club {country}` is a template we hardcode for clubs. That's ONE template replacing NINE `wbsearchentities` variants. Net reduction in hardcoding.

### Optional refinement — infobox nickname enrichment (deferred)

Once we have a Wikipedia article resolved, we could ALSO parse its infobox (via `action=parse&prop=wikitext&section=0`) to extract the `nickname` field. This is the ORIGINAL Approach C. It adds richer nickname coverage than Wikidata's aliases alone (Atlético Madrid has empty P1449 in Wikidata but rich nickname prose in Wikipedia). Not required for baseline recall; can be added as an incremental enrichment on top of Wikidata aliases if it's ever needed.

## Approach D — Twitter-usage bootstrap (POST-MVP)

Once we have ONE working alias for a team (from api-football `team.name` alone), search Twitter for that name + goal-context terms, collect the top-N tweets, extract co-occurring tokens with high frequency, promote them to aliases.

**What makes this philosophically the strongest approach:**

- Aliases come from **actual usage**, not from a dictionary that may lag or omit
- Handles languages Wikidata is weak in (Arabic, Chinese, Persian)
- Captures fan-invented hashtags (#COYS, #ForzaJuve) and social-media-specific nicknames
- Naturally captures NEW nicknames as they emerge

**Global corpus refinement** — cross-team comparison drops tokens that appear across MANY teams (universal fan vocab like "goal", "GOALLLL"). Only team-specific tokens survive. This is the dynamic-skip-list I originally proposed as reduction (2), applied to a corpus derived from actual Twitter data rather than a hand-maintained list. Much more principled than a hardcoded skip-list.

**Chicken-and-egg constraint (why not now):**

- Requires a working Discovery workflow (T + O3 shipped)
- Requires enough per-team Twitter data to have a signal (probably >10 fixtures per team)
- Would run as a nightly job that refines aliases from the prior day's discovery output

**Post-MVP integration point:** once T (twitter port) and O3-O5 (discovery / video pipeline) are shipped and running against real matches, add a bootstrap job that reads discovery output and updates `team_aliases.aliases` with high-frequency team-specific tokens.

## Experiment sequencing (updated 2026-07-21)

1. ~~A — multi-language `wbsearchentities`.~~ **TESTED and REJECTED. Prefix matching, not a language-index issue.**
2. **C — Wikipedia full-text search as resolver. RECOMMENDED. Prototype next.**
3. D — Twitter-usage bootstrap. Post-MVP (post-T, post-O3-O5).

## Notes on hardcoding

- Approach A would have added a country → ISO 639 language mapping (~200 entries). Moot; A rejected.
- **Approach C adds one query template** (`{name} football club {country}`) — ONE template replacing NINE `wbsearchentities` variants. **Net reduction in hardcoding.**
- Approach C ALSO enables removing most of the description skip-keyword list, since Wikipedia's ranking already prioritizes football-typed articles over TV channels / stadiums / disambiguation pages.
- Approach D adds nothing structural — the alias set is derived from live data.

The path to less hardcoding runs through **better candidate generation** (fuzzy retrieval over rich context) + **structural verification** (P31 type check). Not through smarter skip-list algorithms.
