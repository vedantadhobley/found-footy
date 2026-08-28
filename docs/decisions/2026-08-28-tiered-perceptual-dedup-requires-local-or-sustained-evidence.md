# Tiered perceptual dedup requires local or sustained evidence

## Context

Production used one offset-tolerant dHash rule: at least 27 of 30 aligned
frames at per-frame Hamming distance 10 or less. That precision-first starting
point left reviewed re-encodes visible as separate clips. Raising the single
threshold did not produce a safe frontier.

Two manually reviewed production-v2 failures constrain the policy:

- N. Pierre 58′ contained two different fan-shot stadium videos. They first
  satisfy 27/30 at Hamming 15 and 45/50 at Hamming 19.
- Raphinha 37′ paired a direct goal clip with a longer tactical-analysis edit.
  The videos share broadcast frames but are distinct whole-video compositions.
  They first satisfy 27/30 at Hamming 14 and 45/50 at Hamming 18.

Frame-count evidence did not justify making five seconds the admission floor.
The primary three-second route remains useful for trimmed clips and for edits
that share a clean local passage without five clean seconds.

## Decision

Perceptual dedup matches same-category, same-hash-version sequences when either
of these offset-tolerant routes succeeds:

1. **Local route:** at least 27 of 30 frames have Hamming distance 12 or less.
2. **Sustained route:** at least 45 of 50 frames have Hamming distance 16 or
   less.

The 30-frame local window remains `HashVideo`'s minimum readable-hash admission
floor. A 30–49-frame sequence can use the local route; it is neither rejected
nor allowed to weaken the sustained route. Category scoping, exact-MD5
ownership, hash-version compatibility, and quality-winner selection do not
change.

All six thresholds come from the recorded `GetDiscoveryConfig` activity
result. Histories created before this decision retain their recorded local
rule and deserialize the new sustained fields as zero, which disables that
route. Fresh executions receive the new defaults. This preserves Temporal
replay without a workflow version marker.

## Evidence

The selected thresholds sit two Hamming bits below both reviewed Raphinha
failure boundaries. A production-derived 50-frame regression fixture pins that
pair: 12/30/3 and 16/50/5 must reject it, while 14/30/3 and 18/50/5 must
reproduce the unsafe boundary. Workflow tests separately prove that sustained
evidence can match a pair rejected by the local per-frame threshold and that a
historical zero-valued sustained route remains disabled.

## Consequences

- The safe failure direction remains an extra visible duplicate. This policy
  deliberately does not reach reviewed crop and screen-record pairs outside
  the selected boundary.
- Two `Match` scans can run for a same-category pair. Their integer operations
  remain negligible beside download, dense extraction, and vision.
- Partial-overlap semantics and keeper quality remain separate work. A correct
  duplicate decision can still retain a screen recording or player overlay
  because `IsUpgrade` sees duration, encoding density, and resolution—not
  editorial overlays or capture provenance.
- Changing these match thresholds does not change stored hashes or
  `FrameHashVersion`; no schema or asset migration is required. Existing
  assets are not retroactively consolidated.

## Superseded contract

This decision supersedes only the single 10/30/3 matcher policy recorded in
the pre-normalization archive and the statement in
[the bounded-working-image decision](./2026-08-17-dense-hashing-uses-versioned-bounded-working-image.md)
that matcher thresholds remained unchanged. Hash generation and version
isolation from that decision remain current.
