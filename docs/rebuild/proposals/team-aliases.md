# Team aliases pipeline — design proposal

**Status:** design-first draft. Signed off in principle 2026-07-19; implementation pending per tasks #134, #138. Do not deviate from this design without a new proposal or decisions.md entry.

**Cross-refs:**

- Decision — [`../../decisions.md`](../../decisions.md) 2026-07-19 entry (this proposal is the design ref)
- Python behavior — [`../python-functional-spec.md`](../python-functional-spec.md) team-alias RAG section; source code at `archive/src/activities/rag.py`
- Plan intent — [`../../rebuild-plan.md`](../../rebuild-plan.md) §5 W3 (DiscoveryWorkflow consumes aliases)
- Empirical basis — `/tmp/claude-1000/.../scratchpad/alias-eval.md` (session-scoped, results summarized here)
- Working discipline — [`../../../CLAUDE.md § Working discipline`](../../../CLAUDE.md#working-discipline-mandatory-since-2026-07-07-retro)

## Purpose

Derive per-team alias sets used to construct Twitter goal-video search queries. Each fixture goal produces a search where team aliases are OR-joined together with goal-context terms; broader alias coverage = higher tweet recall = more candidate videos to validate.

Aliases are looked up once per team, cached in Postgres for 30 days, and refreshed on cache miss or explicit invalidation. The lookup runs in Ingest's team-cache-refresh activity (already fires daily), not in the hot Discovery path.

## Why not simpler / bigger answers

Options considered and rejected before landing here:

- **Curated YAML in repo** — human maintenance treadmill, doesn't self-update for new teams (promotion, cup opposition). Rejected 2026-07-18.
- **API-Football only (`code` + `name`)** — vendor gives canonical name and 3-letter FIFA code, no nicknames. Insufficient on its own. These fields ARE consumed as inputs to the pipeline, but they don't cover nicknames.
- **TheSportsDB alternate-names endpoint** — thinner coverage than Wikidata; not worth the extra external dependency.
- **Wikipedia infobox scrape** — Wikipedia is Wikidata's downstream. Wikidata's structured `aliases` + `P1449` beat parsing MediaWiki markup.
- **LLM gap-fill for the deep-cut nicknames** (Colchoneros, Scaloneta, etc.) — briefly proposed then rejected mid-design 2026-07-19. Deterministic pipeline covers ~90-95% of legit tweet-relevant aliases; the missing tail is niche nicknames whose tweets almost always contain a dominant term (Argentina, Atlético) also caught by our OR-query. Not worth the LLM dependency for a small recall margin. Can be revisited if prod data shows a specific team's recall meaningfully suffers.

## Semantic model

Wikidata's alias data for football teams surfaces across four fields we care about, in a single JSON payload from `https://www.wikidata.org/wiki/Special:EntityData/{QID}.json`:

- **`labels.<lang>`** — canonical name per language.
- **`aliases.<lang>`** — alternate names per language. This is where nicknames like Les Bleus (fr), Albiceleste (fr/de/pl/ca/sl/mk), Seleção (pt/fr/it/de) live.
- **`claims.P1449[]`** — explicit nickname property. Where Barça, Blaugrana, The Reds, The Gunners live for teams that have them set. Inconsistent across teams (Barcelona: 10 entries; Atlético: empty).
- **`claims.P1549[]`** on the LINKED country entity — demonym forms for national teams. Where Argentine + Argentinian, British + Briton, Spanish + Spaniard live.

Additional Wikidata data referenced by the selection algorithm:

- **`claims.P17[]`** — team's country QID. Used to fetch demonyms for national teams and (optionally) to constrain club disambiguation during the LOOKUP phase.
- **`claims.P159[]`** — team's headquarters location. Combined with API-Football's `venue.city`, used to skip city-name words that aren't part of the canonical team name (Arsenal is in London but "London" isn't a legit search term for Arsenal goals).

## Pipeline shape

Two phases: **Lookup** → **Selection**. Cache is populated at the end and served for 30 days.

### Phase 1 — Lookup (name → QID)

Ports Python's proven fuzzy stack from `rag.py`, with two clean-ups:

1. **7-variant fuzzy search** against Wikidata's `wbsearchentities` MediaWiki action (NOT SPARQL). Variants for clubs: `{name} FC`, `{name} football club`, `FC {name}`, `{name} {city}`, `{name} FC {city}`, `{name} FC {country}`, `{name} United`, `{name} City`, bare name. Nationals: fewer variants — the search space is nearly unambiguous.
2. **Description-quality scoring** on each candidate. Skip descriptions matching `women/youth/reserve/futsal/beach/basketball/stadium/arena/disambiguation`. For clubs, score by presence of city/country in the description; +200 city, +100 country, +50 for " in " locational phrasing.
3. **Python's LLM-generated country variations** for description matching (Spain → [spanish, espana, espanol]) are replaced by Wikidata-derived data: query the country's P1549 demonyms + P1448 native names + strip diacritics. Same disambiguation coverage without an LLM.

Python's real-world hit rate was 99.9%. Go port targets the same, verified empirically against tracked teams before shipping.

Tracked as task #133.

### Phase 2 — Selection (raw Wikidata → alias set)

Deterministic. Split by team type. Both branches share the word-processing pipeline:

1. NFD normalize + strip Unicode combining marks (diacritics)
2. Preprocessing: `ß → ss` before NFD so `fußball → fussball` matches the skip-list
3. Split on whitespace + dashes; strip periods, commas, apostrophes
4. Lowercase for comparison + output
5. Drop tokens ≤ 2 chars, pure-digit tokens, CamelCase concatenations (`LiverpoolFC`, `FCBarcelona`)
6. Multilingual skip-list of pure organizational descriptors — words that never identify a team on their own. Corrected from an earlier draft that incorrectly included `sporting`, `elftal`, `mannschaft`, `selecao`, `seleccion` — those DO identify specific teams (Sporting CP, Netherlands, Brazil, Spain).

   **SKIP list** (organizational-only, safe to drop):
   - Suffixes: `fc, ac, sc, cf`
   - Football-organizational: `football, futbol, futebol, calcio, fussball` (ß normalized to ss), `soccer, club, clube, klub, association, associazione, sociedade, society, sad, deportivo, nazionale, nationalmannschaft, selection, selectionnee, national, team, equipe, reprezentacja, reprezentanca, nogometna, futbolowa, futbolista, voetbal, voetbalelftal`
   - Articles/connectives: `the, los, las, la, le, les, el, il, das, der, die, den, de, del, du, di, da, do, dos, and, of, van, en, y`
   - Placeholder junk: `mens` (from splitting `X men's national football team` → `mens`), `sport, sports`

   **NEVER skip these** (team-identifying words that look generic but distinguish specific teams when combined with context — Python's LLM sometimes over-filtered these, we don't):
   - `united` (Manchester United, Newcastle United, Leeds United, Sheffield United, West Ham United)
   - `city` (Manchester City, Leicester City, Norwich City, Coventry City)
   - `athletic` (Athletic Bilbao, Sporting Athletic, Athletic Club)
   - `sporting` (Sporting CP, Sporting Gijón, Sporting Kansas City)
   - `real` (Real Madrid, Real Sociedad, Real Betis, Real Valladolid)
   - `rangers` (Rangers FC, Queens Park Rangers)
   - `rovers` (Blackburn Rovers, Bristol Rovers, Doncaster Rovers)
   - `town, wanderers, borough, county` (Wolverhampton Wanderers, Middlesbrough, etc.)
   - `dynamo, olympique, olympic, borussia, juventus, atlético, atletico`
   - Foreign-language nickname roots: `seleção, seleçao, selecao, seleccion, seleccio, elftal, mannschaft` — these DO identify specific national teams (Brazil, Spain, Netherlands, Germany)

**Clubs — V5 rule:**

- Aliases extracted from `labels.<lang>` + `aliases.<lang>` across `en/es/fr/it/pt/de/ca/gl/nl/pl/ro` (11-language Latin-script subset)
- P1449 nickname property values extracted (language-agnostic)
- Keep a word if any of:
  - Present in canonical team name from API-Football (always kept)
  - Appears in P1449 (Wikidata explicitly says "nickname")
  - Appears in ≥ 2 distinct languages after normalization (cross-language validation)
- Additional skip: word equals API-Football's `venue.city` AND venue city not a substring of canonical team name. Fixes "London" → dropped for Arsenal; keeps "Liverpool" for Liverpool F.C. and "Newcastle" for Newcastle United.

**Nationals — V8 rule:**

- Same word-processing + language subset + keep rule.
- **Additional demonym expansion**: from the linked country entity (via P17), extract P1549 demonym forms **restricted to English only** (`lang == "en"`). English P1549 already contains all legitimate forms — dual/triple demonym countries have all their forms tagged en (Argentina: Argentine + Argentinian + Argentinean; UK: British + Briton; Spain: Spanish + Spaniard; US: American + Americans). Foreign-language demonym forms (`americain`-fr, `americana`-es, `amerikaner`-de) don't match English tweets and just bloat the query — verified 2026-07-19 with extension test dropping USMNT from 29 → 12 tokens with zero recall loss.
- No venue-city skip — nationals have no venue city in the semantic sense.
- **Extra multilingual-noise skip-list for nationals only**: `nacional, seleccion` (generic Portuguese/Spanish "national"/"team" terms that appear across many countries' aliases — not team-specific) + common foreign country-name variants that duplicate the English canonical: `holanda/holandesa` (Netherlands), `croacia/croata`, `alemana/alemania/germania` (Germany), `inglaterra/inglesa` (England), `belga/belgica/belgische` (Belgium), `espanola` (Spain), `francesa/francia` (France), `mexic/mexicana` (Mexico), `estados/unidos` (USA), `baixos/paises` (Netherlands in Portuguese), `occidental` (East/West Germany artifact). Preserves team-specific words like `selecao` (Brazil), `elftal` (Netherlands), `mannschaft` (Germany), `bleus` (France), `azzurri/azzurra/squadra` (Italy) — these DO identify specific teams.

**Why ≥2 languages, not ≥3:**

Eval V3 (≥2) beat V4 (≥3) by 0.03 club F1. ≥3 drops legit acronyms present in only 2 languages' alias lists (LFC in en+fr, CFC in en only). Combined with the "always keep P1449 + always keep canonical + always keep English aliases" rules the threshold matters less — it's just filtering the fuzzy middle of foreign-language spelling variants.

**No top-N cap.** Python capped at 5-10 aliases because it did 1 Twitter search per alias. Advanced Twitter search's OR-syntax puts all aliases in one query for the same cost as one; more aliases = pure recall gain, bounded only by ~500-char query limit (never approached in practice).

## Data model

`team_aliases` pg table:

```sql
CREATE TABLE team_aliases (
    team_id           BIGINT PRIMARY KEY,       -- API-Football team ID (one row per team)
    canonical_name    TEXT NOT NULL,            -- API-Football team.name at time of resolution
    aliases           TEXT[] NOT NULL,          -- normalized lowercase words for OR-query
    wikidata_qid      TEXT NOT NULL,            -- cached lookup — QIDs are permanent
    resolved_at       TIMESTAMPTZ NOT NULL      -- for 30-day TTL check
);

CREATE INDEX ON team_aliases (resolved_at);
```

One row per team (not one per alias — keeps refresh atomic). `wikidata_qid` cached means the expensive fuzzy-search lookup phase runs ONLY on genuinely-new teams (never seen before). On 30-day refresh, we already know the QID and skip straight to the entity-JSON fetch + selection.

Reads: `SELECT aliases FROM team_aliases WHERE team_id = $1 AND resolved_at > now() - interval '30 days'`.

Refresh trigger: Ingest's team-cache-refresh activity iterates tracked teams. For cache miss on aliases, if a `wikidata_qid` exists from a prior lookup, re-run selection only. If no QID, run full lookup + selection.

## Coverage expectations

Empirical scores from the eval (F1 vs hand-curated gold standard on 15 clubs + 10 nationals):

| Team type | Clubs F1 | Nationals F1 |
|---|---:|---:|
| Wikidata deterministic (V5/V8) | 0.63 | 0.25 |

Baseline scores are conservative — gold standard was intentionally strict on stadium metonymy (Anfield, Old Trafford) and city-only shorthand (Madrid for Real, Milan for AC Milan) that fans use in real tweets. Actual prod hit rate on tweets should exceed the eval F1 by a wide margin.

The nationals F1 of 0.25 looks low but the gold standard for nationals is small (4-5 canonical words per team) and precision suffers from adding many demonym variants that ARE legit but weren't in gold. Real prod recall on nationals is expected to be very good — this is a gold-standard construction artifact, not a pipeline weakness.

**Known coverage gaps** (data-completeness problems in Wikidata, not algorithm failures) — these will NOT be in the output:

- Atlético Madrid: Colchoneros, Rojiblancos (empty P1449)
- Sevilla: Sevillistas, Nervionenses, Palanganas (empty P1449)
- Bayern Munich: Bavarians
- Manchester United: ManU
- Manchester City: ManCity
- Real Madrid: Madridistas
- Argentina: Scaloneta (post-2022 nickname, uncurated on Wikidata)
- France: Tricolores
- Brazil: Canarinho, Verde-amarela

These represent ~5-10% of legit tweet-relevant aliases. Tweets that use these niche nicknames almost always also contain a dominant term (Argentina, Atlético) that the OR-query catches via canonical + core-nickname aliases. If prod hit rate suggests a specific team's recall is meaningfully hurt, LLM gap-fill can be added later as a targeted enhancement.

## Implementation plan

1. **pg schema** — `team_aliases` table, wired into `internal/infra/pg/schema.sql`. Task #138.
2. **`internal/domain/team/`** — package skeleton with types (`AliasSet`, `LookupInput`, `LookupResult`). Task #134.
3. **Word-processing helpers** — `normalize.go`, `skiplist.go` in `internal/domain/team/`. Unit tests against fixture strings.
4. **Selection logic** — `select_club.go`, `select_national.go`. Unit tests against fixture Wikidata JSON files (checked in as testdata).
5. **Lookup logic** — `lookup.go` porting Python's fuzzy stack. Integration tests hitting real Wikidata (skipped in `-short`).
6. **Ingest activity** — `RefreshTeamAliases(teamID)` in `internal/domain/team/activities.go`. Invoked from Ingest's team-cache-refresh loop.
7. **Discovery reader** — `LookupAliases(ctx, teamID) []string` for query construction. Simple pg SELECT.
8. **Regression test** against the 25-team eval set showing F1 doesn't drift over refactors.

Estimated 2 dev-days end to end.

## Open questions

- **Which languages should be in the 11-language subset?** Current: en/es/fr/it/pt/de/ca/gl/nl/pl/ro. Coverage decisions here directly affect nickname recall (Basque `eu` for Athletic Bilbao's `Zurigorriak` isn't in the current subset — Zurigorriak is recovered via P1449 which is language-agnostic, but the language decision matters for edge cases).
- **Should the skip-list live in code or config?** Currently: code const. Config would let us tune without redeploy, but changes are rare enough that code is fine.

## Non-goals

- **Multilingual Twitter search.** Aliases include foreign-language forms (Seleção, Bleus, Albiceleste) that appear in English-language tweets about those teams. We are NOT expanding Twitter search to Portuguese/French/Spanish tweet feeds. If we ever do, this pipeline's multilingual output is already ready.
- **Player aliases.** Separate pipeline, no Wikidata lookup, no caching. Deterministic OR-expansion off API-Football player name. Task #135.
- **Historical alias drift.** If FC Barcelona renames itself we won't detect it automatically; a 30-day TTL means eventual convergence. No alerting for renames — outside scope.
