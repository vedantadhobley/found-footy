// Tests for the selection pipeline. Builds real wikidata.Entity
// values by serving hand-crafted JSON through a small httptest server
// + the actual wikidata client. Verbose but avoids reaching into the
// wikidata package's private Entity constructor.
package alias_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/domain/alias"
	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

// entitySpec bundles the parts of a fake Wikidata entity that
// selection cares about. Passed to newTestClient to build a Client
// backed by an httptest server serving these entities keyed by QID.
type entitySpec struct {
	QID           string
	AliasesByLang map[string][]string
	LabelsByLang  map[string]string
	NicknamesEn   []string // P1449 values (all served as en monolingualtext)
	ClaimQIDs     map[string]string
	Demonyms      []langText // P1549 monolingualtext values
}

type langText struct {
	Lang string
	Text string
}

// newTestClient spins up an httptest server that serves the given
// entities via /wiki/Special:EntityData/... and returns a wikidata
// client pointed at it. The server is stopped by t.Cleanup.
func newTestClient(t *testing.T, entities ...entitySpec) *wikidata.Client {
	t.Helper()

	byQID := make(map[string]entitySpec, len(entities))
	for _, e := range entities {
		byQID[e.QID] = e
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract QID from the path: /wiki/Special:EntityData/{qid}.json
		const prefix = "/wiki/Special:EntityData/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		qid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), ".json")
		spec, ok := byQID[qid]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(specToJSON(t, spec))
	}))
	t.Cleanup(srv.Close)

	ins := wikidata.RegisterMetrics(metrics.New(), &logging.TestEmitter{})
	c, err := wikidata.NewClient(config.WikidataConfig{
		Endpoint:  srv.URL,
		WWWHost:   srv.URL,
		UserAgent: "found-footy-test",
		Timeout:   5 * time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// specToJSON serializes an entitySpec into the exact JSON shape the
// wikidata client's entity decoder expects.
func specToJSON(t *testing.T, s entitySpec) []byte {
	t.Helper()
	labels := map[string]any{}
	for lang, val := range s.LabelsByLang {
		labels[lang] = map[string]any{"language": lang, "value": val}
	}
	aliases := map[string]any{}
	for lang, vals := range s.AliasesByLang {
		list := make([]map[string]any, 0, len(vals))
		for _, v := range vals {
			list = append(list, map[string]any{"language": lang, "value": v})
		}
		aliases[lang] = list
	}
	claims := map[string]any{}
	if len(s.NicknamesEn) > 0 {
		nc := make([]map[string]any, 0, len(s.NicknamesEn))
		for _, n := range s.NicknamesEn {
			nc = append(nc, map[string]any{
				"mainsnak": map[string]any{
					"datatype":  "monolingualtext",
					"datavalue": map[string]any{"value": map[string]any{"text": n, "language": "en"}},
				},
			})
		}
		claims["P1449"] = nc
	}
	for prop, qidVal := range s.ClaimQIDs {
		claims[prop] = []map[string]any{{
			"mainsnak": map[string]any{
				"datatype":  "wikibase-item",
				"datavalue": map[string]any{"value": map[string]any{"id": qidVal}},
			},
		}}
	}
	if len(s.Demonyms) > 0 {
		dc := make([]map[string]any, 0, len(s.Demonyms))
		for _, d := range s.Demonyms {
			dc = append(dc, map[string]any{
				"mainsnak": map[string]any{
					"datatype":  "monolingualtext",
					"datavalue": map[string]any{"value": map[string]any{"text": d.Text, "language": d.Lang}},
				},
			})
		}
		claims["P1549"] = dc
	}
	envelope := map[string]any{
		"entities": map[string]any{
			s.QID: map[string]any{
				"labels":  labels,
				"aliases": aliases,
				"claims":  claims,
			},
		},
	}
	blob, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("specToJSON marshal: %v", err)
	}
	return blob
}

// TestSelect_ClubBasic — a synthetic club with a few multilingual
// aliases + P1449 nicknames. Verifies the pipeline output includes
// canonical + P1449 nicknames + cross-language tokens, and excludes
// skip-listed generic words.
func TestSelect_ClubBasic(t *testing.T) {
	c := newTestClient(t, entitySpec{
		QID: "Q_test_club",
		AliasesByLang: map[string][]string{
			"en": {"Liverpool F.C.", "The Reds", "LFC"},
			"es": {"Liverpool F. C.", "The Reds"},
			"fr": {"Liverpool"},
			"de": {"The Reds"},
			"pl": {"The Reds"},
		},
		LabelsByLang: map[string]string{"en": "Liverpool F.C."},
		NicknamesEn:  []string{"The Reds", "Scousers"},
	})
	r := alias.NewResolver(c, nil)

	aliases, err := r.Select(context.Background(), alias.SelectInput{
		QID:           "Q_test_club",
		IsNational:    false,
		CanonicalName: "Liverpool",
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	// Expected: liverpool (canonical + cross-lang), reds (cross-lang + P1449),
	// scousers (P1449), lfc (en-only rescue).
	// NOT expected: "the" (skip-list), "fc"/"f"/"c" (skip-list / ≤2 char).
	assertIncludes(t, aliases, "liverpool", "reds", "lfc", "scousers")
	assertExcludes(t, aliases, "the", "fc", "f", "c")
}

// TestSelect_ClubVenueCitySkip — Arsenal in London: "london" should be
// dropped because it's not a canonical name token.
func TestSelect_ClubVenueCitySkip(t *testing.T) {
	c := newTestClient(t, entitySpec{
		QID: "Q_arsenal",
		AliasesByLang: map[string][]string{
			"en": {"Arsenal Football Club", "AFC", "Arsenal London", "The Gunners"},
			"es": {"El Arsenal", "Gunners", "Arsenal London"},
			"fr": {"L'Arsenal", "Arsenal London"},
			"it": {"Gunners"},
			"de": {"Arsenal London"},
		},
		LabelsByLang: map[string]string{"en": "Arsenal F.C."},
		NicknamesEn:  []string{"The Gunners"},
	})
	r := alias.NewResolver(c, nil)

	london := "London"
	aliases, err := r.Select(context.Background(), alias.SelectInput{
		QID:           "Q_arsenal",
		IsNational:    false,
		CanonicalName: "Arsenal",
		VenueCity:     &london,
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	assertIncludes(t, aliases, "arsenal", "gunners")
	assertExcludes(t, aliases, "london")
}

// TestSelect_ClubVenueCityKeptWhenInCanonical — venue.city == team name.
func TestSelect_ClubVenueCityKeptWhenInCanonical(t *testing.T) {
	c := newTestClient(t, entitySpec{
		QID: "Q_liverpool",
		AliasesByLang: map[string][]string{
			"en": {"Liverpool F.C.", "LFC"},
			"es": {"Liverpool F. C."},
			"fr": {"Liverpool"},
			"de": {"The Reds"},
			"pl": {"The Reds"},
		},
		LabelsByLang: map[string]string{"en": "Liverpool F.C."},
		NicknamesEn:  []string{"The Reds"},
	})
	r := alias.NewResolver(c, nil)

	liverpool := "Liverpool"
	aliases, err := r.Select(context.Background(), alias.SelectInput{
		QID:           "Q_liverpool",
		IsNational:    false,
		CanonicalName: "Liverpool",
		VenueCity:     &liverpool,
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	assertIncludes(t, aliases, "liverpool", "reds")
}

// TestSelect_NationalBasic — national team with English demonyms via
// the country entity.
func TestSelect_NationalBasic(t *testing.T) {
	c := newTestClient(t,
		entitySpec{
			QID: "Q_argentina_team",
			AliasesByLang: map[string][]string{
				"en": {"Argentina national football team", "Argentina"},
				"fr": {"Albiceleste", "L'albiceleste"},
				"de": {"Albiceleste"},
				"pl": {"Albicelestes"},
			},
			LabelsByLang: map[string]string{"en": "Argentina"},
			NicknamesEn:  []string{"La Albiceleste"},
			ClaimQIDs:    map[string]string{"P17": "Q_argentina_country"},
		},
		entitySpec{
			QID: "Q_argentina_country",
			Demonyms: []langText{
				{Lang: "en", Text: "Argentine"},
				{Lang: "en", Text: "Argentinian"},
				{Lang: "en", Text: "Argentinean"},
				{Lang: "es", Text: "argentino"},
				{Lang: "fr", Text: "argentine"},
				{Lang: "de", Text: "Argentinier"},
			},
		},
	)
	r := alias.NewResolver(c, nil)

	aliases, err := r.Select(context.Background(), alias.SelectInput{
		QID:           "Q_argentina_team",
		IsNational:    true,
		CanonicalName: "Argentina",
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	// Expected: argentina, albiceleste, English demonyms.
	// NOT expected: foreign demonyms (argentino, argentinier).
	assertIncludes(t, aliases, "argentina", "albiceleste", "argentine", "argentinian", "argentinean")
	assertExcludes(t, aliases, "argentinier", "argentino")
}

// TestSelect_EmptyQID — fast-fail.
func TestSelect_EmptyQID(t *testing.T) {
	c := newTestClient(t)
	r := alias.NewResolver(c, nil)
	if _, err := r.Select(context.Background(), alias.SelectInput{QID: ""}); err == nil {
		t.Fatal("expected error for empty QID")
	}
}

// TestSelect_SortedOutput — output must be deterministic (sorted).
func TestSelect_SortedOutput(t *testing.T) {
	c := newTestClient(t, entitySpec{
		QID: "Q_sort",
		AliasesByLang: map[string][]string{
			"en": {"Zebra", "Alpha"},
			"fr": {"Zebra", "Alpha"},
			"de": {"Zebra", "Alpha"},
		},
		LabelsByLang: map[string]string{"en": "Zebra Alpha"},
	})
	r := alias.NewResolver(c, nil)

	aliases, err := r.Select(context.Background(), alias.SelectInput{
		QID:           "Q_sort",
		CanonicalName: "Zebra Alpha",
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if !sort.StringsAreSorted(aliases) {
		t.Errorf("output not sorted: %v", aliases)
	}
}

// assertIncludes fails if any expected token is missing from actual.
func assertIncludes(t *testing.T, actual []string, expected ...string) {
	t.Helper()
	set := make(map[string]struct{}, len(actual))
	for _, a := range actual {
		set[a] = struct{}{}
	}
	missing := []string{}
	for _, e := range expected {
		if _, ok := set[e]; !ok {
			missing = append(missing, e)
		}
	}
	if len(missing) > 0 {
		t.Errorf("missing expected tokens: %v\n  actual output: %v", missing, actual)
	}
}

// assertExcludes fails if any unwanted token appears in actual.
func assertExcludes(t *testing.T, actual []string, unwanted ...string) {
	t.Helper()
	set := make(map[string]struct{}, len(actual))
	for _, a := range actual {
		set[a] = struct{}{}
	}
	present := []string{}
	for _, u := range unwanted {
		if _, ok := set[u]; ok {
			present = append(present, u)
		}
	}
	if len(present) > 0 {
		t.Errorf("unwanted tokens present: %v\n  actual output: %v", present, actual)
	}
}

// TestSelect_AcronymRescue_PSGClass — the PSG regression case. Wikidata's
// English alias for PSG is "PSGFC" (no separator), so the base tokenizer
// produces the single token `psgfc`. Italian's alias "PSG F.C."
// tokenizes to `psg`, but that's only ONE language and would fail the
// ≥2-lang threshold on its own. The acronym-rescue augmentation
// surfaces the implicit `psg` prefix from English's PSGFC, giving it a
// count of 2 (English augmented + Italian natural). Threshold satisfied
// → `psg` survives.
func TestSelect_AcronymRescue_PSGClass(t *testing.T) {
	c := newTestClient(t, entitySpec{
		QID: "Q_psg",
		AliasesByLang: map[string][]string{
			"en": {"PSGFC", "Paris"},
			"it": {"PSG F.C.", "Paris-SG"},
			"de": {"Paris St. Germain"},
			"nl": {"Paris St. Germain"},
		},
		LabelsByLang: map[string]string{"en": "Paris Saint-Germain FC"},
	})
	r := alias.NewResolver(c, nil)

	aliases, err := r.Select(context.Background(), alias.SelectInput{
		QID:           "Q_psg",
		IsNational:    false,
		CanonicalName: "Paris Saint Germain",
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	// psg must be present — implicit from PSGFC + explicit in Italian.
	assertIncludes(t, aliases, "psg", "paris")
}

// TestSelect_AcronymRescue_DoesNotInventNYCFCTokens — the negative case.
// Wikidata's NYCFC data has "NYCFC" as an alias in many languages but no
// language has `nyc` as a standalone. The augmentation adds `nyc` to
// English's implicit set, but no other language has it → count of 1 →
// dropped by ≥2-lang threshold. We must NOT invent `nyc` from thin air.
func TestSelect_AcronymRescue_DoesNotInventNYCFCTokens(t *testing.T) {
	c := newTestClient(t, entitySpec{
		QID: "Q_nycfc",
		AliasesByLang: map[string][]string{
			"en": {"NYCFC", "New York City Football Club"},
			"de": {"NYCFC", "New York City Football Club"},
			"fr": {"NYCFC", "New York City Football Club"},
			"es": {"NYCFC", "New York City Football Club"},
		},
		LabelsByLang: map[string]string{"en": "New York City FC"},
	})
	r := alias.NewResolver(c, nil)

	aliases, err := r.Select(context.Background(), alias.SelectInput{
		QID:           "Q_nycfc",
		IsNational:    false,
		CanonicalName: "New York City",
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	// nycfc, new, york, city all fine. nyc must NOT appear — it exists
	// only as the augmented English prefix; no other language has it.
	assertExcludes(t, aliases, "nyc")
}

// TestSelect_AcronymRescue_DoesNotTouchShortForms — MUFC, AVFC, NUFC,
// CFC etc. must stay whole. Their 2-char prefixes (mu, av, nu) fall
// below the ≥3 char guard.
func TestSelect_AcronymRescue_DoesNotTouchShortForms(t *testing.T) {
	c := newTestClient(t, entitySpec{
		QID: "Q_manutd",
		AliasesByLang: map[string][]string{
			"en": {"MUFC", "Man Utd"},
			"de": {"MUFC"},
			"es": {"MUFC"},
		},
		LabelsByLang: map[string]string{"en": "Manchester United F.C."},
	})
	r := alias.NewResolver(c, nil)

	aliases, err := r.Select(context.Background(), alias.SelectInput{
		QID:           "Q_manutd",
		IsNational:    false,
		CanonicalName: "Manchester United",
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	// mufc kept whole. The 2-char stub "mu" must NOT appear (below
	// the ≥3 char prefix guard).
	assertIncludes(t, aliases, "mufc")
	assertExcludes(t, aliases, "mu")
}

// TestSelect_MultiWordCrossLanguageDedup — Wikidata contributors
// often copy verbatim multi-word aliases from one language into
// another (real 2026-07-23 example: Bayer 04 Leverkusen's German
// full name "Turn- und Sportverein Bayer 04 Leverkusen" listed as
// both an Italian and a Polish alias). Without dedup, tokens like
// `und` / `turn` / `sportverein` — monolingual German words — would
// pass the ≥2-lang threshold based on the copy-paste inflation.
//
// Rule: multi-word alias STRINGS get counted for exactly ONE
// language (sorted-alphabetically first). Single-word abbreviations
// (MUFC, RMCF, PSG) are NOT deduped — they're legitimately shared
// across languages by international football fans.
func TestSelect_MultiWordCrossLanguageDedup(t *testing.T) {
	c := newTestClient(t, entitySpec{
		QID: "Q_leverkusen",
		AliasesByLang: map[string][]string{
			// Same German multi-word phrase copied into 3 language slots.
			// Without the fix, `turn`/`und`/`sportverein` would be counted
			// in de+it+pl → 3 languages → passes ≥2-lang threshold.
			// With the fix, only one language (alphabetically-first: de)
			// gets credit → 1 language → fails threshold → dropped.
			"de": {"Turn- und Sportverein Bayer 04 Leverkusen"},
			"it": {"Turn- und Sportverein Bayer 04 Leverkusen"},
			"pl": {"Turn- und Sportverein Bayer 04 Leverkusen"},
			// Legitimate single-word abbreviation shared across languages
			// — MUST survive the dedup (still single-token, not deduped).
			"en": {"B04"},
			"es": {"B04"},
			"fr": {"B04"},
		},
		LabelsByLang: map[string]string{"en": "Bayer 04 Leverkusen"},
	})
	r := alias.NewResolver(c, nil)
	aliases, err := r.Select(context.Background(), alias.SelectInput{
		QID:           "Q_leverkusen",
		IsNational:    false,
		CanonicalName: "Bayer Leverkusen",
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	// Multi-word phrase tokens: only counted for 1 language now,
	// fail ≥2-lang threshold, dropped.
	assertExcludes(t, aliases, "turn")
	assertExcludes(t, aliases, "und")
	assertExcludes(t, aliases, "sportverein")
	// Single-word abbreviation: not deduped, still counted in 3
	// languages, passes threshold, kept.
	assertIncludes(t, aliases, "b04")
	// Canonical name tokens always survive regardless of threshold.
	assertIncludes(t, aliases, "bayer")
	assertIncludes(t, aliases, "leverkusen")
}
