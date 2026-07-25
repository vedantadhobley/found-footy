# logging.md — Go rebuild ledger

**Purpose.** Emission contract for `internal/observability/logging/`
— what to import, how to call Emit, how to add a new (module, action)
pair, how to test.

Overview + design rationale live in [observability.md](./observability.md);
this doc is the "how do I emit a log line" reference.

## The single call site

```go
import (
    "github.com/vedantadhobley/found-footy/internal/observability/logging"
    "github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// somewhere in package code:
log.Emit(
    logging.LevelInfo,
    vocabulary.ModuleInfraPG,       // typed enum
    vocabulary.ActionPGPoolConnected, // typed enum
    "pg pool ready",
    logging.Int("max_conns", cfg.MaxConns),
    logging.Int("min_conns", cfg.MinConns),
    logging.String("connect_timeout", cfg.ConnectTimeout.String()),
)
```

`log` is a `logging.Emitter` obtained from `bootstrap.Deps.Log`. No
package-level loggers, no init-time construction, no `zap.L()`-style
globals. One import path, one injection point.

## Adding a new (module, action)

If your emit call needs a Module or Action that doesn't exist yet,
one line each:

**Module** — add to `internal/observability/vocabulary/vocabulary.go`
in the appropriate section (workflows / domain / adapters /
cross-cutting) and to the `ValidModules` slice.

**Action** — add to `actions_<family>.go`. Register via
`registerActions(...)` in the file's `init()`. If your action belongs
to a family that doesn't have an `actions_*.go` file yet, create one
following the shape of `actions_infra_pg.go`.

Compile error if you use an undeclared const. `IsKnownAction` runtime
check catches synthesized-string strays if any slipped through.

## Field helpers (typed, not `any`)

```go
logging.String(key, value string) Field
logging.Int(key string, value int) Field
logging.Int64(key string, value int64) Field
logging.Float64(key string, value float64) Field
logging.Bool(key string, value bool) Field
logging.Err(err error) Field    // sets error_class + error_message
```

`logging.Err(err)` produces both `error_class` (typed error class
name via reflection) and `error_message` (the `err.Error()` string)
in one call. Standard shape for adapter error paths.

## Testing (TestEmitter)

Every adapter's unit test uses `*logging.TestEmitter`:

```go
import "github.com/vedantadhobley/found-footy/internal/observability/logging"

func TestPGPool_LogsRegistration(t *testing.T) {
    log := &logging.TestEmitter{}
    reg := metrics.New()
    ins := pg.RegisterMetrics(reg, log)
    _, err := pg.New(ctx, cfg, ins)
    // assert err handling, then:
    require.Contains(t, log.Actions(), vocabulary.ActionPGPoolConnected)
}
```

TestEmitter captures every emission into a slice; helper methods
(`Actions()`, `Fields(action)`) make specific assertions ergonomic.
No real slog output during tests.

## Log-catalog generation — NOT SHIPPED

Plan §11.3 called for a `docs/generated/log-catalog.md` regenerated
on every build via `go generate`, listing every (module, action) pair
with expected fields and level guidance. **Not implemented.** Not
blocking anything today; useful when the emission surface grows
enough to make "grep the codebase" ergonomically painful. See
[decisions.md 2026-07-07](decisions.md).

## Cross-refs

- Overview + four-pillars framing — [observability.md](./observability.md)
- Vocabulary source — [`internal/observability/vocabulary/vocabulary.go`](../internal/observability/vocabulary/vocabulary.go)
- Adapter instrumentation template — [architecture.md § Adapters](./architecture.md#adapters--as-shipped-template)
