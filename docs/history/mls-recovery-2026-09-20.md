# MLS discovery recovery — 2026-09-20 UTC

This record preserves the approved `mls-recovery-20260919` production
operation. It is evidence for the deployed FF-101 replay path, not a current
runbook. Use the [operations ledger](../operations.md#completed-discovery-recovery)
for commands and safeguards.

## Scope

Sixteen completed MLS goal discoveries had no public video after a period of
model contention. Production remained on `d256c7e`; no service, schema,
configuration or media object was changed outside the normal replay workflow.
Each event used `found-footy-ops replay-event --mode recover`, which combined a
fresh bounded search with the typed transient-candidate retry allowlist.

The batch never ran more than two operator replays concurrently. Natural
discovery sometimes owned a third browser. Heavy retry sets were staggered so
only one large model burst entered admission at a time.

## Results

| Goal | Retried candidates | Unavailable probes | Final active shares | Outcome |
|---|---:|---:|---:|---|
| R. Roberts 90+8′ | 2 | 4 | 1 | assets surfaced |
| M. Ojeda 88′ | 0 | 3 | 0 | candidates, no assets |
| K. Yeboah 28′ | 0 | 0 | 1 | assets surfaced |
| João Klauss 76′ | 8 | 1 | 1 | assets surfaced |
| M. Reus 80′ | 11 | 1 | 1 | assets surfaced |
| Guilherme 38′ | 8 | 0 | 1 | assets surfaced |
| B. J. Ramirez Leon 73′ | 8 | 1 | 2 | assets surfaced |
| B. Damiani 64′ | 13 | 0 | 2 | assets surfaced |
| C. Sullivan 66′ | 32 | 0 | 2 | assets surfaced |
| Z. Bassong 73′ | 16 | 0 | 1 | assets surfaced |
| C. Holse 41′ | 6 | 1 | 1 | assets surfaced |
| T. Totland 54′ | 6 | 0 | 1 | assets surfaced |
| S. Becher 68′ | 2 | 0 | 1 | assets surfaced |
| B. White 9′ | 27 | 0 | 2 | assets surfaced |
| T. Muller 29′ | 14 | 0 | 1 | assets surfaced |
| S. Surridge 33′ | 5 | 1 | 2 | assets surfaced |

All sixteen checklists completed all 15 usable searches. The batch selected
158 typed transient candidates and ended with 20 active shares. Every event had
zero pending candidates at closure. Ojeda's three discovered candidates ended
in deterministic media-shape rejection: two portrait/narrow clips and one
2.2-second clip.

## Operational evidence

- X exhausted its 50-request window during the early replays. The affected
  workflows recorded `upstream_error`, preserved their usable-attempt counts,
  and resumed after the reset.
- Surridge recorded one isolated `unknown_timeout` with
  `timeline_failure=activity_error`; the next cadence recovered without
  operator action.
- Large retry sets drained successfully when their starts were staggered.
  Sullivan's 32 candidates fell to zero without a new timeout wave.
- Every scoped replay Firefox container and one-shot worker container was gone
  after closure. No manual candidate, checklist, share or media mutation was
  required.
