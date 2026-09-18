// Competition presentation removes API-Football round parsing from consumers.
package fixturepresentation

import (
	"regexp"
	"strings"
)

// RoundKind is the normalized semantic class of a provider round string.
type RoundKind string

const (
	RoundKindUnknown       RoundKind = "unknown"
	RoundKindRegularSeason RoundKind = "regular_season"
	RoundKindFinal         RoundKind = "final"
	RoundKindOther         RoundKind = "other"
)

// Round is the provider-independent public projection of one round string.
type Round struct {
	Label string
	Kind  RoundKind
}

var regularSeasonRound = regexp.MustCompile(`(?i)^regular season\s*-\s*([0-9]+)$`)

// PresentRound keeps unknown competition vocabulary intact while normalizing
// the two meanings the product currently treats specially.
func PresentRound(raw string) Round {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Round{Kind: RoundKindUnknown}
	}
	if match := regularSeasonRound.FindStringSubmatch(trimmed); match != nil {
		return Round{Label: "Matchweek " + match[1], Kind: RoundKindRegularSeason}
	}
	if strings.EqualFold(trimmed, "final") {
		return Round{Label: "Final", Kind: RoundKindFinal}
	}
	return Round{Label: trimmed, Kind: RoundKindOther}
}

// CompetitionPriority preserves today's ascending API-Football competition
// order behind a producer-owned field. Consumers no longer depend on provider
// identity, and Found Footy can later change policy without a contract change.
func CompetitionPriority(leagueID int) int { return leagueID }
