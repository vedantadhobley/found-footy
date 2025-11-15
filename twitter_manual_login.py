#!/usr/bin/env python3
"""
Manual Twitter Login - Run this inside the Docker container
Opens a browser window where you can manually login, then saves cookies
"""

import pickle
import os
import time
from selenium import webdriver
from selenium.webdriver.chrome.options import Options
from selenium.webdriver.chrome.service import Service

def manual_login():
    """Open browser for manual login and save cookies"""
    
    cookies_file = os.getenv('TWITTER_COOKIES_FILE', '/data/twitter_cookies.pkl')
    
    print("🐦 Twitter Manual Login")
    print("=" * 50)
    print()
    print("This will:")
    print("1. Open Twitter login page")
    print("2. Wait for you to login manually")
    print("3. Save cookies to:", cookies_file)
    print()
    
    # Setup browser - NOT headless so you can see it
    chrome_options = Options()
    chrome_options.add_argument("--no-sandbox")
    chrome_options.add_argument("--disable-dev-shm-usage")
    chrome_options.add_argument("--window-size=1920,1080")
    chrome_options.add_argument("--disable-web-security")
    chrome_options.add_argument("--user-agent=Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36")
    
    service = Service("/usr/bin/chromedriver")
    
    try:
        print("🌐 Opening browser...")
        driver = webdriver.Chrome(service=service, options=chrome_options)
        
        # Go to Twitter login
        driver.get("https://twitter.com/login")
        print()
        print("✋ PLEASE LOGIN IN THE BROWSER WINDOW")
        print("   Complete all steps (username, password, 2FA if needed)")
        print("   Once you see your Twitter home feed, come back here")
        print()
        input("Press ENTER after you've logged in and see your home feed...")
        
        # Check if login was successful
        current_url = driver.current_url
        if "home" in current_url or "twitter.com" in current_url:
            print()
            print("✅ Detected login! Saving cookies...")
            
            # Save cookies
            cookies = driver.get_cookies()
            os.makedirs(os.path.dirname(cookies_file), exist_ok=True)
            
            with open(cookies_file, 'wb') as f:
                pickle.dump(cookies, f)
            
            print(f"✅ Saved {len(cookies)} cookies to {cookies_file}")
            print()
            print("🎉 Setup complete!")
            print("   Restart the Twitter service to use the saved cookies:")
            print("   docker compose restart twitter-session")
            return True
        else:
            print()
            print("❌ Login not detected (still on login page)")
            print(f"   Current URL: {current_url}")
            return False
            
    except Exception as e:
        print(f"❌ Error: {e}")
        import traceback
        traceback.print_exc()
        return False
    finally:
        try:
            driver.quit()
        except:
            pass

if __name__ == "__main__":
    success = manual_login()
    exit(0 if success else 1)
