// provider_health.go wires durable evidence into the guarded poll workflow.
package workflow

import (
	"github.com/google/uuid"
	"go.temporal.io/sdk/workflow"

	"github.com/vedantadhobley/found-footy/internal/activity/monitor"
	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
)

// providerObservationID derives a bounded stable receipt key from the complete
// execution identity. Temporal's test environment reuses its synthetic run ID,
// while production can reuse a workflow ID across runs; the pair is unique in
// both environments and the digest stays below the database length bound.
func providerObservationID(ctx workflow.Context) string {
	info := workflow.GetInfo(ctx).WorkflowExecution
	identity := info.ID + "\x00" + info.RunID
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity)).String()
}

// collectProviderHealth records all-failed polls as well as successful ones.
// Collector failure closes the provider-write gate for this poll.
func collectProviderHealth(ctx workflow.Context, in monitor.CollectProviderHealthInput, out *ActivePollWorkflowOutput) bool {
	var result providerintegrity.HealthResult
	if err := workflow.ExecuteActivity(ctx, "CollectProviderHealth", in).Get(ctx, &result); err != nil {
		workflow.GetLogger(ctx).Error("Provider health evidence collection failed", "error", err)
		out.Errors = append(out.Errors, "CollectProviderHealth: "+err.Error())
		return false
	}
	out.ProviderHealth = &result
	if len(result.Transitions) > 0 {
		workflow.GetLogger(ctx).Warn("Provider health policy evidence changed",
			"transitions", result.Transitions, "provider_policy", result.ProviderPolicy, "generation", result.Generation,
			"requested", result.Requested, "usable", result.Usable, "failed_attempts", result.FailedAttempts)
	}
	return true
}
