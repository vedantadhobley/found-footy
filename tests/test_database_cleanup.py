"""Database cleanup for tests"""
import pytest
from found_footy.storage.mongo_store import FootyMongoStore

@pytest.fixture(scope="session", autouse=True)
def cleanup_test_data():
    """Clean up test data before and after test session"""
    try:
        store = FootyMongoStore()
        
        # Clean up any test data that might interfere
        store.goals_pending.delete_many({"_id": {"$regex": "^12345_"}})
        store.goals_processed.delete_many({"_id": {"$regex": "^12345_"}})
        
        print("🧹 Cleaned up test data from database")
    except Exception as e:
        print(f"⚠️ Could not clean test data: {e}")
    
    yield  # Run tests
    
    # Cleanup after tests
    try:
        store = FootyMongoStore()
        store.goals_pending.delete_many({"_id": {"$regex": "^12345_"}})
        store.goals_processed.delete_many({"_id": {"$regex": "^12345_"}})
        print("🧹 Final cleanup completed")
    except Exception as e:
        print(f"⚠️ Could not do final cleanup: {e}")