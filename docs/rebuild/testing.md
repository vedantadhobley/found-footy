# testing.md — Go rebuild

**Phase F stub.** Populated during Phase D (as domain tests land) and
Phase T (synthetic harness backfill).

Target content:

- Three-tier pyramid: unit / integration / synthetic e2e per
  [`../rebuild-plan.md`](../rebuild-plan.md) §12
- Tier 1 (unit): factory conventions, hand-rolled mocks (not
  mockery-generated), pure-Go tests with mocked stores
- Tier 1.5 (workflow tests): `temporaltest.NewTestSuiteInstance` +
  `env.OnActivity` mocks — coverage floor per workflow
- Tier 2 (integration): testcontainers-go for Postgres, Garage, NATS —
  real backends, real network, real transactions
- Tier 3 (synthetic e2e): YAML scenarios that drive the full pipeline
  with recorded API-Football + Twitter + LLM responses
- Coverage floors per package
- Load-bearing invariant tests — URL stability, rank uniqueness (100
  concurrent goroutines stress), per-event upload serialization,
  NATS+event_log dual-write skew

Current design source of truth: [`../rebuild-plan.md`](../rebuild-plan.md)
§12 (testing).
