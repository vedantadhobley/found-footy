"""Team data management using Prefect Variables - consistent with fixture_status.py pattern"""
import asyncio
import json
import nest_asyncio
import argparse
from prefect import get_client
from prefect.client.schemas.objects import Variable

# ✅ STATIC DATA - Same as team_variables_manager.py but structured for utils module
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
    9: {"name": "Spain", "country": "Spain", "rank": 2},
    2: {"name": "France", "country": "France", "rank": 3},
    10: {"name": "England", "country": "England", "rank": 4},
    6: {"name": "Brazil", "country": "Brazil", "rank": 5},
    27: {"name": "Portugal", "country": "Portugal", "rank": 6},
    1118: {"name": "Netherlands", "country": "Netherlands", "rank": 7},
    1: {"name": "Belgium", "country": "Belgium", "rank": 8},
    25: {"name": "Germany", "country": "Germany", "rank": 9},
    3: {"name": "Croatia", "country": "Croatia", "rank": 10},
    768: {"name": "Italy", "country": "Italy", "rank": 11},
    31: {"name": "Morocco", "country": "Morocco", "rank": 12},
    16: {"name": "Mexico", "country": "Mexico", "rank": 13},
    8: {"name": "Colombia", "country": "Colombia", "rank": 14},
    2384: {"name": "USA", "country": "USA", "rank": 15},
    7: {"name": "Uruguay", "country": "Uruguay", "rank": 16},
    12: {"name": "Japan", "country": "Japan", "rank": 17},
    13: {"name": "Senegal", "country": "Senegal", "rank": 18},
    15: {"name": "Switzerland", "country": "Switzerland", "rank": 19},
    22: {"name": "Iran", "country": "Iran", "rank": 20},
    21: {"name": "Denmark", "country": "Denmark", "rank": 21},
    775: {"name": "Austria", "country": "Austria", "rank": 22},
    17: {"name": "South Korea", "country": "South Korea", "rank": 23},
    20: {"name": "Australia", "country": "Australia", "rank": 24},
    2382: {"name": "Ecuador", "country": "Ecuador", "rank": 25}
}

async def create_team_data_variables():
    """Create Prefect Variables for team data management"""
    print("🚀 Creating team data variables...")
    
    async with get_client() as client:
        try:
            # UEFA Teams Variable
            uefa_variable = Variable(
                name="uefa_25_2025",
                value=json.dumps(UEFA_25_2025, indent=2),
                tags=["teams", "uefa", "clubs", "2025"]
            )
            
            await client.create_variable(uefa_variable)
            print("✅ Created variable: uefa_25_2025")
            
            # FIFA Teams Variable
            fifa_variable = Variable(
                name="fifa_25_2025", 
                value=json.dumps(FIFA_25_2025, indent=2),
                tags=["teams", "fifa", "national", "2025"]
            )
            
            await client.create_variable(fifa_variable)
            print("✅ Created variable: fifa_25_2025")
            
            # Team IDs Lists
            uefa_ids = list(UEFA_25_2025.keys())
            fifa_ids = list(FIFA_25_2025.keys())
            all_ids = uefa_ids + fifa_ids
            
            uefa_ids_variable = Variable(
                name="uefa_25_2025_ids",
                value=",".join(map(str, uefa_ids)),
                tags=["team-ids", "uefa", "deployment"]
            )
            
            fifa_ids_variable = Variable(
                name="fifa_25_2025_ids", 
                value=",".join(map(str, fifa_ids)),
                tags=["team-ids", "fifa", "deployment"]
            )
            
            all_ids_variable = Variable(
                name="all_teams_2025_ids",
                value=",".join(map(str, all_ids)),
                tags=["team-ids", "all", "deployment"]
            )
            
            await client.create_variable(uefa_ids_variable)
            await client.create_variable(fifa_ids_variable) 
            await client.create_variable(all_ids_variable)
            
            print("✅ Created team ID variables")
            print(f"📊 UEFA Teams: {len(UEFA_25_2025)} clubs")
            print(f"🌍 FIFA Teams: {len(FIFA_25_2025)} national teams")
            print(f"📋 Total Teams: {len(all_ids)} teams tracked")
            
        except Exception as e:
            if "already exists" in str(e):
                print("⚠️ Team variables already exist - use --update to update them")
            else:
                print(f"❌ Error creating team variables: {e}")

async def update_team_data_variables():
    """Update existing team data variables"""
    print("🔄 Updating team data variables...")
    
    async with get_client() as client:
        try:
            # Update UEFA and FIFA
            await client.set_variable(name="uefa_25_2025", value=json.dumps(UEFA_25_2025, indent=2))
            await client.set_variable(name="fifa_25_2025", value=json.dumps(FIFA_25_2025, indent=2))
            
            # Update ID lists
            uefa_ids = list(UEFA_25_2025.keys())
            fifa_ids = list(FIFA_25_2025.keys())
            all_ids = uefa_ids + fifa_ids
            
            await client.set_variable(name="uefa_25_2025_ids", value=",".join(map(str, uefa_ids)))
            await client.set_variable(name="fifa_25_2025_ids", value=",".join(map(str, fifa_ids)))
            await client.set_variable(name="all_teams_2025_ids", value=",".join(map(str, all_ids)))
            
            print("✅ All team data variables updated")
            
        except Exception as e:
            print(f"❌ Error updating team variables: {e}")

def get_team_data():
    """Synchronous helper to get team data"""
    try:
        return asyncio.run(get_team_data_async())
    except RuntimeError:
        import nest_asyncio
        nest_asyncio.apply()
        return asyncio.run(get_team_data_async())

async def get_team_data_async():
    """Get team data configuration from Prefect Variables"""
    try:
        async with get_client() as client:
            uefa_var = await client.read_variable_by_name("uefa_25_2025")
            fifa_var = await client.read_variable_by_name("fifa_25_2025")
            
            uefa_data = json.loads(uefa_var.value)
            fifa_data = json.loads(fifa_var.value)
            
            return {
                "uefa": uefa_data,
                "fifa": fifa_data,
                "all": {**uefa_data, **fifa_data}
            }
    except Exception as e:
        print(f"⚠️ Could not load team data from variables: {e}")
        # Fallback to hardcoded values
        return {
            "uefa": UEFA_25_2025,
            "fifa": FIFA_25_2025,
            "all": {**UEFA_25_2025, **FIFA_25_2025}
        }

def get_team_ids():
    """Get all team IDs from Prefect Variables"""
    try:
        return asyncio.run(get_team_ids_async())
    except RuntimeError:
        import nest_asyncio
        nest_asyncio.apply()
        return asyncio.run(get_team_ids_async())

async def get_team_ids_async():
    """Get team IDs from Prefect Variables"""
    try:
        async with get_client() as client:
            var = await client.read_variable_by_name("all_teams_2025_ids")
            team_ids = [int(x.strip()) for x in var.value.split(',')]
            return team_ids
    except Exception as e:
        print(f"⚠️ Could not load team IDs from variables: {e}")
        # Fallback to hardcoded values
        all_teams = {**UEFA_25_2025, **FIFA_25_2025}
        return list(all_teams.keys())

# ✅ CONVENIENCE FUNCTIONS
def get_uefa_teams():
    """Get UEFA team data only"""
    return get_team_data()["uefa"]

def get_fifa_teams():
    """Get FIFA team data only"""
    return get_team_data()["fifa"]

def get_all_teams():
    """Get all team data"""
    return get_team_data()["all"]

def get_team_by_id(team_id):
    """Get specific team by ID"""
    all_teams = get_all_teams()
    return all_teams.get(team_id)

def is_team_tracked(team_id):
    """Check if team ID is tracked"""
    return team_id in get_team_ids()

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Manage team data variables")
    parser.add_argument("--create", action="store_true", help="Create team data variables")
    parser.add_argument("--update", action="store_true", help="Update existing variables")
    args = parser.parse_args()
    
    if args.create:
        asyncio.run(create_team_data_variables())
    elif args.update:
        asyncio.run(update_team_data_variables())
    else:
        print("Usage: python team_data.py [--create|--update]")