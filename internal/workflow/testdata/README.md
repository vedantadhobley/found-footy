# Workflow regression evidence

- [Mastantuono bridge](./mastantuono-bridge.csv) — exact metadata and big-endian
  uint64 dHashes from the [September 11 incident](../../../docs/design/audits/mastantuono-bridge-removal-2026-09-11.md).
  A is first-goal footage, B is second-goal footage, and C directly matches
  both while A and B do not match. No media, credentials, or signed URLs are
  included. Popularity and supersession columns are incident evidence, not
  initial workflow state; tests construct their own arrival and recovery state.

Decode hashes from hex, never through JSON floating-point numbers. The
unmodified CSV SHA-256 is
`79ad3967a411d61b078b8414361ce83414ef5ada29fe6012efcb60edc8fc7adc`.
