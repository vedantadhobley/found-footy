#!/usr/bin/env python3
"""Team variables management utility for Found Footy using Prefect Variables"""

import asyncio
import json
from prefect import get_client
from prefect.client.schemas.objects import Variable

# ✅ UEFA Top 25 Teams 2025 (with rank order)
UEFA_25_2025 = {
    541: {"name": "Real Madrid", "country": "Spain", "rank": 1},
    81: {"name": "Bayern Munich", "country": "Germany", "rank": 2},
    110: {"name": "Inter Milan", "country": "Italy", "rank": 3},
    50: {"name": "Manchester City", "country": "England", "rank": 4},
    42: {"name": "Liverpool", "country": "England", "rank": 5},
    85: {"name": "Paris Saint-Germain", "country": "France", "rank": 6},
    98: {"name": "Bayer Leverkusen", "country": "Germany", "rank": 7},
    83: {"name": "Borussia Dortmund", "country": "Germany", "rank": 8},
    529: {"name": "Barcelona", "country": "Spain", "rank": 9},
    211: {"name": "Benfica", "country": "Portugal", "rank": 10},
    530: {"name": "Atletico Madrid", "country": "Spain", "rank": 11},
    487: {"name": "Roma", "country": "Italy", "rank": 12},
    49: {"name": "Chelsea", "country": "England", "rank": 13},
    40: {"name": "Arsenal", "country": "England", "rank": 14},
    91: {"name": "Eintracht Frankfurt", "country": "Germany", "rank": 15},
    33: {"name": "Manchester United", "country": "England", "rank": 16},
    499: {"name": "Atalanta", "country": "Italy", "rank": 17},
    82: {"name": "Feyenoord", "country": "Netherlands", "rank": 18},
    48: {"name": "West Ham United", "country": "England", "rank": 19},
    121: {"name": "Club Brugge", "country": "Belgium", "rank": 20},
    489: {"name": "AC Milan", "country": "Italy", "rank": 21},
    79: {"name": "PSV Eindhoven", "country": "Netherlands", "rank": 22},
    502: {"name": "Fiorentina", "country": "Italy", "rank": 23},
    228: {"name": "Sporting CP", "country": "Portugal", "rank": 24},
    47: {"name": "Tottenham Hotspur", "country": "England", "rank": 25}
}

# ✅ FIFA Top 25 National Teams 2025 (with rank order)
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
    7: {"name": "Italy", "country": "Italy", "rank": 11},
    31: {"name": "Morocco", "country": "Morocco", "rank": 12},
    16: {"name": "Mexico", "country": "Mexico", "rank": 13},
    8: {"name": "Colombia", "country": "Colombia", "rank": 14},
    1569: {"name": "USA", "country": "United States", "rank": 15},
    28: {"name": "Uruguay", "country": "Uruguay", "rank": 16},
    15: {"name": "Japan", "country": "Japan", "rank": 17},
    13: {"name": "Senegal", "country": "Senegal", "rank": 18},
    14: {"name": "Switzerland", "country": "Switzerland", "rank": 19},
    22: {"name": "IR Iran", "country": "Iran", "rank": 20},
    21: {"name": "Denmark", "country": "Denmark", "rank": 21},
    17: {"name": "Austria", "country": "Austria", "rank": 22},
    24: {"name": "Korea Republic", "country": "South Korea", "rank": 23},
    32: {"name": "Australia", "country": "Australia", "rank": 24},
    5: {"name": "Ecuador", "country": "Ecuador", "rank": 25}
}

async def create_team_variables():
    """Create Prefect Variables for team tracking"""
    print("🚀 Creating Prefect Variables for team tracking...")
    
    async with get_client() as client:
        try:
            # ✅ UEFA Teams Variable
            uefa_variable = Variable(
                name="uefa_25_2025",
                value=json.dumps(UEFA_25_2025, indent=2),
                tags=["teams", "uefa", "clubs", "2025"]
            )
            
            await client.create_variable(uefa_variable)
            print("✅ Created variable: uefa_25_2025")
            
            # ✅ FIFA Teams Variable
            fifa_variable = Variable(
                name="fifa_25_2025", 
                value=json.dumps(FIFA_25_2025, indent=2),
                tags=["teams", "fifa", "national", "2025"]
            )
            
            await client.create_variable(fifa_variable)
            print("✅ Created variable: fifa_25_2025")
            
            # ✅ Team IDs Lists for easy deployment usage
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
            
            print("✅ Created team ID variables: uefa_25_2025_ids, fifa_25_2025_ids, all_teams_2025_ids")
            
            print("\n🎯 Variables Summary:")
            print(f"📊 UEFA Teams: {len(UEFA_25_2025)} clubs")
            print(f"🌍 FIFA Teams: {len(FIFA_25_2025)} national teams")
            print(f"📋 Total Teams: {len(all_ids)} teams tracked")
            
        except Exception as e:
            print(f"❌ Error creating variables: {e}")
            if "already exists" in str(e):
                print("⚠️ Variables may already exist. Use --update to replace them.")

async def update_team_variables():
    """Update existing Prefect Variables"""
    print("🔄 Updating Prefect Variables for team tracking...")
    
    async with get_client() as client:
        try:
            # Update UEFA
            await client.set_variable(name="uefa_25_2025", value=json.dumps(UEFA_25_2025, indent=2))
            await client.set_variable(name="fifa_25_2025", value=json.dumps(FIFA_25_2025, indent=2))
            
            # Update ID lists
            uefa_ids = list(UEFA_25_2025.keys())
            fifa_ids = list(FIFA_25_2025.keys())
            all_ids = uefa_ids + fifa_ids
            
            await client.set_variable(name="uefa_25_2025_ids", value=",".join(map(str, uefa_ids)))
            await client.set_variable(name="fifa_25_2025_ids", value=",".join(map(str, fifa_ids)))
            await client.set_variable(name="all_teams_2025_ids", value=",".join(map(str, all_ids)))
            
            print("✅ All team variables updated successfully")
            
        except Exception as e:
            print(f"❌ Error updating variables: {e}")

async def list_team_variables():
    """List all team-related variables"""
    print("📋 Team Variables:")
    
    async with get_client() as client:
        try:
            variables = await client.read_variables()
            team_variables = [v for v in variables if any(tag in v.tags for tag in ["teams", "team-ids"])]
            
            for var in team_variables:
                print(f"  🔗 {var.name} (tags: {', '.join(var.tags)})")
                if "ids" in var.name:
                    ids_count = len(var.value.split(",")) if var.value else 0
                    print(f"     📊 {ids_count} team IDs")
                else:
                    try:
                        data = json.loads(var.value)
                        print(f"     📊 {len(data)} teams")
                    except:
                        print(f"     📊 Value length: {len(var.value)}")
                        
        except Exception as e:
            print(f"❌ Error listing variables: {e}")

async def delete_team_variables():
    """Delete all team variables"""
    print("🗑️ Deleting team variables...")
    
    variable_names = [
        "uefa_25_2025", "fifa_25_2025", 
        "uefa_25_2025_ids", "fifa_25_2025_ids", "all_teams_2025_ids"
    ]
    
    async with get_client() as client:
        for name in variable_names:
            try:
                await client.delete_variable_by_name(name)
                print(f"✅ Deleted: {name}")
            except Exception as e:
                print(f"⚠️ Could not delete {name}: {e}")

def main():
    import argparse
    
    parser = argparse.ArgumentParser(description="Manage team variables in Prefect")
    parser.add_argument("--create", action="store_true", help="Create team variables")
    parser.add_argument("--update", action="store_true", help="Update existing variables")
    parser.add_argument("--list", action="store_true", help="List team variables")
    parser.add_argument("--delete", action="store_true", help="Delete team variables")
    
    args = parser.parse_args()
    
    if args.create:
        asyncio.run(create_team_variables())
    elif args.update:
        asyncio.run(update_team_variables())
    elif args.delete:
        asyncio.run(delete_team_variables())
    elif args.list:
        asyncio.run(list_team_variables())
    else:
        print("Usage: python team_variables_manager.py [--create|--update|--list|--delete]")
        print("\nExample:")
        print("  python team_variables_manager.py --create")

if __name__ == "__main__":
    main()