import requests
from datetime import date, datetime, timezone
from sqlalchemy import text, Column, Integer, String, DateTime
from sqlalchemy.orm import sessionmaker, declarative_base
from found_footy import engine

# Create Base if not imported from elsewhere
Base = declarative_base()

BASE_URL = "https://api-football-v1.p.rapidapi.com/v3"
HEADERS = {
    "x-rapidapi-key": "3815c39f56msh68991ec604f7be3p1cb7c4jsnf78a6cb47415",
    "x-rapidapi-host": "api-football-v1.p.rapidapi.com"
}

# Define the leagues
leagues = [
    (39, "Premier League"),
    (140, "La Liga"),
    (78, "Bundesliga"),
    (61, "Ligue 1"),
    (135, "Serie A")
    # Add more leagues as needed
]

def get_fixtures_by_date(query_date=date.today(), table="teams_2526"):
    # Query team_ids from Postgres
    with engine.connect() as conn:
        result = conn.execute(text(f"SELECT team_id FROM {table}"))
        team_ids = set(int(row[0]) for row in result.fetchall())

    fixture_url = f"{BASE_URL}/fixtures?date={query_date}"
    response = requests.get(fixture_url, headers=HEADERS)
    fixtures = response.json().get("response", [])

    selected_fixtures = []
    for fixture in fixtures:
        home_id = fixture["teams"]["home"]["id"]
        away_id = fixture["teams"]["away"]["id"]
        if home_id in team_ids or away_id in team_ids:
            selected_fixtures.append({
                "id": fixture["fixture"]["id"],
                "home": fixture["teams"]["home"]["name"],
                "away": fixture["teams"]["away"]["name"],
                "league": fixture["league"]["name"],
                "time": fixture["fixture"]["date"]
            })
    return selected_fixtures

def get_fixture_details(fixture_ids):
    url = f"{BASE_URL}/fixtures"
    querystring = {"ids": str(fixture_ids)}
    response = requests.get(url, headers=HEADERS, params=querystring)
    response.raise_for_status()
    return response.json().get("response", [])

def get_fixture_events(fixture_id):
    """Get all events for a specific fixture"""
    url = f"{BASE_URL}/fixtures/events"
    querystring = {"fixture": str(fixture_id)}
    response = requests.get(url, headers=HEADERS, params=querystring)
    response.raise_for_status()
    return response.json().get("response", [])

def store_fixture_result(fixture_data, table_name="fixtures_2526"):
    """Store completed fixture result in database"""
    Fixture = create_fixture_model(table_name)
    Base.metadata.create_all(engine, tables=[Fixture.__table__])
    
    Session = sessionmaker(bind=engine)
    session = Session()
    
    try:
        # Extract fixture data
        fixture_info = fixture_data["fixture"]
        teams_info = fixture_data["teams"]
        goals_info = fixture_data["goals"]
        league_info = fixture_data["league"]
        
        # Create or update fixture record
        fixture_record = Fixture(
            fixture_id=fixture_info["id"],
            home_team_id=teams_info["home"]["id"],
            home_team_name=teams_info["home"]["name"],
            away_team_id=teams_info["away"]["id"],
            away_team_name=teams_info["away"]["name"],
            home_goals=goals_info["home"],
            away_goals=goals_info["away"],
            status=fixture_info["status"]["short"],
            fixture_date=datetime.fromisoformat(fixture_info["date"].replace('Z', '+00:00')),
            league_id=league_info["id"],
            league_name=league_info["name"],
            completed_at=datetime.now(timezone.utc)  # Fixed timezone import
        )
        
        session.merge(fixture_record)  # Use merge to handle duplicates
        session.commit()
        
        print(f"✅ Stored fixture result: {teams_info['home']['name']} {goals_info['home']} - {goals_info['away']} {teams_info['away']['name']}")
        return True
        
    except Exception as e:
        session.rollback()
        print(f"❌ Error storing fixture result: {e}")
        return False
    finally:
        session.close()

def store_fixture_events(fixture_id, events_data, table_name="events_2526"):
    """Store fixture events in database - ONLY Goal events"""
    Event = create_event_model(table_name)
    Base.metadata.create_all(engine, tables=[Event.__table__])
    
    Session = sessionmaker(bind=engine)
    session = Session()
    
    try:
        goal_events_stored = 0
        
        for event in events_data:
            # Only store Goal events
            if event.get("type") == "Goal":
                # Extract event data
                time_info = event.get("time", {})
                team_info = event.get("team", {})
                player_info = event.get("player", {})
                assist_info = event.get("assist", {}) or {}
                
                # Create event record
                event_record = Event(
                    fixture_id=fixture_id,
                    time_elapsed=time_info.get("elapsed"),
                    time_extra=time_info.get("extra"),
                    team_id=team_info.get("id"),
                    team_name=team_info.get("name", "Unknown"),
                    player_id=player_info.get("id"),
                    player_name=player_info.get("name", "Unknown"),
                    assist_id=assist_info.get("id"),
                    assist_name=assist_info.get("name"),
                    event_type=event.get("type"),
                    event_detail=event.get("detail"),
                    comments=event.get("comments"),
                    created_at=datetime.now(timezone.utc)  # Fixed timezone import
                )
                
                session.merge(event_record)  # Use merge to handle duplicates
                goal_events_stored += 1
        
        session.commit()
        
        print(f"✅ Stored {goal_events_stored} goal events for fixture {fixture_id}")
        return goal_events_stored
        
    except Exception as e:
        session.rollback()
        print(f"❌ Error storing events for fixture {fixture_id}: {e}")
        return 0
    finally:
        session.close()

def get_teams_for_league(league_id, season):
    url = f"{BASE_URL}/teams?league={league_id}&season={season}"
    resp = requests.get(url, headers=HEADERS)
    data = resp.json()
    teams = []
    for team in data.get("response", []):
        teams.append({
            "id": team["team"]["id"],
            "name": team["team"]["name"],
        })
    return teams

def season_to_table_name(season):
    # season: 2024 -> 'teams_2425', 2025 -> 'teams_2526'
    short = str(season)[2:]
    next_short = str(season + 1)[2:]
    return f"teams_{short}{next_short}"

def populate_teams_table(season):
    table_name = season_to_table_name(season)
    Team = create_team_model(table_name)
    Base.metadata.create_all(engine, tables=[Team.__table__])
    Session = sessionmaker(bind=engine)
    session = Session()

    # Check if table already has data
    existing_count = session.query(Team).count()
    if existing_count > 0:
        print(f"Table {table_name} already populated with team data.")
        session.close()
        return

    for league_id, league_name in leagues:
        teams = get_teams_for_league(league_id, season)
        for team in teams:
            t = Team(
                team_id=team["id"],
                team_name=team["name"],
                league_id=league_id,
                league_name=league_name
            )
            session.merge(t)
    session.commit()
    session.close()
    print(f"Table {table_name} populated with team data.")

def create_team_model(table_name):
    class_name = f"Team_{table_name}"
    return type(class_name, (Base,), {
        '__tablename__': table_name,
        '__table_args__': {'extend_existing': True},  # Fix SQLAlchemy issue
        'team_id': Column(Integer, primary_key=True),
        'team_name': Column(String, nullable=False),
        'league_id': Column(Integer, nullable=False),
        'league_name': Column(String, nullable=False)
    })

def create_fixture_model(table_name):
    """Create a dynamic fixture model for the given table name"""
    class_name = f"Fixture_{table_name}"
    return type(class_name, (Base,), {
        '__tablename__': table_name,
        '__table_args__': {'extend_existing': True},  # Fix SQLAlchemy issue
        'fixture_id': Column(Integer, primary_key=True),
        'home_team_id': Column(Integer, nullable=False),
        'home_team_name': Column(String, nullable=False),
        'away_team_id': Column(Integer, nullable=False),
        'away_team_name': Column(String, nullable=False),
        'home_goals': Column(Integer, nullable=True),
        'away_goals': Column(Integer, nullable=True),
        'status': Column(String, nullable=False),
        'fixture_date': Column(DateTime, nullable=False),
        'league_id': Column(Integer, nullable=False),
        'league_name': Column(String, nullable=False),
        'completed_at': Column(DateTime, nullable=False)
    })

def create_event_model(table_name):
    """Create a dynamic event model for the given table name"""
    class_name = f"Event_{table_name}"
    return type(class_name, (Base,), {
        '__tablename__': table_name,
        '__table_args__': {'extend_existing': True},  # Fix SQLAlchemy issue
        'id': Column(Integer, primary_key=True, autoincrement=True),
        'fixture_id': Column(Integer, nullable=False),
        'time_elapsed': Column(Integer, nullable=True),
        'time_extra': Column(Integer, nullable=True),
        'team_id': Column(Integer, nullable=True),
        'team_name': Column(String, nullable=False),
        'player_id': Column(Integer, nullable=True),
        'player_name': Column(String, nullable=False),
        'assist_id': Column(Integer, nullable=True),
        'assist_name': Column(String, nullable=True),
        'event_type': Column(String, nullable=False),  # Will always be "Goal"
        'event_detail': Column(String, nullable=True),  # "Normal Goal", "Penalty", etc.
        'comments': Column(String, nullable=True),
        'created_at': Column(DateTime, nullable=False)
    })