// fixture_contract.go validates the API-Football fixture response envelope
// before provider observations can reach ingest or live reconciliation.
package apifootball

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// ErrInvalidFixtureContract identifies a nominally successful fixtures
// response that violated the provider's documented wire contract.
var ErrInvalidFixtureContract = errors.New("apifootball: invalid fixture response contract")

// FixtureContractReason is a bounded machine-readable response defect. It is
// also the label value for the adapter's contract-failure counter.
type FixtureContractReason string

const (
	FixtureContractErrorsMissing       FixtureContractReason = "errors_missing"
	FixtureContractErrorsInvalid       FixtureContractReason = "errors_invalid"
	FixtureContractErrorsNonEmpty      FixtureContractReason = "errors_nonempty"
	FixtureContractResultsMissing      FixtureContractReason = "results_missing"
	FixtureContractResultsMismatch     FixtureContractReason = "results_mismatch"
	FixtureContractPagingMissing       FixtureContractReason = "paging_missing"
	FixtureContractPagingIncomplete    FixtureContractReason = "paging_incomplete"
	FixtureContractResponseMissing     FixtureContractReason = "response_missing"
	FixtureContractResponseInvalid     FixtureContractReason = "response_invalid"
	FixtureContractFixtureIdentity     FixtureContractReason = "fixture_identity_invalid"
	FixtureContractTeamIdentity        FixtureContractReason = "team_identity_invalid"
	FixtureContractNegativeScore       FixtureContractReason = "negative_score"
	FixtureContractEventsMissing       FixtureContractReason = "events_missing"
	FixtureContractEventsNull          FixtureContractReason = "events_null"
	FixtureContractEventTeamInvalid    FixtureContractReason = "event_team_invalid"
	FixtureContractRequestedDuplicate  FixtureContractReason = "requested_id_duplicate"
	FixtureContractReturnedDuplicate   FixtureContractReason = "returned_id_duplicate"
	FixtureContractReturnedUnrequested FixtureContractReason = "returned_id_unrequested"
	FixtureContractRequestedMissing    FixtureContractReason = "requested_id_missing"
)

// FixtureContractError carries one typed provider-contract failure without
// retaining or logging the full response body.
type FixtureContractError struct {
	Reason FixtureContractReason
	Detail string
}

// Error implements error.
func (e *FixtureContractError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("%v: %s", ErrInvalidFixtureContract, e.Reason)
	}
	return fmt.Sprintf("%v: %s: %s", ErrInvalidFixtureContract, e.Reason, e.Detail)
}

// Unwrap lets callers classify every reason through
// ErrInvalidFixtureContract while retaining errors.As access to Reason.
func (e *FixtureContractError) Unwrap() error { return ErrInvalidFixtureContract }

type fixtureEnvelope struct {
	Errors   json.RawMessage `json:"errors"`
	Results  *int            `json:"results"`
	Paging   *fixturePaging  `json:"paging"`
	Response json.RawMessage `json:"response"`
}

type fixturePaging struct {
	Current int `json:"current"`
	Total   int `json:"total"`
}

type fixtureEventsState uint8

const (
	fixtureEventsMissing fixtureEventsState = iota
	fixtureEventsNull
	fixtureEventsPresent
)

// UnmarshalJSON preserves whether the vendor omitted events, sent null, or
// sent an explicit array. A plain []APIFixtureEvent cannot distinguish the
// first two invalid by-ID cases from a legitimate empty event inventory.
func (f *APIFixture) UnmarshalJSON(data []byte) error {
	type fixtureAlias APIFixture
	var decoded fixtureAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	state := fixtureEventsMissing
	if raw, ok := fields["events"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			state = fixtureEventsNull
		} else {
			state = fixtureEventsPresent
		}
	}

	*f = APIFixture(decoded)
	f.eventsState = state
	return nil
}

func decodeFixtureEnvelope(envelope fixtureEnvelope, expectedIDs []int64, requireEvents bool) ([]APIFixture, error) {
	if err := validateFixtureErrors(envelope.Errors); err != nil {
		return nil, err
	}
	if envelope.Results == nil {
		return nil, fixtureContractError(FixtureContractResultsMissing, "")
	}
	if envelope.Paging == nil {
		return nil, fixtureContractError(FixtureContractPagingMissing, "")
	}
	if envelope.Paging.Current != 1 || envelope.Paging.Total != 1 {
		return nil, fixtureContractError(FixtureContractPagingIncomplete,
			fmt.Sprintf("current=%d total=%d", envelope.Paging.Current, envelope.Paging.Total))
	}
	rawResponse := bytes.TrimSpace(envelope.Response)
	if len(rawResponse) == 0 {
		return nil, fixtureContractError(FixtureContractResponseMissing, "")
	}
	if bytes.Equal(rawResponse, []byte("null")) {
		return nil, fixtureContractError(FixtureContractResponseInvalid, "null response")
	}
	var fixtures []APIFixture
	if err := json.Unmarshal(rawResponse, &fixtures); err != nil {
		return nil, fixtureContractError(FixtureContractResponseInvalid, err.Error())
	}
	if *envelope.Results != len(fixtures) {
		return nil, fixtureContractError(FixtureContractResultsMismatch,
			fmt.Sprintf("results=%d response=%d", *envelope.Results, len(fixtures)))
	}
	if err := validateFixturePayloads(fixtures, expectedIDs, requireEvents); err != nil {
		return nil, err
	}
	return fixtures, nil
}

func validateFixtureErrors(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return fixtureContractError(FixtureContractErrorsMissing, "")
	}
	var value any
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return fixtureContractError(FixtureContractErrorsInvalid, err.Error())
	}
	switch typed := value.(type) {
	case []any:
		if len(typed) == 0 {
			return nil
		}
	case map[string]any:
		if len(typed) == 0 {
			return nil
		}
	default:
		return fixtureContractError(FixtureContractErrorsInvalid,
			fmt.Sprintf("unexpected %T", value))
	}
	return fixtureContractError(FixtureContractErrorsNonEmpty, "provider returned errors")
}

func validateFixturePayloads(fixtures []APIFixture, expectedIDs []int64, requireEvents bool) error {
	expected := make(map[int64]struct{}, len(expectedIDs))
	for _, id := range expectedIDs {
		if _, duplicate := expected[id]; duplicate {
			return fixtureContractError(FixtureContractRequestedDuplicate, fmt.Sprintf("fixture_id=%d", id))
		}
		expected[id] = struct{}{}
	}

	returned := make(map[int64]struct{}, len(fixtures))
	for _, f := range fixtures {
		id := f.Fixture.ID
		if id <= 0 {
			return fixtureContractError(FixtureContractFixtureIdentity, fmt.Sprintf("fixture_id=%d", id))
		}
		if _, duplicate := returned[id]; duplicate {
			return fixtureContractError(FixtureContractReturnedDuplicate, fmt.Sprintf("fixture_id=%d", id))
		}
		returned[id] = struct{}{}
		if len(expected) > 0 {
			if _, requested := expected[id]; !requested {
				return fixtureContractError(FixtureContractReturnedUnrequested, fmt.Sprintf("fixture_id=%d", id))
			}
		}
		if f.Teams.Home.ID <= 0 || f.Teams.Away.ID <= 0 || f.Teams.Home.ID == f.Teams.Away.ID {
			return fixtureContractError(FixtureContractTeamIdentity,
				fmt.Sprintf("fixture_id=%d home=%d away=%d", id, f.Teams.Home.ID, f.Teams.Away.ID))
		}
		if negativeInt(f.Goals.Home) || negativeInt(f.Goals.Away) ||
			negativeScoreLine(f.Score.Halftime) || negativeScoreLine(f.Score.Fulltime) ||
			negativeScoreLine(f.Score.Extratime) || negativeScoreLine(f.Score.Penalty) {
			return fixtureContractError(FixtureContractNegativeScore, fmt.Sprintf("fixture_id=%d", id))
		}
		if requireEvents {
			switch f.eventsState {
			case fixtureEventsMissing:
				return fixtureContractError(FixtureContractEventsMissing, fmt.Sprintf("fixture_id=%d", id))
			case fixtureEventsNull:
				return fixtureContractError(FixtureContractEventsNull, fmt.Sprintf("fixture_id=%d", id))
			}
		}
		for _, providerEvent := range f.Events {
			if providerEvent.Team.ID != f.Teams.Home.ID && providerEvent.Team.ID != f.Teams.Away.ID {
				return fixtureContractError(FixtureContractEventTeamInvalid,
					fmt.Sprintf("fixture_id=%d event_team_id=%d", id, providerEvent.Team.ID))
			}
		}
	}
	for id := range expected {
		if _, present := returned[id]; !present {
			return fixtureContractError(FixtureContractRequestedMissing, fmt.Sprintf("fixture_id=%d", id))
		}
	}
	return nil
}

func negativeInt(value *int) bool { return value != nil && *value < 0 }

func negativeScoreLine(line APIFixtureScoreLine) bool {
	return negativeInt(line.Home) || negativeInt(line.Away)
}

func fixtureContractError(reason FixtureContractReason, detail string) error {
	return &FixtureContractError{Reason: reason, Detail: detail}
}

func (c *Client) recordFixtureContractFailure(ctx context.Context, err error) {
	reason := FixtureContractResponseInvalid
	var contractErr *FixtureContractError
	if errors.As(err, &contractErr) {
		reason = contractErr.Reason
	}
	c.ins.contractFailures.WithLabelValues(string(reason)).Inc()
	c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionAPIFootballFailed,
		"apifootball fixture contract rejected",
		logging.String("reason", string(reason)),
		logging.Err(err),
	)
}
