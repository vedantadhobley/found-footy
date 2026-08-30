// Package fixture owns the fixture lifecycle: staging → active → completed
// state transitions and per-fixture polling metadata. Completed history is
// durable; bounded public reads are an adapter-level projection.
package fixture
