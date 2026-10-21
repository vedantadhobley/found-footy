# Twitter admission research probes

Test-only evidence for the [FF-038 proposal](../../docs/design/proposals/twitter-shared-admission.md).
This is not the runtime controller or scheduler. No production credentials,
model calls, authenticated X traffic or application-state changes are needed.

## What runs by default

`make check-short` discovers this package. Only the in-process browser
characterization runs; real-server/browser probes require explicit opt-in.
The characterization pins a current defect: a rendered candidate followed by
429 or 503 pagination is currently returned as a usable `rendered` result.
Replace that assertion with the target partial-result invariant when fixing
the runtime path. It is not the intended search policy.

## Private Temporal probe

Use the secret-free source snapshot helper. The Compose stack has a private
internal network, no published ports, no application networks/data/cookies,
explicit memory limits, and disposable PostgreSQL storage. Dependencies must
already exist in the shared cache under `$HOME/.cache/found-footy`; the probe
cannot download modules. The cache layout matches the root Makefile.

From the repository root:

```sh
task_snapshot=$(mktemp -d /tmp/found-footy-ff038-source.XXXXXX)
./scripts/go-scope.sh snapshot "$task_snapshot"
test ! -e "$task_snapshot/.env"
test ! -e "$task_snapshot/.git"
export FF038_SOURCE_DIR="$task_snapshot"
docker compose --project-name ff038-research \
  -f scripts/probe_twitter_admission/compose.yml \
  up --abort-on-container-exit --exit-code-from probe probe
```

Collect the probe exit code. Afterward, remove **only this research stack**:

```sh
docker compose --project-name ff038-research \
  -f scripts/probe_twitter_admission/compose.yml down --volumes
```

The client hard-codes `temporal:7233` and `ff038-research`; do not add a
production-address override. The toy actor has two credits, named idempotent
receipts, worker replay, and history rotation. It does not implement fairness,
quota-header policy, browser leases or the eventual scheduler.

October 15 result: Temporal reported 1.24.3. Signal/receipt retry, replacement
caller, replacement worker and Continue-As-New passed without double debit.
One hundred local calls measured median 49.697 ms, p95 52.419 ms and maximum
56.660 ms. Workflow Updates were disabled in this disposable namespace's
default configuration. This does not assert production's namespace setting.

## Real Go/Firefox probe

Compile the test executable in the pinned Go image using the existing caches:

```sh
docker run --rm --memory 2g --memory-swap 2g --cpus 2 \
  -e GOCACHE=/gocache -e GOMODCACHE=/gomodcache -e CGO_ENABLED=0 \
  -v "$PWD:/src:ro" \
  -v "$HOME/.cache/found-footy/gocache:/gocache" \
  -v "$HOME/.cache/found-footy/gomodcache:/gomodcache" \
  -v /tmp:/out -w /src golang:1.25.11-bookworm \
  go test -buildvcs=false -c -o /out/ff038-browser-probe.bin \
  ./scripts/probe_twitter_admission
```

Then run against the retained immutable Twitter image, without network access
outside the container or cookie/profile mounts:

```sh
docker run --rm --network none --memory 1g --memory-swap 1g \
  --cpus 2 --shm-size 256m --security-opt seccomp=unconfined \
  -e FF038_DISPOSABLE_BROWSER=1 \
  --mount type=bind,src=/tmp/ff038-browser-probe.bin,dst=/probe.test,readonly \
  --entrypoint /probe.test \
  found-footy-prod-twitter:55c2afd32355dd0cc24bd0a1454e65f79fafee80 \
  -test.run TestFirefoxPausedRoute -test.v -test.timeout 30s
```

October 15 result: passed in 1.77 seconds. A paused route did not reach the
local upstream; other Go driver calls remained responsive; three continuations
preserved the synthetic cursor; cancellation prevented a fourth send.

This proves a local short suspension seam, not long-lived X page continuation,
live service-worker coverage, routing/cache overhead, rate-limit scope, or
production latency. Never substitute this test for those acceptance gates.
