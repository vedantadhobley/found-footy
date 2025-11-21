#!/usr/bin/env python3
"""
Manual Twitter Login Helper

Run this script inside the container to login to Twitter via GUI browser:
    docker compose -f docker-compose.dev.yml exec twitter python -m twitter.manual_login

Opens browser visible at http://localhost:6080/vnc.html
"""
import sys
import os

# Add parent directory to path
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from twitter.config import TwitterConfig
from twitter.auth import TwitterAuthenticator
from twitter.session import TwitterSessionManager


def main():
    """Run interactive login via VNC GUI browser"""
    print("=" * 80)
    print("Twitter Manual Login")
    print("=" * 80)
    print()
    print("This will open a browser visible at: http://localhost:6080/vnc.html")
    print("Login to Twitter in that browser, and this script will capture cookies.")
    print()
    input("Press Enter to continue (or Ctrl+C to cancel)...")
    print()
    
    # Initialize
    config = TwitterConfig()
    session_mgr = TwitterSessionManager(config)
    
    # Launch GUI browser (visible via VNC)
    print("🌐 Launching browser (visible at http://localhost:6080/vnc.html)...")
    if not session_mgr._setup_browser(headless=False):
        print("❌ Failed to launch browser")
        return 1
    
    print("✅ Browser launched!")
    print()
    print("=" * 80)
    print("OPEN IN YOUR HOST BROWSER:")
    print("    http://localhost:6080/vnc.html")
    print()
    print("Then login to Twitter and return here.")
    print("=" * 80)
    print()
    
    # Wait for manual login (5 minutes max)
    authenticator = TwitterAuthenticator(config)
    success = authenticator.interactive_login(session_mgr.driver, timeout=300)
    
    if success:
        print()
        print("✅ Successfully authenticated!")
        print(f"   Cookies saved to: {config.cookies_file}")
        print()
        print("You can now use the Twitter API:")
        print("    curl http://localhost:3103/health")
        print("    curl 'http://localhost:3103/search?query=football'")
        print()
        return 0
    else:
        print()
        print("❌ Authentication failed or timed out")
        print("   Please try again")
        print()
        return 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        print("\n\n⚠️  Cancelled by user")
        sys.exit(1)
    except Exception as e:
        print(f"\n\n❌ Error: {e}")
        import traceback
        traceback.print_exc()
        sys.exit(1)
