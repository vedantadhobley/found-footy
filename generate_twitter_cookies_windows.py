#!/usr/bin/env python3
"""
Run this on your WINDOWS machine (not in WSL/Docker) to generate cookies.
It will open a browser, let you login, then save cookies to a file you can copy.
"""

import pickle
import time
from selenium import webdriver
from selenium.webdriver.chrome.options import Options

def generate_cookies():
    print("=" * 60)
    print("🐦 TWITTER COOKIE GENERATOR (Windows)")
    print("=" * 60)
    print()
    print("This will open Chrome on your Windows desktop.")
    print("Login to Twitter, then come back here.")
    print()
    
    # Regular Chrome options (no headless, no special flags)
    chrome_options = Options()
    chrome_options.add_argument('--incognito')
    
    try:
        print("🌐 Opening Chrome...")
        driver = webdriver.Chrome(options=chrome_options)
        
        print("📱 Navigating to x.com...")
        driver.get("https://x.com/login")
        
        print()
        print("=" * 60)
        print("✋ LOGIN IN THE BROWSER WINDOW")
        print("   Complete login and wait until you see your home feed")
        print("   Then press ENTER here")
        print("=" * 60)
        print()
        
        input("Press ENTER after login...")
        
        current_url = driver.current_url
        print(f"\n🔍 Current URL: {current_url}")
        
        if "home" in current_url or ("x.com" in current_url and "login" not in current_url):
            cookies = driver.get_cookies()
            
            # Save to file
            output_file = "twitter_cookies_from_windows.pkl"
            with open(output_file, 'wb') as f:
                pickle.dump(cookies, f)
            
            print(f"\n✅ Saved {len(cookies)} cookies to: {output_file}")
            print(f"\n📋 Next steps:")
            print(f"   1. Copy {output_file} to your WSL home directory")
            print(f"   2. Run: docker cp {output_file} found-footy-twitter-session:/data/twitter_cookies.pkl")
            print(f"   3. Run: docker compose restart twitter-session")
        else:
            print("\n❌ Login not detected. Please try again.")
        
        driver.quit()
        
    except Exception as e:
        print(f"\n❌ Error: {e}")
        import traceback
        traceback.print_exc()

if __name__ == "__main__":
    generate_cookies()
