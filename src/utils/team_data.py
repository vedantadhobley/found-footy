"""Team data management - pure Python, no orchestration dependencies"""

# Static team data
UEFA_25_2025 = {
    541: {"name": "Real Madrid", "country": "Spain", "rank": 1},
    157: {"name": "Bayern Munich", "country": "Germany", "rank": 2},
    505: {"name": "Inter Milan", "country": "Italy", "rank": 3},
    50: {"name": "Manchester City", "country": "England", "rank": 4},
    40: {"name": "Liverpool", "country": "England", "rank": 5},
    85: {"name": "Paris Saint-Germain", "country": "France", "rank": 6},
    168: {"name": "Bayer Leverkusen", "country": "Germany", "rank": 7},
    165: {"name": "Borussia Dortmund", "country": "Germany", "rank": 8},
    529: {"name": "Barcelona", "country": "Spain", "rank": 9},
    211: {"name": "Benfica", "country": "Portugal", "rank": 10},
    530: {"name": "Atletico Madrid", "country": "Spain", "rank": 11},
    910: {"name": "Roma", "country": "Italy", "rank": 12},
    49: {"name": "Chelsea", "country": "England", "rank": 13},
    42: {"name": "Arsenal", "country": "England", "rank": 14},
    169: {"name": "Eintracht Frankfurt", "country": "Germany", "rank": 15},
    33: {"name": "Manchester United", "country": "England", "rank": 16},
    499: {"name": "Atalanta", "country": "Italy", "rank": 17},
    209: {"name": "Feyenoord", "country": "Netherlands", "rank": 18},
    48: {"name": "West Ham United", "country": "England", "rank": 19},
    569: {"name": "Club Brugge", "country": "Belgium", "rank": 20},
    489: {"name": "AC Milan", "country": "Italy", "rank": 21},
    197: {"name": "PSV Eindhoven", "country": "Netherlands", "rank": 22},
    502: {"name": "Fiorentina", "country": "Italy", "rank": 23},
    228: {"name": "Sporting CP", "country": "Portugal", "rank": 24},
    47: {"name": "Tottenham Hotspur", "country": "England", "rank": 25}
}

FIFA_25_2025 = {
    26: {"name": "Argentina", "country": "Argentina", "rank": 1},
    9: {"name": "Spain", "country": "Spain", "rank": 2}
}

def get_team_data():
    """Get team data configuration"""
    return {
        "uefa": UEFA_25_2025,
        "fifa": FIFA_25_2025,
        "all": {**UEFA_25_2025, **FIFA_25_2025}
    }

def get_team_ids():
    """Get all team IDs"""
    all_teams = {**UEFA_25_2025, **FIFA_25_2025}
    return list(all_teams.keys())

def get_uefa_teams():
    """Get UEFA team data only"""
    return UEFA_25_2025

def get_fifa_teams():
    """Get FIFA team data only"""
    return FIFA_25_2025

def get_all_teams():
    """Get all team data"""
    return {**UEFA_25_2025, **FIFA_25_2025}

def get_team_by_id(team_id):
    """Get specific team by ID"""
    all_teams = get_all_teams()
    return all_teams.get(team_id)

def is_team_tracked(team_id):
    """Check if team ID is tracked"""
    return team_id in get_team_ids()
