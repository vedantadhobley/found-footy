# Python Functional Specification — found-footy

> **Frozen legacy behavior reference.** This describes the retired Python
> system, not production and not the Go architecture. Use it with `archive/`
> only when prior behavior or parity is relevant. Current authority starts at
> [`docs/README.md`](../../README.md).

## Preamble

This spec set describes **WHAT the Python found-footy system DOES**
— its behaviors, contracts, invariants, and edge cases — **NOT HOW
it's implemented, how to redesign it, or what bugs exist.** It's the
authoritative reference for the system's actual functional behavior,
useful for:

- **Go rewrite implementation**: Design against this spec, not
  against code archaeology
- **User/PM understanding**: What does the system actually guarantee,
  and when can it fail?
- **Testing**: Every claim here should be verifiable by a functional test
- **Design conversations**: Answer "does Python do X?" without reading
  code

This spec **complements** [`rebuild-plan.md`](../rebuild-plan.md),
which describes the *target* architecture. This spec captures the
*actual* behavior of the Python production system, workflow-by-
workflow, so the Go rewrite can be a faithful mechanical translation
of intent (with any deliberate divergences flagged in
[`decisions.md`](../../decisions.md)).

**Cross-references:** Numbered sections retain the original monolithic spec's
section numbers across the topic files below. When a section cites a file like
`archive/src/workflows/twitter_workflow.py:249-284`, that's a live
code location. When it says "UNCLEAR from code, would need to test,"
it means the code is ambiguous or the behavior is undocumented and
the Go rewrite team should determine intent from the user before
picking a behavior.

**Also**: some observations in this spec are marked `BUG?` — those
are things that read like defects to a fresh reader. They are NOT
proposals to fix in Python; they're behavior notes so the Go rewrite
can decide whether to preserve or correct.

---

## Core behavior topics

- [`system-and-data.md`](./system-and-data.md) — §§1–2, system overview and
  MongoDB schemas.
- [`ingest-and-monitor.md`](./ingest-and-monitor.md) — §§3–4, ingest and fixture
  monitoring.
- [`discovery.md`](./discovery.md) — §5, Twitter search and discovery.
- [`video-processing.md`](./video-processing.md) — §§6–7, download, validation,
  deduplication, upload, and asset persistence.
- [`completion-and-coordination.md`](./completion-and-coordination.md) — §§8–9,
  fixture completion and cross-workflow coordination.
- [`failures-and-edge-cases.md`](./failures-and-edge-cases.md) — §§10–11,
  recovery and corner behavior.
- [`configuration-and-dependencies.md`](./configuration-and-dependencies.md) —
  §§12–14, configuration, dependencies, and retention.
- [`observability-and-gaps.md`](./observability-and-gaps.md) — §§15–16,
  telemetry, known omissions, and the original summary.

## Detailed subsystem addendum

The 2026-07-18 addendum was produced from parallel deep reads as ground-truth
input for the August 15 rebuild roadmap. It preserves WHAT and WHY, including
the original `archive/` line references:

- [`twitter-service.md`](./twitter-service.md)
- [`upload-workflow.md`](./upload-workflow.md)
- [`hashing.md`](./hashing.md)
- [`vision.md`](./vision.md)
- [`scaler-and-consumer.md`](./scaler-and-consumer.md)
