#!/usr/bin/env python3
"""Twitter worker startup script with session initialization"""
import sys
sys.path.insert(0, '/app')

def initialize_twitter_session():
    """Initialize persistent Twitter session on worker startup"""
    print("🚀 Initializing Twitter session for worker...")
    
    try:
        from found_footy.services.twitter_session import twitter_session
        
        # Authenticate once on startup
        if twitter_session.authenticate():
            print("✅ Twitter session authenticated and ready")
            print("🔄 Session will persist for all goal searches")
            return True
        else:
            print("❌ Failed to authenticate Twitter session")
            return False
            
    except Exception as e:
        print(f"❌ Session initialization failed: {e}")
        return False

if __name__ == "__main__":
    initialize_twitter_session()