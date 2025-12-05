#!/usr/bin/env python3
"""Export Twitter cookies from your local browser to use in Docker.

This script extracts Twitter cookies from your local Chrome/Firefox browser
and saves them in a format that the Docker container can use.

Usage:
    1. Login to Twitter in your regular browser (Chrome or Firefox)
    2. Run: python scripts/export_twitter_cookies.py
    3. Restart the twitter container

Requirements:
    pip install browser-cookie3
"""
import os
import sys
import pickle
import json

try:
    import browser_cookie3
except ImportError:
    print("❌ Missing dependency. Install with:")
    print("   pip install browser-cookie3")
    sys.exit(1)


def get_twitter_cookies_chrome():
    """Get Twitter cookies from Chrome"""
    try:
        cj = browser_cookie3.chrome(domain_name='.twitter.com')
        cookies = []
        for cookie in cj:
            cookies.append({
                'name': cookie.name,
                'value': cookie.value,
                'domain': cookie.domain,
                'path': cookie.path,
                'secure': cookie.secure,
                'httpOnly': cookie.has_nonstandard_attr('HttpOnly'),
            })
        return cookies
    except Exception as e:
        print(f"⚠️  Chrome: {e}")
        return []


def get_twitter_cookies_firefox():
    """Get Twitter cookies from Firefox"""
    try:
        cj = browser_cookie3.firefox(domain_name='.twitter.com')
        cookies = []
        for cookie in cj:
            cookies.append({
                'name': cookie.name,
                'value': cookie.value,
                'domain': cookie.domain,
                'path': cookie.path,
                'secure': cookie.secure,
                'httpOnly': cookie.has_nonstandard_attr('HttpOnly'),
            })
        return cookies
    except Exception as e:
        print(f"⚠️  Firefox: {e}")
        return []


def get_twitter_cookies_x():
    """Get X.com cookies (Twitter rebranded)"""
    try:
        cj = browser_cookie3.chrome(domain_name='.x.com')
        cookies = []
        for cookie in cj:
            cookies.append({
                'name': cookie.name,
                'value': cookie.value,
                'domain': cookie.domain,
                'path': cookie.path,
                'secure': cookie.secure,
                'httpOnly': cookie.has_nonstandard_attr('HttpOnly'),
            })
        return cookies
    except Exception as e:
        print(f"⚠️  X.com Chrome: {e}")
        return []


def main():
    print("🔍 Looking for Twitter cookies in your browsers...")
    print()
    
    all_cookies = []
    
    # Try Chrome
    chrome_cookies = get_twitter_cookies_chrome()
    if chrome_cookies:
        print(f"✅ Found {len(chrome_cookies)} cookies from Chrome (twitter.com)")
        all_cookies.extend(chrome_cookies)
    
    # Try X.com domain too
    x_cookies = get_twitter_cookies_x()
    if x_cookies:
        print(f"✅ Found {len(x_cookies)} cookies from Chrome (x.com)")
        all_cookies.extend(x_cookies)
    
    # Try Firefox
    firefox_cookies = get_twitter_cookies_firefox()
    if firefox_cookies:
        print(f"✅ Found {len(firefox_cookies)} cookies from Firefox")
        all_cookies.extend(firefox_cookies)
    
    if not all_cookies:
        print()
        print("❌ No Twitter cookies found!")
        print()
        print("Make sure you're logged into Twitter in Chrome or Firefox,")
        print("then run this script again.")
        sys.exit(1)
    
    # Check for critical cookies
    cookie_names = [c['name'] for c in all_cookies]
    has_auth = 'auth_token' in cookie_names
    has_ct0 = 'ct0' in cookie_names
    
    print()
    if has_auth and has_ct0:
        print("✅ Found required auth_token and ct0 cookies")
    else:
        print("⚠️  Warning: Missing some auth cookies")
        if not has_auth:
            print("   - Missing: auth_token")
        if not has_ct0:
            print("   - Missing: ct0")
    
    # Save to file
    output_file = "twitter_cookies.pkl"
    with open(output_file, 'wb') as f:
        pickle.dump(all_cookies, f)
    
    print()
    print(f"✅ Saved {len(all_cookies)} cookies to {output_file}")
    print()
    print("Next steps:")
    print("  1. Copy cookies to Docker volume:")
    print("     docker cp twitter_cookies.pkl found-footy-twitter:/data/twitter_cookies.pkl")
    print()
    print("  2. Restart the twitter container:")
    print("     docker compose -f docker-compose.dev.yml restart twitter")
    print()


if __name__ == "__main__":
    main()
