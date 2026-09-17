# Frozen-clock policy screen — 2026-09-15

## Result

The isolated, boundary-aware policy changes no outcome in the replayable saved
sample. It exposes one necessary guard: a frozen timer loses exact elapsed
stoppage time, **not the period-ending lower bound**. A blanket downgrade to
unverified would soften an existing wrong-time rejection.

No natural accepted evaluation in this sample exercises the actual proposed
verified-to-unverified path. Zero observed impact is therefore limited coverage,
not proof that the policy cannot hide a valid historical clip. The production
parser, prompt, tolerance and deployment remain unchanged.

## Snapshot and replay coverage

The user approved testing the isolated policy before implementation. A
repeatable-read, read-only snapshot at **2026-09-15 17:26:54 UTC** exported
accepted raw evidence, retained clock-reject observations since August 31 and
current live-share read inputs. The program has no network, database or model
client. No model request or production mutation occurred.

| Evidence | Included | Limits |
|---|---:|---|
| Natural accepted evaluations | 159 | Four manual restoration evaluations excluded; raw accepted evidence only dates from the September 13 capture rollout |
| Clock-rejected candidate rows | 3,429 | 1,515 rows lack retained clock expectations; 13 have changed expectations; followers can repeat observations |
| Earlier saved-history observations | 108 | Thirteen deduplicated event/run exports from the existing corpus; separate source, not asserted disjoint from rejected rows |
| Current live-share candidates before FF-078 | 846 | Fixed current roots and scores; not all have original raw evidence |
| Currently visible shares after FF-078 | 545 | Only 47 have linked natural raw evidence in this snapshot |

All included baselines reproduce outcome, reason and matched minute using the
unchanged production evaluator. Accepted evidence also uses its original target
and tolerance; rejected evidence must reproduce its retained expected minute
and period before using current event fields. Baseline drift fails the report.

Four accepted evaluations use the old `frozen-boundary ... minute unpinned`
reason, but their supplied main clocks advance through `90:xx`. They belong
to the already established FF-094 running-clock defect, not this isolated policy.
The screen leaves those defective branches unchanged rather than attributing
their consequences to genuine frozen-clock handling.

## The counterexample: Nico Williams 46′

The September 5 rejection for event
`8b32351e-7429-4886-b758-63af2c5c4662`, fixture `1570364`, candidate
`c1c15353-8fb0-4ae6-93d3-9131216ce0bb`, retains three identical model readings:

- main clock `90:00`;
- explicit period `2H`;
- announced allowance `+2`;
- no secondary elapsed timer.

The saved target is completed minute 45, period 2H (API ordinal 46′).
This is a model-output counterexample, **not newly verified footage**.

| Control | Outcome | Reason |
|---|---|---|
| Production baseline | Rejected | Clock evidence does not match the target |
| Naive: omit unpinned minute evidence | Unverified | No remaining usable clock; this wrongly softens the contradiction |
| Preserve the boundary lower bound | Rejected | A second-half ending boundary cannot overlap the target window 44–46 |

The bounded rule is: when no independently matching usable reading exists,
`expected completed minute + tolerance < period end` remains a contradiction.
An unknown exact stoppage minute can be unverified only if the target is still
compatible with what the display establishes. The rule applies to H1, H2 and
both extra-time halves, including explicitly labeled reset-period boundaries.

This is not an allowance-derived upper bound. `+2` never caps the elapsed clock
or restricts stoppage to two minutes. The lower bound comes from the supported
period-ending clock, not the announced amount.

## Outcome and public projection

- Bounded control: all 159 natural accepted, 3,429 rejected and 108 saved-history
  outcomes stay unchanged. The naive control changes only the Nico rejection
  to unverified.
- No accepted evaluation contains the targeted frozen-compatible/no-secondary
  shape. The sole same-period candidate in included observations is Nico.
  A static raw reading is not proof that the original scorebug actually froze.
- No supported current-share reclassification, rank change or newly hidden clip
  appears. The fixed-root/fixed-score projection returns the same 545 shares.
  Sixty-six live shares have linked natural raw evidence; 47 are currently
  visible. Missing evidence for the other shares is **unknown**, not a no-change
  finding about their original validation.
- No dHash reselection or support-category recomputation was performed. Since
  no included accepted evaluation changes, the replay establishes no changed
  source category requiring that follow-up. Future category changes must assess
  direct-support and selection effects separately; stored scores are not fixed
  in the actual runtime.

## Regression and implementation boundary

The [offline program](../../../scripts/audit_frozen_clock/README.md) implements
two explicit controls, without copying or replacing the production evaluator.
It excludes only selected clock fields in a counterfactual copy while keeping
all raw observations in the report. Content votes, existing non-targeted
fallbacks, wrong-period behavior and ±1 tolerance stay unchanged.

Constructed tests cover allowances `+1`, `+2`, `+5`, `+10`; all four periods;
reset-period boundaries; real secondary timers; bare-minute exclusion; running
clock defects left untouched; missing clocks; content gates; a single usable
match; repeated and reversed readings; and decisive contradictory evidence.
The retained Nico regression is separate from those constructed cases.

Input tests pin drift rejection, explicit exclusions and no partial report.
Public-projection tests pin timestamp-priority loss, singleton hiding and stable
rank tie breaks. All tests passed with race detection for the offline program,
the unchanged vision domain and the existing clock-tolerance audit, using Go
1.25.11 in a network-disabled Docker container capped at 2 GiB and two CPUs.

The [FF-094 proposal](../proposals/clock-field-normalization.md) now explicitly
preserves boundary evidence. This screen does not implement that runtime policy,
repair double addition, fix false unpinning of running clocks, test a revised
prompt, authorize historical repair or prove complete release readiness.

## Reproduction artifacts

Ignored local artifacts under `scratch-audit-2026-09-15/`:

- `frozen-clock-evidence.ndjson` — approved snapshot; SHA-256
  `60e4635407ae97ac385d0f7d04a98ace07b610039d791d92f7939e20eaa7fdc3`.
- `saved-history-clock-evidence.ndjson` — allowlisted existing-history
  projection; SHA-256
  `5db24648d7fae7c0b091838defb00ce38b30ead46322533d925f12633c4216a7`.
- `frozen-clock-report.json` — complete controls and public projection; SHA-256
  `abdcf6cd1c398b2d629ab58a55c4a287e7e126c0095a97331db81eeb6a59adcf`.

The source history corpus remains checked in with SHA-256
`4d030ccdee0d1f99eee73a775f039303dd618a4bfe721c9604ced500b7de35a3`.
The small generated Nico fixture preserves its allowlisted raw model response
in the offline program's testdata without media, credentials or tweet payloads.
