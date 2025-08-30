⚽ Prefect Fixture Monitoring Architecture (with MongoDB)

This setup manages both live fixtures (future dates) and historical fixtures (backfilling/testing) while ensuring efficient bulk API polling.

🔹 MongoDB Collections

You only need two collections:

teams
{
  "_id": ObjectId,
  "team_id": <int>,          // from API
  "name": <string>,
  "league": <string>,        // optional, if you want grouping
  "created_at": <datetime>
}

fixtures
{
  "_id": ObjectId,
  "fixture_id": <int>,       // from API
  "date": <date>,            // match date
  "kickoff": <datetime>,     // kickoff time
  "teams": {
    "home": <team_id>,
    "away": <team_id>
  },
  "status": <string>,        // "scheduled", "live", "completed"
  "events": [                // embedded array of event docs
    {
      "time": <datetime>,
      "type": <string>,      // "goal", "card", "substitution", etc.
      "team": <team_id>,
      "player": <string>,    // optional
      "detail": <string>     // e.g. "Penalty", "Yellow Card"
    }
  ],
  "last_checked": <datetime>, // last time we polled API for this fixture
  "created_at": <datetime>,
  "updated_at": <datetime>
}


Notes:

Events can be embedded in the fixture to keep it simple (each monitor run diffs api_events vs stored events).

Alternatively, you could have a separate events collection if you want very granular querying — but embedding usually suffices here.

🔹 1. Parent Flow: fixtures_flow(date, teams)

Purpose:

Discover fixtures for a given date and set of teams.

Insert or update them in the fixtures collection.

Decide whether to start monitoring immediately (historical) or schedule it for kickoff (future).

Steps:

Fetch fixtures from API.

Upsert fixtures into MongoDB (fixtures collection).

If historical → run fixtures_flow_monitor(fixture_ids) immediately.

If future/live → schedule fixtures_flow_monitor(fixture_ids) to start at kickoff.

🔹 2. Subflow: fixtures_flow_monitor(fixture_ids)

Purpose:

Poll API every minute in bulk for all active fixture IDs.

Compare response to MongoDB.

Insert new events into the fixtures.events array.

Stop polling once all fixtures are marked completed.

Scheduling:

Attach a Prefect interval schedule (every 1 minute).

Each scheduled run does one poll.

Prefect UI shows each poll as its own run.

Graceful stop:

On each run, check the status field in MongoDB.

If all fixtures are "completed", cancel or pause the schedule so polling stops.

This can be done by:

Setting a “monitoring active” flag in fixtures.

Or calling Prefect’s API to cancel further scheduled runs.

🔹 3. Task: detect_new_events(api_data)

Purpose:

Compare API events against stored events in MongoDB.

Insert only new ones.

Logic:

For each fixture in api_data:

Load the existing events array from MongoDB.

Find any API events not present in DB.

$push new events into fixtures.events.

Update status field if match progresses (e.g. "live" → "completed").

🔹 Prefect Scheduling Objects
Interval schedule (1-minute polling)
from prefect.client.schemas.schedules import IntervalSchedule
from datetime import timedelta

IntervalSchedule(interval=timedelta(minutes=1))

RRule schedule (cron-like minute polling)
from prefect.client.schemas.schedules import RRuleSchedule
from dateutil.rrule import rrule, MINUTELY
from datetime import datetime

RRuleSchedule(rrule=rrule(MINUTELY, interval=1, dtstart=datetime.utcnow()))

Programmatically scheduling monitoring at kickoff
from prefect.deployments import run_deployment

run_deployment(
    name="fixtures_flow_monitor/deployment-name",
    parameters={"fixture_ids": [fixture_id]},
    scheduled_time=kickoff_time
)

🔹 Mermaid Flow
flowchart TD

    A[Start Parent Flow: fixtures_flow(date, teams)] --> B[Fetch fixtures from API]
    B --> C[Upsert fixtures into MongoDB.fixtures]

    C --> D{Is date historical?}
    D -- Yes --> E[Call fixtures_flow_monitor immediately with all fixture_ids]
    D -- No (future/live) --> F[Schedule fixtures_flow_monitor for each fixture at kickoff time]

    subgraph Monitoring Subflow: fixtures_flow_monitor
        G[Prefect IntervalSchedule: every 1 minute] --> H[Bulk fetch fixture data from API]
        H --> I[Task: detect_new_events - compare vs MongoDB]
        I --> J{All fixtures status = completed?}
        J -- No --> G
        J -- Yes --> K[Cancel/pause schedule → End Monitoring]
    end

    E --> G
    F --> G


✅ Key Points for Claude to Implement:

MongoDB collections: teams, fixtures (with embedded events).

fixtures_flow = discovery + scheduling.

fixtures_flow_monitor = scheduled polling loop (every 1 minute).

detect_new_events = MongoDB diff + insert.

Graceful stop = check fixture status, cancel schedule once completed.




i like the bulk write for mongo, but im getting the feeling that we dont even need the subflow called "fixtures_flow_monitor"

we can have a task that is scheduled to run each minute to fetch from the API and compare to the existing data in mongo (including if the fixture does not yet exist in mongo, which means it needs to instantly trigger twitter flows for each goal event)

the list of fixtures should be an object that exists in the fixtures_flow, and fixtures are added to this object dynamically with a scheduled task

this task is scheduled when fixtures_flow originally starts and makes its first batch api call to retrieve and 

this code really needs to be simplified and i think this is the way





i like the bulk write for mongo, but im getting the feeling that we dont even need the subflow called "fixtures_flow_monitor"

we're going to use 3 collections for our project:
staged_fixtures
live_fixtures
completed_fixtuers

we can have a task which does the mongodb comparison based on fetched fixtures called fixtures_task_fetch. this task will run once as soon as fixtures_flow starts and will store all fixture_ids in an object owned by the fixtures_flow

a second task will then be triggered (it needs to be after fixtures_task_fetch) which is scheduled by the fixtures_flow to run every minute

this method needs do an api call on the fixtures that were stored by fixtures_task_fetch and then has options based on comparisons to the mongodb for each fixture

if the fixture does not already exist in the fixtures collection and the game is already started/completed, then a twitter_flow needs to be triggered for each goal event and then the fixture needs to be added to the fixtures collection

if the fixture does exist in the fixtures collection, a diff needs to be done on the events for the fixture, each new goal event needs to trigger a twitter_flow and the fixture data needs to be updated in the fixtures collection

if the fixture is over the fixture needs to be updated in the fixtures collection (so that the fixture status reflects properly) and then fixure id needs to be dropped from the object that is tracking the current fixtures (the one that contains the api data from the )