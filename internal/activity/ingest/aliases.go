// Canonical team-alias placeholder activity.
package ingest

import (
	"context"
	"fmt"

	"github.com/vedantadhobley/found-footy/internal/domain/alias"
)

// EnsureAliasPlaceholdersInput carries team references collected during
// categorization.
type EnsureAliasPlaceholdersInput struct {
	Teams []TeamRef
}

// EnsureAliasPlaceholdersOutput counts existing (already-cached) vs
// newly-inserted (placeholder) rows. Errors carries per-team context
// strings for anything that failed inside the loop but didn't fail
// the activity — aggregated into the workflow's top-level Errors.
type EnsureAliasPlaceholdersOutput struct {
	Existing int
	Inserted int
	Errors   []string
}

// EnsureAliasPlaceholders BulkGets existing alias rows for each
// team ID; for teams without a cached row, inserts a placeholder
// (canonical vendor fields only). The retired resolution columns remain nil;
// there is no production alias-resolution job.
//
// TeamCode isn't in TeamRef yet — passed as nil for the placeholder
// (no active consumer needs it). Add it to
// TeamRef when a future consumer needs it at Ingest time.
func (a *Activities) EnsureAliasPlaceholders(ctx context.Context, in EnsureAliasPlaceholdersInput) (EnsureAliasPlaceholdersOutput, error) {
	out := EnsureAliasPlaceholdersOutput{}
	if len(in.Teams) == 0 {
		return out, nil
	}

	ids := make([]int, 0, len(in.Teams))
	for _, t := range in.Teams {
		ids = append(ids, t.TeamID)
	}
	existing, err := a.AliasRepo.BulkGet(ctx, ids)
	if err != nil {
		return out, fmt.Errorf("ingest.EnsureAliasPlaceholders: BulkGet: %w", err)
	}

	now := a.now()
	for _, t := range in.Teams {
		if _, hasIt := existing[t.TeamID]; hasIt {
			out.Existing++
			continue
		}
		ta := alias.New(t.TeamID, t.TeamName, t.IsNational, nil, t.Country, t.City, now)
		if err := a.AliasRepo.UpsertVendorFields(ctx, ta); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("alias upsert team=%d: %v", t.TeamID, err))
			continue
		}
		out.Inserted++
	}
	return out, nil
}
