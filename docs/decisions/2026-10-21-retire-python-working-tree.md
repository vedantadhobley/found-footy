# Retired Python source leaves the working tree

## Decision

The user approved removing `archive/` on October 21. Python left production
at the August 15 Go cutover; no active build, test or runtime reads this tree.
Remove the retired source, its old configuration/documentation and generated
Python bytecode. Keep the independent Go research branches and the frozen
[Python functional specification](../design/python-functional-spec/) intact.

This supersedes the temporary archive-retention choice in the
[main-promotion decision](./2026-10-20-go-development-returns-to-main.md).
Keeping Python in Git is sufficient for historical comparison; carrying it in
the current checkout is not an architectural or recovery requirement.

## Historical source and retrieval

The complete pre-removal tree is preserved at Git commit
`bfcbc4247f6c95ab22e75027091b84310ec25438`. Browse its
[Python source and documentation](https://github.com/vedantadhobley/found-footy/tree/bfcbc4247f6c95ab22e75027091b84310ec25438/archive),
or inspect an individual file with ordinary Git:

```bash
git show bfcbc42:archive/src/activities/monitor.py
git show bfcbc42:archive/docs/architecture.md
```

Historical `archive/...` paths and line numbers in frozen reports or source
comments refer to this revision, not a current local directory. Markdown
source links use immutable revision URLs. The older `archive/python-main` tag
also remains available, but it preserves the previous Python-era main layout;
it is not the exact post-cutover `archive/` snapshot above.

## Boundaries and verification

This is source retirement, not a production deployment or data cleanup. Do not
stop containers, remove volumes, mutate databases/objects, replay workflows or
apply migrations. Python is not a supported rollback against current Go SQL,
Garage or Temporal state.

Remove obsolete local archive links and development exclusions; retain useful
historical citations with their revision context. Schema and immutable SQL
migration text stay byte-for-byte unchanged, including historical comments:
changing those comments would unnecessarily invalidate schema/checksum gates.
Go comment updates must not change executable declarations.

Validate references and the Git-visible package scope after deletion, then run
the standard fast engineering gate. No new inference, authenticated X requests,
database integration fixture or production operation is needed for this cleanup.
