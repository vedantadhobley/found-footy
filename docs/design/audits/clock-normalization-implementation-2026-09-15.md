# Clock normalization implementation — 2026-09-15

## Scope and status

FF-094 is implemented locally as `vision-evaluate-v2`, following the
[approved decision](../../decisions/2026-09-15-clock-fields-preserve-visible-evidence.md).
Production is unchanged. No database migration, historical repair, tolerance
change or extra inference call is part of this implementation.

## Historical replay

The actual new evaluator replayed the same September 15 17:26:54 UTC snapshot
used by the [isolated policy screen](./frozen-clock-policy-2026-09-15.md).
Recorded outcomes and raw observations remain immutable. The replay reports
current results separately; it does not claim v1 verdicts still reproduce.

| Evidence | Compared | Outcome changes |
|---|---:|---|
| Natural accepted evaluations | 159 | Three verified → rejected, all Adžić 90+4′ |
| Clock-rejected candidate rows | 3,429 | Twenty-three → unverified; three → verified |
| Saved-history observations | 108 | One rejected → verified: Wöber, overlapping rejected-candidate evidence |

Exclusions remain explicit: four manual evaluations, 1,515 rejected rows
without retained clock expectations, and thirteen changed expectations. Do not
sum history and candidate rows as unique videos; exact-byte followers repeat
observations. Older accepted evidence is missing, not presumed unchanged.

Interpretation of the changes:

- Three Adžić evaluations show running `90:xx` for expected completed minute
  93. The old allowance/secondary branches incorrectly verified them. One
  affected asset is currently selected in the snapshot: share `s_b3efc3f66372`,
  event `deb550ac-d037-4d30-934e-e35cc0d93429`, popularity eight. This is a
  historical review candidate, **not a repair authorization**.
- Twenty rejected rows change through repaired auxiliary composition:
  seventeen become unverified, one Damiani row verifies on its main timer,
  and two Wöber rows verify through the unchanged leading-digit rebase. These
  are the same broad control effects identified on September 14, not twenty
  proven lost goal videos. The Wöber history is the same observation family.
- Six more rows become unverified because their main timers are malformed:
  five Ramsey rows read `06:88`, and one Sinayoko row reads `23:08:06`.
  The old substring parser supplied a misleading minute; v2 supplies no usable
  clock. Existing no-clock admission still requires content gates. This is
  neither clock verification nor proof of event authenticity.
- Three Zhegrova acceptances stay verified, now using their running minute
  instead of the old unpinned fallback. Nico's 46′ case stays rejected with an
  explicit period-end lower-bound reason.

No naturally accepted record in this sample exercises a genuine frozen,
no-secondary downgrade. No full dHash graph/category-support projection or
public reorder was performed. Existing v1 assets remain unchanged on deploy,
including exact-byte reuse; historical repair requires separate review.

## Fixed-image prompt test

The [benchmark](../../../scripts/benchmark_clock_prompt/README.md) made eight
sequential requests to the published Control gateway using the catalog's real
`gemma-4-12b` ID. Both variants used the existing response schema, no reasoning,
and model-owned sampling defaults. Each preselected sample received one call
per variant, without retrying for a preferred result. All eight requests
returned valid three-frame responses in 8.994–10.806 seconds.

| Sample | Literal-only wording | Bounded cross-frame wording |
|---|---|---|
| Maitland-Niles original 95:04/15/27, +5′ | Main clocks correct; allowance omitted | Main clocks correct; +5 in `added`, secondary null |
| Fernández original 45:17/25/33, +1 | Main clocks correct; allowance omitted | Main clocks correct; +1 in `added`, secondary null |
| Farrington original, only first clock visible | First clock 29:53; second null; invented bare 35 in third | Same observation errors; no cross-frame digit copying observed |
| JVC documented 45:00 / 1:23 / +2 | All three timing roles correct | Main/secondary correct; allowance missing in first response frame |

Both prompts avoided fabricated secondary `05:00`/`01:00` on the real allowance
examples. The bounded-context prompt retains more allowance evidence there and
remains the default. It does not promise perfect literal transcription: it
omits the visible prime after +5 and loses one diagram allowance.

The JVC input is a rendered region of page 5 from the vendor manual, repeated
three times. It is **not natural match footage or advancing secondary-clock
coverage**. Both prompts incorrectly mark this graphics-only diagram as
`soccer=true`; this is a content-gate limitation, not a production incident.
The Farrington third frame was visually rechecked: no scorebug is visible.
Its invented `35` has unknown origin and does not change this sample's rejection.
These errors remain under FF-003; no new guessing or content bypass is added.

The unchanged parser tests still cover real Lauberbach secondary-timer
observations. Natural frozen-secondary and reset-half prompt coverage remains
limited. One run per prompt is not a comparative error-rate or latency study.

Fingerprints:

- Literal prompt: `f9450f175ca83eddb9a59ef84a5c7b53adf62844329d30e975d05fc1e927bb8d`
- Context/default: `7b80787118e7ec3f8114a1a8bab2a431831b792bc228cb544fc3e5ee9f38b0e7`
- Unchanged schema: `00f178fc55b7fbd3a407eb83dbf28b1bed5d70b4d574ffd980bd9e7a34be763c`
- Manifest: `e927390512f62b5787697de7dc3d82a747707d1348db6fd9097baed1738c19c1`

## Verification and artifacts

Domain, activity and workflow race suites pass. Tests cover every supported
period-ending display, malformed seconds, compact double-addition prevention,
allowances of different lengths, missing/bare clocks, single exact matches,
reversed/repeated frames, independent contradictions and old/new evidence
serialization. Old accepted evidence remains valid even when v2 would reject
its raw observations. Workflow tests verify old rejections acquire no invented
evaluator field.

The original tolerance and frozen-control modes still fail closed on baseline
drift. Explicit `--replay-current` modes compare across versions while retaining
the original verdict. The old history corpus is unchanged; tests now assert
the single Wöber policy difference separately from tolerance changes.

Ignored scratch contains two unrelated standalone `main` programs in one
directory, which prevents an unfiltered workspace `go test ./...` build.
Release verification uses a source-only copy of tracked and nonignored files;
scratch evidence is retained, not deleted or silently included in the release.
The source-only copy passes all short tests with race detection, `go vet`,
the pinned golangci-lint gate (zero issues), `go build`, formatting and
`go mod tidy -diff`. No schema changed, so no new database integration run was
required. All touched documentation's local links resolve. The historical
replay repeated byte-for-byte after the final code review.

Local artifacts under `scratch-audit-2026-09-15/`:

- `clock-v2-replay.json`: current evaluator versus original outcomes.
- `clock-prompt-manifest.json`: preselected local image identities/provenance.
- `clock-prompt-results.ndjson`: every response, timing and input fingerprint,
  followed by the explicit eight-call completion marker.

Report checksums:

- Replay: `80910339f8f8185d6035a0f706f82aa7372e36b9879ce59898d6ae0aeae1167f`
- Prompt responses: `741c20a151bca6f8cc5c69e6df875545d3b385af8060924956963935db49da6a`
- Unchanged v1 history corpus: `4d030ccdee0d1f99eee73a775f039303dd618a4bfe721c9604ced500b7de35a3`

The public impact of historical repair is deliberately not inferred from these
verdict changes. Future validation must also observe natural v2 outcomes after
a separately approved rollout.
