#!/usr/bin/env python3
"""Test script to print the first 5 tweets from Szoboszlai Liverpool search"""
import sys
import os
import time
import traceback
sys.path.insert(0, '/app')

# Set MongoDB URL for Docker network
os.environ['MONGODB_URL'] = 'mongodb://footy_admin:footy_secure_pass@mongodb:27017/found_footy?authSource=admin'

def test_twitter_content_extraction():
    """Test Twitter browser automation to extract actual tweet content"""
    print("🐦 TESTING TWITTER CONTENT EXTRACTION")
    print("=" * 50)
    
    try:
        from selenium import webdriver
        from selenium.webdriver.chrome.options import Options
        from selenium.webdriver.chrome.service import Service
        from selenium.webdriver.common.by import By
        from selenium.webdriver.support.ui import WebDriverWait
        from selenium.webdriver.support import expected_conditions as EC
        from selenium.common.exceptions import TimeoutException, NoSuchElementException
        from urllib.parse import quote
        
        # Set up Chrome options (same as your working config)
        chrome_options = Options()
        chrome_options.add_argument("--headless")
        chrome_options.add_argument("--no-sandbox")
        chrome_options.add_argument("--disable-dev-shm-usage")
        chrome_options.add_argument("--disable-gpu")
        chrome_options.add_argument("--window-size=1920,1080")
        chrome_options.add_argument("--disable-web-security")
        chrome_options.add_argument("--disable-features=VizDisplayCompositor")
        chrome_options.add_argument("--disable-extensions")
        chrome_options.add_argument("--disable-plugins")
        # Note: Keep JavaScript enabled for Twitter login
        chrome_options.add_argument("--user-agent=Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
        
        # Initialize driver
        service = Service("/usr/bin/chromedriver")
        driver = webdriver.Chrome(service=service, options=chrome_options)
        
        print("✅ Browser setup successful")
        
        # ✅ FIX: Actually log in to Twitter first
        print("🔐 Logging in to Twitter...")
        
        # Get credentials from environment
        twitter_username = os.getenv('TWITTER_USERNAME', 'REDACTED_USERNAME')
        twitter_password = os.getenv('TWITTER_PASSWORD', 'REDACTED_PASSWORD')
        twitter_email = os.getenv('TWITTER_EMAIL', 'REDACTED_EMAIL')
        
        print(f"   Using username: {twitter_username}")
        print(f"   Using email: {twitter_email}")
        
        # Navigate to Twitter login
        driver.get("https://twitter.com/login")
        
        # Wait for login form
        print("   ⏳ Waiting for login form...")
        try:
            username_input = WebDriverWait(driver, 10).until(
                EC.presence_of_element_located((By.NAME, "text"))
            )
            print("   📝 Found username input")
            
            # Enter username/email
            username_input.clear()
            username_input.send_keys(twitter_username)
            
            # Click next button
            next_button = driver.find_element(By.XPATH, "//span[text()='Next']/..")
            next_button.click()
            
            print("   ⏳ Waiting for password form...")
            time.sleep(3)
            
            # Check if we need to provide email (additional verification)
            try:
                email_input = driver.find_element(By.NAME, "text")
                print("   📧 Email verification required")
                email_input.clear()
                email_input.send_keys(twitter_email)
                
                next_button = driver.find_element(By.XPATH, "//span[text()='Next']/..")
                next_button.click()
                time.sleep(3)
            except NoSuchElementException:
                print("   ✅ No email verification needed")
            
            # Enter password
            password_input = WebDriverWait(driver, 10).until(
                EC.presence_of_element_located((By.NAME, "password"))
            )
            print("   🔑 Found password input")
            
            password_input.clear()
            password_input.send_keys(twitter_password)
            
            # Click login button
            login_button = driver.find_element(By.XPATH, "//span[text()='Log in']/..")
            login_button.click()
            
            print("   ⏳ Waiting for login to complete...")
            time.sleep(5)
            
            # Check if login was successful
            current_url = driver.current_url
            if "home" in current_url or "twitter.com" in current_url and "login" not in current_url:
                print("   ✅ Login successful!")
            else:
                print(f"   ⚠️ Login may have failed. Current URL: {current_url}")
                
        except TimeoutException:
            print("   ❌ Login form not found or timeout")
            print(f"   Current URL: {driver.current_url}")
            print(f"   Page title: {driver.title}")
        
        # Now search for Szoboszlai Liverpool
        search_query = "Szoboszlai Liverpool"
        search_url = f"https://twitter.com/search?q={quote(search_query)}&src=typed_query&f=live"
        
        print(f"\n🌐 Navigating to: {search_url}")
        driver.get(search_url)
        
        # Wait for page to load
        print("⏳ Waiting for page to load...")
        time.sleep(5)
        
        # Check current URL and title
        print(f"📍 Current URL: {driver.current_url}")
        print(f"📄 Page title: {driver.title}")
        
        # Try multiple selectors to find tweets
        tweet_selectors = [
            "article[data-testid='tweet']",  # Modern Twitter
            "div[data-testid='tweet']",     # Alternative
            "article",                       # Generic article tags
            "[data-testid='cellInnerDiv']", # Tweet container
            "[data-testid='tweetText']",    # Tweet text specifically
            ".tweet",                       # Classic selector
            ".js-stream-item"              # Legacy selector
        ]
        
        tweets_found = []
        
        for selector in tweet_selectors:
            try:
                print(f"🔍 Trying selector: {selector}")
                tweet_elements = driver.find_elements(By.CSS_SELECTOR, selector)
                print(f"   Found {len(tweet_elements)} elements")
                
                if tweet_elements:
                    tweets_found = tweet_elements
                    print(f"✅ Using selector: {selector}")
                    break
                    
            except Exception as e:
                print(f"   ❌ Selector failed: {e}")
                continue
        
        if not tweets_found:
            # Get page source for debugging
            print("\n🔍 NO TWEETS FOUND - DEBUGGING INFO:")
            print("=" * 50)
            page_source = driver.page_source
            print(f"Page title: {driver.title}")
            print(f"Current URL: {driver.current_url}")
            print(f"Page source length: {len(page_source)} characters")
            
            # Look for specific patterns in the HTML
            print("\n📊 HTML ANALYSIS:")
            if "login" in page_source.lower():
                print("⚠️ LOGIN REQUIRED - Twitter is still asking for authentication")
            if "suspended" in page_source.lower():
                print("⚠️ SUSPENDED - Account or request may be suspended")
            if "rate limit" in page_source.lower():
                print("⚠️ RATE LIMITED - Too many requests")
            if "robot" in page_source.lower() or "bot" in page_source.lower():
                print("⚠️ BOT DETECTION - Twitter detected automation")
            if "challenge" in page_source.lower():
                print("⚠️ CHALLENGE - Twitter is requesting additional verification")
            if "captcha" in page_source.lower():
                print("⚠️ CAPTCHA - Human verification required")
            if "tweet" in page_source.lower():
                tweet_count = page_source.lower().count("tweet")
                print(f"✅ Found {tweet_count} mentions of 'tweet' in HTML")
            if "search" in page_source.lower():
                search_count = page_source.lower().count("search")
                print(f"🔍 Found {search_count} mentions of 'search' in HTML")
            
            # Save page source for manual inspection
            with open("/tmp/twitter_page_source.html", "w", encoding="utf-8") as f:
                f.write(page_source)
            print("💾 Saved page source to /tmp/twitter_page_source.html")
            
            # Try to find any content at all
            print("\n🔍 LOOKING FOR ANY CONTENT:")
            all_elements = driver.find_elements(By.CSS_SELECTOR, "*")
            print(f"   Total elements on page: {len(all_elements)}")
            
            # Look for common Twitter elements
            common_elements = [
                "main", "nav", "header", "footer", "[role='main']", 
                "[data-testid]", ".css-", "[aria-label]"
            ]
            
            for element_selector in common_elements:
                try:
                    elements = driver.find_elements(By.CSS_SELECTOR, element_selector)
                    if elements:
                        print(f"   Found {len(elements)} {element_selector} elements")
                except:
                    pass
            
            driver.quit()
            return False
        
        print(f"\n📋 EXTRACTING CONTENT FROM {len(tweets_found)} TWEETS:")
        print("=" * 60)
        
        # Extract content from first 5 tweets
        for i, tweet_element in enumerate(tweets_found[:5]):
            try:
                print(f"\n🐦 TWEET #{i+1}:")
                print("-" * 30)
                
                # Try multiple methods to extract text
                text_selectors = [
                    "[data-testid='tweetText']",
                    ".tweet-text",
                    "[lang]",  # Tweets often have lang attribute
                    "span",    # Tweet text is often in spans
                    "div"      # Fallback to divs
                ]
                
                tweet_text = ""
                for text_selector in text_selectors:
                    try:
                        text_elements = tweet_element.find_elements(By.CSS_SELECTOR, text_selector)
                        if text_elements:
                            text_parts = [elem.text.strip() for elem in text_elements if elem.text.strip()]
                            if text_parts:
                                tweet_text = " ".join(text_parts[:3])  # Take first 3 text elements
                                break
                    except Exception:
                        continue
                
                if not tweet_text:
                    # Fallback: get all text from tweet element
                    tweet_text = tweet_element.text.strip()
                
                # Clean up text
                if tweet_text:
                    # Remove excessive whitespace
                    tweet_text = " ".join(tweet_text.split())
                    # Truncate if too long
                    if len(tweet_text) > 200:
                        tweet_text = tweet_text[:200] + "..."
                
                print(f"Text: {tweet_text if tweet_text else '(No text found)'}")
                
                # Try to extract additional metadata
                try:
                    # Look for username
                    username_selectors = ["@", "data-screen-name", "[data-testid='User-Name']"]
                    username = "Unknown"
                    for selector in username_selectors:
                        try:
                            if selector == "@":
                                # Look for @username pattern in text
                                full_text = tweet_element.text
                                if "@" in full_text:
                                    parts = full_text.split("@")
                                    if len(parts) > 1:
                                        username_part = parts[1].split()[0]
                                        username = f"@{username_part}"
                                        break
                            else:
                                user_elem = tweet_element.find_element(By.CSS_SELECTOR, f"[{selector}]")
                                if user_elem:
                                    username = user_elem.get_attribute(selector) or user_elem.text
                                    break
                        except Exception:
                            continue
                    
                    print(f"User: {username}")
                    
                    # Look for time/date
                    time_selectors = ["time", "[datetime]", "[data-testid='Time']"]
                    timestamp = "Unknown"
                    for selector in time_selectors:
                        try:
                            time_elem = tweet_element.find_element(By.CSS_SELECTOR, selector)
                            timestamp = time_elem.get_attribute("datetime") or time_elem.text
                            break
                        except Exception:
                            continue
                    
                    print(f"Time: {timestamp}")
                    
                    # Look for media indicators
                    media_indicators = ["video", "img", "[data-testid='videoPlayer']", "[data-testid='card.layoutSmall.media']"]
                    has_media = False
                    for indicator in media_indicators:
                        try:
                            media_elem = tweet_element.find_elements(By.CSS_SELECTOR, indicator)
                            if media_elem:
                                has_media = True
                                break
                        except Exception:
                            continue
                    
                    print(f"Has Media: {'Yes' if has_media else 'No'}")
                    
                except Exception as e:
                    print(f"Metadata extraction error: {e}")
                
                print()  # Empty line between tweets
                
            except Exception as e:
                print(f"❌ Error extracting tweet #{i+1}: {e}")
                continue
        
        driver.quit()
        print("✅ Content extraction completed!")
        return True
        
    except ImportError:
        print("❌ Selenium not installed. Install with: pip install selenium")
        return False
    except Exception as e:
        print(f"❌ Error in content extraction: {e}")
        traceback.print_exc()
        return False

def test_ssl_bypass():
    """Test if SSL bypass is working"""
    print("\n🔒 TESTING SSL BYPASS")
    print("=" * 30)
    
    try:
        import ssl
        import urllib3
        import requests
        
        # Disable SSL warnings
        urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
        
        # Test basic HTTPS request with SSL disabled
        response = requests.get('https://httpbin.org/get', verify=False, timeout=10)
        if response.status_code == 200:
            print("✅ SSL bypass working - HTTPS requests successful")
            return True
        else:
            print(f"⚠️ Unexpected status code: {response.status_code}")
            return False
            
    except Exception as e:
        print(f"❌ SSL bypass test failed: {e}")
        return False

def main():
    """Run the Twitter content extraction test"""
    print("🐦 TWITTER CONTENT EXTRACTION TEST")
    print("⏰ " + "2025-09-14")
    print("=" * 60)
    
    # Test SSL bypass first
    ssl_ok = test_ssl_bypass()
    
    # Test Twitter content extraction
    content_ok = test_twitter_content_extraction()
    
    print("\n" + "=" * 60)
    if ssl_ok and content_ok:
        print("✅ ALL TESTS PASSED!")
        print("🎯 Successfully extracted Twitter content")
        print("\n💡 INSIGHTS:")
        print("   • If you see actual tweet content above, the browser automation works")
        print("   • If you see login prompts, Twitter requires authentication")
        print("   • If you see no tweets, Twitter may be blocking the request")
        print("   • Check /tmp/twitter_page_source.html for raw HTML analysis")
        return 0
    else:
        print("❌ SOME TESTS FAILED")
        print("🔧 Check the errors above")
        if not content_ok:
            print("\n🤔 TWITTER BOT PROTECTION ANALYSIS:")
            print("   • The login attempt above shows if authentication worked")
            print("   • Twitter may require 2FA or additional verification steps")
            print("   • Your account may need to be verified/active for API access")
            print("   • Try running without --headless to see the actual browser")
            print("   • Consider using official Twitter API instead of scraping")
        return 1

if __name__ == "__main__":
    exit_code = main()
    sys.exit(exit_code)