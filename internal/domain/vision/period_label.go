// period_label.go converts literal visible period labels into domain periods
// without consulting API time or other frame content.
package vision

import "strings"

// PeriodFromLabel recognizes explicit half and extra-time labels. A bare
// number, clock, allowance or unknown label supplies no period evidence.
func PeriodFromLabel(label string) (Period, bool) {
	text := strings.NewReplacer(
		"ˢ", "s", "ᵗ", "t", "ᵉ", "e", "ʳ", "r", "ᵈ", "d", "ⁿ", "n",
	).Replace(label)
	text = strings.ToUpper(strings.Join(strings.Fields(text), ""))
	switch text {
	case "1H", "H1", "1ST", "1RE", "1ER", "1P", "HALF1", "1STHALF":
		return PeriodFirstHalf, true
	case "2H", "H2", "2ND", "2E", "2P", "HALF2", "2NDHALF":
		return PeriodSecondHalf, true
	case "ET1", "1ET", "1STEXTRATIME":
		return PeriodExtraFirst, true
	case "ET2", "2ET", "2NDEXTRATIME":
		return PeriodExtraSecond, true
	default:
		return PeriodUnknown, false
	}
}

// framePeriodEvidence preserves the old normalized field for Temporal and
// evidence compatibility while making literal text authoritative when present.
func framePeriodEvidence(frame FrameObservation) (Period, bool, bool) {
	if frame.PeriodLabel != nil {
		literal, recognized := PeriodFromLabel(*frame.PeriodLabel)
		if frame.Period != nil {
			if !recognized || !frame.Period.Valid() || literal != *frame.Period {
				return PeriodUnknown, false, true
			}
		}
		return literal, recognized, false
	}
	if frame.Period == nil {
		return PeriodUnknown, false, false
	}
	if !frame.Period.Valid() {
		return PeriodUnknown, false, true
	}
	return *frame.Period, true, false
}
