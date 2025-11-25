#!/usr/bin/env python3
"""
Firefox Manual Login - ONE TIME SETUP

This opens Firefox in the VNC GUI. Login to Twitter manually ONCE,
and the entire Firefox profile (with cookies and session) gets saved.

After this, all automation will work because Firefox thinks it's the same browser.

Usage:
    docker compose -f docker-compose.dev.yml exec twitter python -m twitter.firefox_manual_setup
"""
import os
import sys
import time
import subprocess

def main():
    """Launch Firefox GUI for manual Twitter login"""
    print("=" * 80)
    print("Firefox Manual Login Setup")
    print("=" * 80)
    print()
    print("This will:")
    print("  1. Launch Firefox in GUI mode (visible at http://localhost:6080/vnc.html)")
    print("  2. Open Twitter login page")
    print("  3. You login manually")
    print("  4. Close Firefox when done (profile auto-saves)")
    print()
    print("After this, the Twitter scraper will work automatically!")
    print()
    input("Press Enter to start Firefox (or Ctrl+C to cancel)...")
    print()
    
    # Set display
    os.environ['DISPLAY'] = ':99'
    
    # Profile directory
    profile_dir = "/data/firefox_profile"
    os.makedirs(profile_dir, exist_ok=True)
    
    print(f"🦊 Launching Firefox with profile: {profile_dir}")
    print()
    print("=" * 80)
    print("VIEW IN YOUR BROWSER:")
    print("    http://localhost:6080/vnc.html")
    print()
    print("LOGIN TO TWITTER, then close Firefox or press Ctrl+C here")
    print("=" * 80)
    print()
    
    try:
        # Launch Firefox with persistent profile
        subprocess.run([
            'firefox',
            '--profile', profile_dir,
            '--no-remote',
            'https://twitter.com/login'
        ])
        
        print()
        print("✅ Firefox closed - profile saved!")
        print(f"   Profile location: {profile_dir}")
        print()
        print("You can now use the Twitter scraper:")
        print("    curl http://localhost:3103/health")
        print("    curl 'http://localhost:3103/search?query=football'")
        print()
        return 0
        
    except KeyboardInterrupt:
        print("\n\n✅ Firefox still running in VNC")
        print("   Close Firefox manually when done with login")
        print(f"   Profile will be saved to: {profile_dir}")
        print()
        return 0
    except Exception as e:
        print(f"\n\n❌ Error: {e}")
        return 1


if __name__ == "__main__":
    sys.exit(main())
