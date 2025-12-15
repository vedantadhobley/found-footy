"""Team data management - pure Python, no orchestration dependencies"""

# Static team data
TOP_UEFA = {
    541: {"name": "Real Madrid", "country": "Spain", "rank": 1},
    157: {"name": "Bayern Munich", "country": "Germany", "rank": 2},
    50: {"name": "Manchester City", "country": "England", "rank": 3},
    85: {"name": "Paris Saint-Germain", "country": "France", "rank": 4},
    529: {"name": "Barcelona", "country": "Spain", "rank": 5},
    40: {"name": "Liverpool", "country": "England", "rank": 6},
    530: {"name": "Atletico Madrid", "country": "Spain", "rank": 7},
    42: {"name": "Arsenal", "country": "England", "rank": 8},
    33: {"name": "Manchester United", "country": "England", "rank": 9},
    165: {"name": "Borussia Dortmund", "country": "Germany", "rank": 10},
    49: {"name": "Chelsea", "country": "England", "rank": 11},
    496: {"name": "Juventus", "country": "Italy", "rank": 12},
    497: {"name": "Roma", "country": "Italy", "rank": 13},
    505: {"name": "Inter Milan", "country": "Italy", "rank": 14},
    47: {"name": "Tottenham Hotspur", "country": "England", "rank": 15}

    # 168: {"name": "Bayer Leverkusen", "country": "Germany", "rank": 8},
    # 211: {"name": "Benfica", "country": "Portugal", "rank": 10},
    # 169: {"name": "Eintracht Frankfurt", "country": "Germany", "rank": 15},
    # 499: {"name": "Atalanta", "country": "Italy", "rank": 17},
    # 209: {"name": "Feyenoord", "country": "Netherlands", "rank": 18},
    # 48: {"name": "West Ham United", "country": "England", "rank": 19},
    # 569: {"name": "Club Brugge", "country": "Belgium", "rank": 20},
    # 489: {"name": "AC Milan", "country": "Italy", "rank": 21},
    # 197: {"name": "PSV Eindhoven", "country": "Netherlands", "rank": 22},
    # 502: {"name": "Fiorentina", "country": "Italy", "rank": 23},
    # 228: {"name": "Sporting CP", "country": "Portugal", "rank": 24},
}

TOP_FIFA = {
    9: {"name": "Spain", "country": "Spain", "rank": 1},
    26: {"name": "Argentina", "country": "Argentina", "rank": 2},
    2: {"name": "France", "country": "France", "rank": 3},
    10: {"name": "England", "country": "England", "rank": 4},
    6: {"name": "Brazil", "country": "Brazil", "rank": 5},
    27: {"name": "Portugal", "country": "Portugal", "rank": 6},
    1118: {"name": "Netherlands", "country": "Netherlands", "rank": 7},
    1: {"name": "Belgium", "country": "Belgium", "rank": 8},
    25: {"name": "Germany", "country": "Germany", "rank": 9},
    3: {"name": "Croatia", "country": "Croatia", "rank": 10},
    31: {"name": "Morocco", "country": "Morocco", "rank": 11},
    768: {"name": "Italy", "country": "Italy", "rank": 12},
    8: {"name": "Colombia", "country": "Colombia", "rank": 13},
    2384: {"name": "USA", "country": "USA", "rank": 14},
    16: {"name": "Mexico", "country": "Mexico", "rank": 15}
    # 7: {"name": "Uruguay", "country": "Uruguay", "rank": 16},
    # 12: {"name": "Japan", "country": "Japan", "rank": 17},
    # 13: {"name": "Senegal", "country": "Senegal", "rank": 18},
    # 15: {"name": "Switzerland", "country": "Switzerland", "rank": 19},
    # 22: {"name": "Iran", "country": "Iran", "rank": 20},
    # 21: {"name": "Denmark", "country": "Denmark", "rank": 21},
    # 775: {"name": "Austria", "country": "Austria", "rank": 22},
    # 17: {"name": "South Korea", "country": "South Korea", "rank": 23},
    # 20: {"name": "Australia", "country": "Australia", "rank": 24},
    # 2382: {"name": "Ecuador", "country": "Ecuador", "rank": 25}
}

def get_team_data():
    """Get team data configuration"""
    return {
        "uefa": TOP_UEFA,
        "fifa": TOP_FIFA,
        "all": {**TOP_UEFA, **TOP_FIFA}
    }

def get_team_ids():
    """Get all team IDs"""
    all_teams = {**TOP_UEFA, **TOP_FIFA}
    return list(all_teams.keys())

def get_uefa_teams():
    """Get UEFA team data only"""
    return TOP_UEFA

def get_fifa_teams():
    """Get FIFA team data only"""
    return TOP_FIFA

def get_all_teams():
    """Get all team data"""
    return {**TOP_UEFA, **TOP_FIFA}

def get_team_by_id(team_id):
    """Get specific team by ID"""
    all_teams = get_all_teams()
    return all_teams.get(team_id)

def is_team_tracked(team_id):
    """Check if team ID is tracked"""
    return team_id in get_team_ids()
