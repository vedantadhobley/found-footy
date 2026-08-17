# Score evidence gates goal removal and played-fixture completion

## Context

Lazio–Mantova fixture `1564801` exposed a false premise inherited from the
Python monitor and the rebuild plan: a goal missing from one provider event
array was treated as VAR after three absence votes. API-Football omitted I.
Cajazzo's 90+6 goal across several polls while retaining the 0–2 score. The
removal transaction closed the goal's downstream checklist, canceled discovery,
and allowed the fixture to complete with only one surviving goal.

The aggregate score and event array are two observations from the same provider
response. When they disagree, destructive removal must not trust the weaker
observation without qualification.

## Decision

For a stored goal absent from the current event array, `ReconcileFixture`
counts the response's non-shootout scoring events for the beneficiary team. If
the reported score is greater than that count, the score proves the array is
missing at least one goal. The activity records the held natural key in its
output and casts no absence vote. It conservatively holds every absent stored
goal for that team because the response does not identify which one was
omitted.

Normal absence debounce resumes when the score no longer requires a missing
goal. A real VAR therefore remains a three-vote transition after the score
drops. A replacement player identity also accounts for the score and permits
the old identity to decay. API-Football's captured own-goal behavior needs no
special case: `event.team` is the team that benefited.

Fixture completion requires three consecutive coherent terminal provider
responses. For played terminal statuses (`FT`, `AET`, and `PEN`), a response
advances `completion_counter` only when its own scoring-event array exactly
matches its reported per-team score. A non-terminal, nil-score, or inconsistent
played response resets the counter to zero. Winner flags remain stored
result/display facts and no longer bypass the counter.

After the counter reaches three, `FixtureReadyToComplete` separately requires
exact per-team equality between the reported score and all surviving stored
`event_type='goal'` rows. It also retains the event-settled and downstream-
workflow-complete predicates. Unknown-scorer goal placeholders count toward
score parity, while red cards, missed penalties, and shootout events do not.

Exceptional terminal statuses (`CANC`, `ABD`, `WO`, and `AWD`) retain their
existing completion path because they do not promise a played-match goal
inventory.

## Consequences

- A provider array omission cannot destroy a goal, its workflow, or its clips
  while the score still requires it.
- A played fixture cannot complete until the provider supplies three
  consecutive self-consistent terminal snapshots and stored events still
  explain the score.
- Responses with nil score data retain the existing three-vote fallback; they
  contain no aggregate evidence with which to override a goal absence, and
  they cannot advance played-fixture completion.
- If the score reflects a goal before the event array does, the fixture remains
  active until the provider reports a coherent terminal event set three times.
  The system does not fabricate an event or silently complete inconsistent
  data.
- Downstream completion remains a separate durable gate. It does not need three
  repeated observations, and failures in that lifecycle remain FF-002 and
  FF-007 rather than being hidden inside provider debounce state.

## Superseded contract

This decision supersedes the unconditional “missing event means removal
candidate” behavior in the historical
[`MonitorWorkflow` plan](../design/rebuild-plan.md#workflow-2-monitorworkflow)
and archived Python monitor. It extends the historical
[`fixture completion proposal`](../design/proposals/completion-contract.md).
It also supersedes that proposal's winner-data completion bypass.
Current behavior is recorded in the
[`orchestration ledger`](../orchestration.md) and tracked under
[`FF-014`](../history/issue-register-2026-08-17.md#ff-014--score-consistent-goal-is-false-removed-on-event-array-omission).
