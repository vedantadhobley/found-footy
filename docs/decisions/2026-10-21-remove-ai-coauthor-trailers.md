# Remove historical AI co-author trailers

## Decision

The user explicitly approved an attribution-only history rewrite on October 21,
after reviewing its effect on descendant commit IDs, branches, tags and historical
links. Remove only the Claude co-author trailer from these two commit messages:

| Original commit | Rewritten commit |
|---|---|
| `f17b2bbf32ce82d97c97582f32ba3553fc1e41a3` | `9f79f53ab76fd1dfbb925a1c577578742f4efc31` |
| `7d5429d18f8fc9c08fa790e068e50e39872bf493` | `9e8ed27eec8e52f8127da64c5717caf48cd6edfa` |

All file trees, author and committer identities/dates, other messages, empty
commits and parent ordering remain unchanged. Descendants receive new parent
IDs. The unsigned annotated archive tag retains its message and tagger metadata
and points to the equivalent rewritten commit. Ordinary future changes still
use new commits; this explicitly approved rewrite is not a routine cleanup step.

## Verification and recovery

Before changing refs, a private bundle captured all refs and reflog-reachable
objects. A manifest retained branch/tag tips, all three stash entries, and stale
detached-worktree HEADs. The isolated rewrite verified every commit's raw bytes
against the allowed changes, identical tree IDs, unchanged topology and commit
counts, and Git object integrity. An independent review checked the full map.

The local backup is under `.git/attribution-backup.<suffix>/`: `before.bundle`,
`manifest.json`, the original stash reflog, `rewrite.json`, and `commit-map.tsv`.
It also holds a verified rewritten bundle. Keep this backup private; it contains
unpublished work and the original historical objects. No garbage collection,
stash deletion, branch deletion or worktree pruning is part of this operation.

The [public revision map](../history/attribution-rewrite-2026-10-21.tsv) contains
changed IDs reachable from published history and the approved repository cleanup.
The private map additionally covers unpublished research and stash history.
Do not publish those branches or recovery bundles merely to synchronize history.

The six affected remote refs are `main`, `rebuild/go`, `audit/project-design`,
`feat/event-update`, `feature/event-matching`, and `archive/python-main`.
Update them together with an atomic push and explicit old-object leases, after
the normal pre-push gate. Any changed remote tip rejects the whole push.

Existing clones must preserve their local work before resynchronizing. Use the
revision map to locate equivalent new bases; do not merge an old branch back
into the rewritten history. This checkout's local branches and saved stashes
have already been mapped without changing their file contents.

## Release and documentation boundaries

Production images, running processes, Temporal history and durable application
data do not change. Historical release IDs remain the actual original deployed
IDs; do not relabel images or rewrite operational evidence to match new Git IDs.
The public map links those IDs to equivalent source commits.

Current Python-source links now use `bfcbc4247f6c95ab22e75027091b84310ec25438`,
whose tree is identical to the original `4c9971b` snapshot. Other old commit URLs
may eventually stop resolving on GitHub; use the map or the private backup rather
than assuming server retention of unreachable objects.

GitHub contributor displays may lag the rewritten history by about 24 hours;
see [GitHub's contributor-cache guidance](https://docs.github.com/en/repositories/viewing-activity-and-data-for-your-repository/viewing-a-projects-contributors#contributor-data-is-stale-after-history-changes).
