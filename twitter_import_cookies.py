#!/usr/bin/env python3
"""
Import Twitter cookies from browser-exported JSON
Use this on WSL or headless systems where you can't run a browser
"""

import json
import pickle
import os
import sys

def import_cookies():
    """Import cookies from JSON file exported from browser"""
    
    cookies_json = os.path.join(os.getcwd(), '.twitter_cookies.json')
    cookies_pkl = os.getenv('TWITTER_COOKIES_FILE', '/data/twitter_cookies.pkl')
    
    print("🐦 Import Twitter Cookies from Browser Export")
    print("=" * 60)
    print()
    
    if not os.path.exists(cookies_json):
        print(f"❌ No cookies file found at: {cookies_json}")
        print()
        print("How to export cookies:")
        print("1. Open Chrome/Firefox and go to https://twitter.com")
        print("2. Login to Twitter")
        print("3. Install 'Cookie-Editor' extension")
        print("4. Click extension → Export → Export as JSON")
        print(f"5. Save file as: {cookies_json}")
        print()
        return False
    
    try:
        print(f"📂 Reading cookies from: {cookies_json}")
        with open(cookies_json, 'r') as f:
            cookies_data = json.load(f)
        
        # Handle different export formats
        if isinstance(cookies_data, list):
            cookies = cookies_data
        elif isinstance(cookies_data, dict) and 'cookies' in cookies_data:
            cookies = cookies_data['cookies']
        else:
            print("❌ Unrecognized cookie format")
            return False
        
        # Filter for twitter.com cookies only
        twitter_cookies = []
        for cookie in cookies:
            domain = cookie.get('domain', '')
            if 'twitter.com' in domain or 'x.com' in domain:
                # Convert to Selenium cookie format
                selenium_cookie = {
                    'name': cookie['name'],
                    'value': cookie['value'],
                    'domain': cookie['domain'],
                    'path': cookie.get('path', '/'),
                    'secure': cookie.get('secure', True),
                    'httpOnly': cookie.get('httpOnly', False)
                }
                if 'expirationDate' in cookie:
                    selenium_cookie['expiry'] = int(cookie['expirationDate'])
                elif 'expiry' in cookie:
                    selenium_cookie['expiry'] = int(cookie['expiry'])
                
                twitter_cookies.append(selenium_cookie)
        
        if not twitter_cookies:
            print("❌ No Twitter cookies found in export")
            print("   Make sure you're logged in to Twitter before exporting")
            return False
        
        print(f"✅ Found {len(twitter_cookies)} Twitter cookies")
        
        # Save in pickle format for the service
        os.makedirs(os.path.dirname(cookies_pkl), exist_ok=True)
        with open(cookies_pkl, 'wb') as f:
            pickle.dump(twitter_cookies, f)
        
        print(f"✅ Saved cookies to: {cookies_pkl}")
        print()
        print("🎉 Success! Cookies imported.")
        print()
        print("Next steps:")
        print("1. Restart Twitter service: docker compose restart twitter-session")
        print("2. Check status: curl http://localhost:8888/health")
        
        # Cleanup JSON file
        try:
            os.remove(cookies_json)
            print()
            print(f"🧹 Cleaned up temporary file: {cookies_json}")
        except:
            pass
        
        return True
        
    except Exception as e:
        print(f"❌ Error importing cookies: {e}")
        import traceback
        traceback.print_exc()
        return False

if __name__ == "__main__":
    success = import_cookies()
    sys.exit(0 if success else 1)
