// Shared types between DiscoveryWorkflow (internal/workflow) and
// call sites that spawn it (currently monitor). Placed here to break
// the potential import cycle: internal/workflow imports monitor
// (for activities), so monitor can't import internal/workflow. Both
// import internal/activity/discovery instead.
package discovery

import "github.com/google/uuid"

// DiscoveryWorkflowInput carries everything Discovery needs from the
// spawning site (Monitor's ReconcileFixture activity). Same shape as
// the workflow's declared input; kept here so the spawner package
// can construct it without importing internal/workflow.
type DiscoveryWorkflowInput struct {
	EventID    uuid.UUID `json:"event_id"`
	FixtureID  int64     `json:"fixture_id"`
	PlayerName string    `json:"player_name"`
	TeamName   string    `json:"team_name"`
	TeamID     int64     `json:"team_id"`
	Minute     int       `json:"minute"`
}
