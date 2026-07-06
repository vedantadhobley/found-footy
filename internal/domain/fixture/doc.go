// Package fixture owns the fixture lifecycle: staging → active → completed
// state transitions, per-fixture polling metadata, and the retention rule
// (prune completed > 14 days AND zero video_shares). Model + Store + Service
// per §4 fixture domain.
package fixture
