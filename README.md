# Found Footy - Football Goal Automation

## 🚀 Architecture Overview

### Flows
- **fixtures-flow**: Ingests fixtures and monitors for goals
- **twitter-flow**: Processes individual goals (triggered by automation)

### Collections (MongoDB)
- **teams**: Team metadata
- **fixtures_staging**: Future fixtures
- **fixtures_active**: Live/ongoing fixtures  
- **fixtures_processed**: Completed fixtures
- **goals_active**: Unprocessed goals
- **goals_processed**: Processed goals

### Deployments
1. **fixtures-flow-daily**: Scheduled daily runner (disabled by default)
2. **fixtures-flow-manual**: Manual runner with pre-filled date
3. **twitter-flow**: Goal processing (triggered by automation)

### Automation
- **goal-twitter-automation**: Triggers twitter-flow when goals are detected

## 🚀 Quick Start

```bash
# Deploy everything
python -m found_footy.flows.deployments --apply

# Test automation
python debug_events.py

# Access UI
http://localhost:4200
```

## 🎯 How It Works

1. **fixtures-flow** ingests fixtures and monitors for goals
2. When goals are detected, events are emitted
3. **goal-twitter-automation** catches goal events
4. **twitter-flow** processes individual goals
5. Goals/fixtures move through active → processed collections

## 📋 Parameters

### fixtures-flow
- `date_str`: Date in YYYYMMDD format (null = today)
- `team_ids`: Team IDs to monitor (null = all teams)

### twitter-flow  
- `goal_id`: Specific goal to process (provided by automation)

## 🔧 Development

```bash
# Clean deployments
python -m found_footy.flows.deployments --clean-only

# Deploy and run immediately
python -m found_footy.flows.deployments --apply --run-now
```