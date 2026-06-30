# docs/ — found-footy knowledge base

This directory is the project's **frozen knowledge layer** — everything an
agent or contributor needs to understand the system, beyond the running
code itself. The first stop is the project's front door (`AGENTS.md` /
`CLAUDE.md` at the repo root), which routes here for permanent reference
material.

Pair this with two other persistence mechanisms:

- **Code-level docstrings** — module headers + method docstrings carry
  *behaviour-of-this-specific-file* knowledge that's inseparable from the
  code. See `AGENTS.md` § "Documentation and docstrings" for the policy.
- **Per-agent auto-memory** — `~/.claude/projects/<project>/memory/` and
  analogous paths for other agents. Reserved for *user preferences and
  collaboration tone*. Project facts do NOT go here; they go in `docs/`.

If you're unsure where a thing belongs, see "Intake rules" below.

---

## What lives here

### Structural reference (read first)

- [`architecture.md`](./architecture.md) — the 5-collection MongoDB design,
  workflow hierarchy, video pipeline, scoped dedup, schema reference,
  per-activity inventory. The "how is the system shaped" doc.
- [`orchestration.md`](./orchestration.md) — fixture and event lifecycles,
  debouncing rules, VAR handling, the state machine. The "how do things
  move through the pipeline" doc.
- [`temporal.md`](./temporal.md) — per-activity timeouts, retries, and
  heartbeats. The "how is each workflow wired" doc.
- [`api-contract.md`](./api-contract.md) — the HTTP/SSE contract the
  vedanta-systems frontend depends on (Phase 6 work).

### Subsystem deep-dives

- [`logging.md`](./logging.md) — structured-JSON logging reference; module
  and action vocabulary; LogQL/Grafana query cookbook for found-footy in
  Loki. (For one-off forensics, query `monitor-loki:3100` directly; do
  NOT rely on `docker logs` — scaled-down workers vanish with their
  per-container json log file even though Loki retains the lines.)
- [`rag.md`](./rag.md) — Wikidata + LLM team-alias pipeline. Header note
  flags design-stage sections that don't reflect the current code.
- [`twitter-auth.md`](./twitter-auth.md) — browser automation, cookie
  lifecycle, VNC re-auth procedure.
- [`operations.md`](./operations.md) — runbook for bring-up, scaling,
  common failure modes, inspection queries.

### Time-anchored state

- [`decisions.md`](./decisions.md) — **append-only** architectural
  decisions log. Newest at top, ISO dates. When a decision changes, add
  a new entry above the old one pointing back at it; don't edit the old
  entry.
- [`todo.md`](./todo.md) — active work, open bugs, deferred items.
  Newest above older. Paste-ready start-of-session block at the top.
- [`roadmap.md`](./roadmap.md) — the committed multi-phase rewrite plan
  (current: 2026-Q2, 7 phases). Strategic direction. Phases reference
  back into `sprints.md` and `todo.md` for execution detail.
- [`sprints.md`](./sprints.md) — operational sprint board with
  per-session task lists. Subordinate to `roadmap.md`.
- [`audit.md`](./audit.md) — full code audit (May 2026). ~50 findings
  with file:line references. Sprints in `sprints.md` cite back here.

### Design docs (pre-implementation)

- [`proposals/`](./proposals/) — design docs for in-flight or planned
  feature work. Each file is a single proposal; the header note tells
  you whether it's accepted, in-progress, or rejected. Current set:
  `dedup-unification`, `event-matching`, `geo-restriction-bypass`,
  `llm-stack-redesign`.

---

## Intake rules — where new things go

When you learn something or decide something during a session, route it
as follows. This matches the rule in global `~/.claude/CLAUDE.md` and is
restated here so the docs and the rules about them live together.

| What you learned / decided                            | Where it goes                                        |
| ----------------------------------------------------- | ---------------------------------------------------- |
| Architectural decision (with date + rationale)        | [`decisions.md`](./decisions.md) (newest at top)     |
| Open bug, deferred work, follow-up item               | [`todo.md`](./todo.md) (newest above older)          |
| Structural fact about the system (schema, wiring)     | [`architecture.md`](./architecture.md) or a more specific subsystem doc |
| Operational procedure (how to do X in prod/dev)       | [`operations.md`](./operations.md)                   |
| Deploy / cross-project infra change                   | [`../deploy/INFRA-NOTES.md`](../deploy/INFRA-NOTES.md) |
| Cross-cutting design (multi-file, pre-implementation) | [`proposals/<name>.md`](./proposals/)                |
| Sprint task breakdown                                 | [`sprints.md`](./sprints.md)                         |
| Roadmap-level strategic direction                     | [`roadmap.md`](./roadmap.md)                         |
| Non-obvious file-local invariant or "why"             | a docstring in that file (NOT here)                  |
| User preference about collaboration tone              | per-agent memory (NOT here)                          |
| Cross-project preference                              | global `~/.claude/CLAUDE.md` (NOT here)              |

**Always include dates** on entries in `decisions.md`, `todo.md`, and
`audit.md`. ISO format (`2026-06-30`). When summarizing a session,
convert relative dates ("yesterday", "tomorrow") to absolute before
writing.

---

## After-session checklist

Before ending a substantive session, walk this list. The point is *not*
to write something in every category every session — most sessions touch
one or two. The point is to *consider* all six so nothing falls through.

1. **Decisions** — did we make any architectural choice that future-us
   should be able to find without digging through transcripts? → add to
   `decisions.md`.
2. **Open work** — is there a follow-up that won't happen this session? →
   add (or update an existing entry) in `todo.md`.
3. **Discovered facts** — did we learn a structural truth about the
   system (a schema, a wiring rule, an invariant)? → fold into the
   relevant subsystem doc; if no doc fits, add a section or a new file.
4. **Operational changes** — did we change how prod/dev is run, deployed,
   or recovered? → update `operations.md` or `deploy/INFRA-NOTES.md`.
5. **Stale entries** — does anything we touched now contradict an
   existing doc? → update or supersede the old entry (with a date)
   rather than leaving the conflict.
6. **Docstrings** — for any file we edited substantively, does the
   module header and the touched methods still match what they do? →
   adjust.
