// Package team owns the dynamic top-flight team-ID list used by
// IngestWorkflow's fixture filter. Team rosters are refreshed from
// API-Football's /teams endpoint once per TopFlightCacheHours and
// stored in tracked_teams_cache. Domain callers query IsTracked(id)
// or List() to filter global fixture responses down to what we care
// about.
//
// TrackedTeam is a thin projection of the API's team object plus the
// (league, season) provenance needed to reason about roster
// freshness. Full team profile info (venues, founding year, national
// vs club distinction) lives in the alias domain — this package
// exists purely for the ingest filter.
package team
