# Public source security and third-party references

## Decision

Add automatic secret checks to the existing local engineering gates. Use the
pinned, Dockerized Gitleaks default detectors rather than a new hosted service,
custom detector framework or broad allowlist. Scan staged data before commits
and outgoing history before pushes; a clean final tree must not hide a secret
committed earlier in the same push. Keep historical credential debt separate
from new-source enforcement. The [security ledger](../security.md) owns the
as-built commands, isolation, acceptance and remaining credential limitations.

Make the Git-visible snapshot NUL-safe and reject recursive directory copies.
Clearing foreign Git environment and avoiding full working-tree mounts prevents
the security tool itself from touching a source ref or reading ignored state.
Use explicitly fake fixture values rather than disabling generic key detection.

## Vendor material

The October 21 review found no explicit full-document redistribution grant in
the [API-Football terms](https://www.api-football.com/terms),
[API-Sports terms](https://api-sports.io/terms), or the saved documentation HTML.
The terms' API-data restrictions do not by themselves prove that documentation
mirroring is prohibited. We do not treat absence of a grant as a legal verdict
or assume that project MIT licensing grants vendor rights.

Stop tracking exactly the v3.9.3 HTML and PDF under
`docs/api-football/vendor/`, retaining their existing local bytes unchanged.
Ignore that directory and retain attributed project notes plus official links.
Raw exports are optional local references, never build or test prerequisites.
Frozen provenance remains historical evidence. This removes current public-tree
copies, not historical copies; no additional history purge is authorized here.

This changes repository tooling and publication only. It does not deploy code,
change runtime behavior, test live credentials, rotate secrets or modify
production state.
