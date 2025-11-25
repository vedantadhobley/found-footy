#!/usr/bin/env python3
"""
Clean MongoDB collections for fresh test.
Drops all fixture collections to start from scratch.
"""
from src.data.mongo_store import MongoStore

def clean_database():
    """Drop all fixture collections"""
    print("=" * 60)
    print("CLEANING MONGODB")
    print("=" * 60)
    print()
    
    store = MongoStore()
    
    collections = [
        "fixtures_staging",
        "fixtures_active", 
        "fixtures_completed"
    ]
    
    for coll_name in collections:
        collection = store.db[coll_name]
        count = collection.count_documents({})
        
        if count > 0:
            print(f"🗑️  Dropping {coll_name} ({count} documents)...")
            collection.drop()
        else:
            print(f"✓ {coll_name} already empty")
    
    print()
    print("✅ Database cleaned - ready for fresh test")
    print()

if __name__ == "__main__":
    clean_database()
