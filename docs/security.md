# Public repository security

These are source-publication controls, not production credential management.
They require Docker and Git, not a hosted scanning service or host-installed
scanner. See the [decision](./decisions/2026-10-21-public-source-security-and-references.md)
for the accepted boundary.

## Secret-scanning gates

The Makefile pins Gitleaks alongside the other engineering tools. Its default
detectors remain enabled. No project-wide test/vendor exclusions, baseline,
inline `gitleaks:allow` bypass, or fingerprint ignore file suppresses findings.
Project policy files cannot replace the defaults through the tree snapshot.

| Entry point | Source inspected |
|---|---|
| `make secrets-check` | Git-visible tracked and ordinary-untracked working files, through a temporary snapshot. Ignored files stay outside it. |
| `make secrets-check-staged` | The actual copied index versus HEAD, including a split index. A clean working replacement cannot hide a staged secret. |
| Installed pre-push hook | All outgoing commit patches, including intermediate commits, root commits, merge resolutions and non-checked-out branches. Only this destination's known history is excluded. |

Both `make check-short` and `make check` include the working-file scan. The commit
hook additionally scans the staged patch; the push hook consumes Git's supplied
ref list before running the full engineering gate. Ref deletion uploads nothing.

The scanner runs offline and read-only with a 512 MiB cap, two CPUs, dropped
capabilities and fully redacted output. Git scans mount only a temporary bare
repository, copied index metadata and read-only Git objects, not the working
tree, cookies, `.env`, SSH configuration or private recovery bundles. Exported
hook variables are cleared for foreign Git operations. Shallow or externally
shared object stores fail closed with a diagnostic.

Snapshots preserve Unicode/newline paths and do not recursively copy a nested
repository or gitlink, which could expose its ignored files. An unsupported
directory/special-file entry fails instead of being silently omitted.

## Acceptance and limits

`make test-secrets` runs the real pinned scanner against a disposable repository
with runtime-generated synthetic tokens. It verifies redaction, ignored state,
staged/working separation, split indices, exported hook variables, policy-file
bypasses, unusual paths, transient historical secrets, other branches, destination
isolation, merge-only secrets, shallow/shared object stores and deleted refs.
It makes no authenticated external requests.
The full `make check` includes this acceptance suite.

A scanner is heuristic, not proof that no credentials exist. Existing Git
history still includes the previously identified legacy RapidAPI value and a
realistic-looking Twitter-cookie example. The attribution rewrite did not remove
them. On October 21, read-only comparison confirmed that the historical API
value differs from configured values, and both historical Twitter-cookie values
differ from the persisted production cookies. No authenticated probe ran.
A different current value does not prove an old value is revoked; provider
revocation and account/session review remain separate owner actions. Do not
validate old credentials against live services or rotate production credentials
without the required authorization. Do not commit scan reports containing
unredacted values.

## Third-party references

The [API-Football reference](./api-football/) contains attributed integration
notes and official links. Full vendor exports are optional, ignored local files.
Their local bytes were preserved, not deleted; historical public Git copies
also remain. The project MIT license does not relicense those exports or footage.
