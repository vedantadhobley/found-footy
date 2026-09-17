// Decode only vision inputs/results from saved Temporal json/plain payloads.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"

	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// payloads deliberately supports only the unencrypted exported JSON contract.
type payloads struct {
	Payloads []struct {
		Metadata map[string][]byte `json:"metadata"`
		Data     []byte            `json:"data"`
	} `json:"payloads"`
}

// decode rejects missing/encrypted/multiple payloads instead of guessing evidence.
func (p payloads) decode(target any) error {
	if len(p.Payloads) != 1 || string(p.Payloads[0].Metadata["encoding"]) != "json/plain" {
		return fmt.Errorf("expected one json/plain payload")
	}
	return json.Unmarshal(p.Payloads[0].Data, target)
}

type historyEvent struct {
	ID    string `json:"eventId"`
	Start *struct {
		RunID        string                `json:"originalExecutionRunId"`
		WorkflowID   string                `json:"workflowId"`
		WorkflowType struct{ Name string } `json:"workflowType"`
		Input        payloads              `json:"input"`
	} `json:"workflowExecutionStartedEventAttributes"`
	Scheduled *struct {
		ActivityType struct{ Name string } `json:"activityType"`
		Input        payloads              `json:"input"`
	} `json:"activityTaskScheduledEventAttributes"`
	Completed *struct {
		ScheduledID string   `json:"scheduledEventId"`
		Result      payloads `json:"result"`
	} `json:"activityTaskCompletedEventAttributes"`
}

type eventContext struct {
	EventID   string `json:"event_id"`
	FixtureID int64  `json:"fixture_id"`
	Player    string `json:"player_name"`
	Team      string `json:"team_name"`
}

type visionInput struct {
	EventID    string
	FixtureID  int64
	StagingKey string
	APIElapsed *int
	APIExtra   *int
}

type visionOutput struct {
	Outcome       dvision.Outcome
	Reason        string
	MatchedMinute *int
	Frames        []dvision.FrameObservation
}

type savedObservation struct {
	ScheduleID string
	Input      visionInput
	Output     visionOutput
}

type historyInput struct {
	Source       string
	SHA256       string
	RunID        string
	Context      eventContext
	Scheduled    int
	Observations []savedObservation
}

// readHistory bounds local input size and retains a checksum, not raw payloads.
func readHistory(path string) (historyInput, error) {
	file, err := os.Open(path) // #nosec G304 -- explicit operator-supplied local evidence file.
	if err != nil {
		return historyInput{}, err
	}
	defer func() { _ = file.Close() }()
	const maxBytes = 64 << 20
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return historyInput{}, err
	}
	if len(data) > maxBytes {
		return historyInput{}, fmt.Errorf("history exceeds 64 MiB")
	}
	result, err := decodeHistory(data)
	result.Source = path
	result.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	return result, err
}

// decodeHistory joins completions to their own scheduled input, not today's event.
func decodeHistory(data []byte) (historyInput, error) {
	var history struct{ Events []historyEvent }
	if err := json.Unmarshal(data, &history); err != nil {
		return historyInput{}, err
	}
	var result historyInput
	schedules := make(map[string]visionInput)
	ids := make(map[string]bool)
	completed := make(map[string]bool)
	for _, event := range history.Events {
		if event.ID == "" || ids[event.ID] {
			return result, fmt.Errorf("missing or repeated history event ID")
		}
		ids[event.ID] = true
		if start := event.Start; start != nil {
			if result.RunID != "" || start.RunID == "" || start.WorkflowType.Name != "EventWorkflow" {
				return result, fmt.Errorf("expected one identified EventWorkflow start")
			}
			if err := start.Input.decode(&result.Context); err != nil {
				return result, err
			}
			if result.Context.EventID == "" || result.Context.FixtureID <= 0 || start.WorkflowID != "event-"+result.Context.EventID {
				return result, fmt.Errorf("workflow/event identity mismatch")
			}
			result.RunID = start.RunID
		}
		if scheduled := event.Scheduled; scheduled != nil && scheduled.ActivityType.Name == "ValidateClip" {
			var input visionInput
			if err := scheduled.Input.decode(&input); err != nil {
				return result, err
			}
			if input.EventID != result.Context.EventID || input.FixtureID != result.Context.FixtureID ||
				input.StagingKey == "" || input.APIElapsed == nil || input.APIExtra == nil {
				return result, fmt.Errorf("incomplete or mismatched vision input at %s", event.ID)
			}
			schedules[event.ID] = input
		}
		if done := event.Completed; done != nil {
			input, ok := schedules[done.ScheduledID]
			if !ok {
				continue // Other activity results are deliberately never decoded.
			}
			if completed[done.ScheduledID] {
				return result, fmt.Errorf("repeated completion for %s", done.ScheduledID)
			}
			completed[done.ScheduledID] = true
			var output visionOutput
			if err := done.Result.decode(&output); err != nil {
				return result, err
			}
			result.Observations = append(result.Observations, savedObservation{done.ScheduledID, input, output})
		}
	}
	if result.RunID == "" {
		return result, fmt.Errorf("missing EventWorkflow start")
	}
	result.Scheduled = len(schedules)
	return result, nil
}
