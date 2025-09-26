#!/usr/bin/env python3
"""Test the real Szoboszlai goal flow that we implemented"""
import sys
import os
import time
import tempfile
import traceback
sys.path.insert(0, '/app')

# ✅ FIX: Set correct URLs for Docker network
os.environ['MONGODB_URL'] = 'mongodb://footy_admin:footy_secure_pass@mongodb:27017/found_footy?authSource=admin'
os.environ['S3_ENDPOINT_URL'] = 'http://minio:9000'  # ✅ FIX: Use Docker service name
os.environ['S3_ACCESS_KEY'] = 'footy_admin'
os.environ['S3_SECRET_KEY'] = 'footy_secure_pass'
os.environ['S3_BUCKET_NAME'] = 'footy-videos'

def test_real_twitter_login_and_search():
    """Test actual Twitter login and video search - REAL IMPLEMENTATION"""
    print("🐦 TESTING REAL TWITTER LOGIN & VIDEO SEARCH")
    print("=" * 60)
    
    try:
        from selenium import webdriver
        from selenium.webdriver.chrome.options import Options
        from selenium.webdriver.chrome.service import Service
        from selenium.webdriver.common.by import By
        from selenium.webdriver.support.ui import WebDriverWait
        from selenium.webdriver.support import expected_conditions as EC
        from selenium.common.exceptions import TimeoutException, NoSuchElementException
        from urllib.parse import quote
        
        # Set up Chrome options (same as working config)
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
        
        # LOGIN TO TWITTER FIRST (using working logic)
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
            driver.quit()
            return []
        
        # NOW SEARCH FOR SZOBOSZLAI LIVERPOOL VIDEOS
        search_query = "Szoboszlai Liverpool filter:videos"
        search_url = f"https://twitter.com/search?q={quote(search_query)}&src=typed_query&f=live"
        
        print(f"\n🌐 Searching for videos: {search_url}")
        driver.get(search_url)
        
        # Wait for page to load
        print("⏳ Waiting for page to load...")
        time.sleep(5)
        
        # Check current URL and title
        print(f"📍 Current URL: {driver.current_url}")
        print(f"📄 Page title: {driver.title}")
        
        # Look for tweets with videos
        tweet_elements = driver.find_elements(By.CSS_SELECTOR, "article[data-testid='tweet']")
        print(f"🔍 Found {len(tweet_elements)} tweets")
        
        discovered_videos = []
        
        if tweet_elements:
            print(f"\n🎬 SEARCHING FOR VIDEOS IN TWEETS:")
            print("=" * 60)
            
            for i, tweet_element in enumerate(tweet_elements[:10]):  # Check first 10 tweets
                try:
                    print(f"\n📱 TWEET #{i+1}:")
                    print("-" * 30)
                    
                    # Get tweet text
                    tweet_text = ""
                    try:
                        text_elements = tweet_element.find_elements(By.CSS_SELECTOR, "[data-testid='tweetText']")
                        if text_elements:
                            tweet_text = " ".join([elem.text.strip() for elem in text_elements if elem.text.strip()])
                        else:
                            tweet_text = tweet_element.text.strip()
                        
                        if tweet_text:
                            tweet_text = " ".join(tweet_text.split())
                            if len(tweet_text) > 150:
                                tweet_text = tweet_text[:150] + "..."
                    except Exception:
                        tweet_text = "(Could not extract text)"
                    
                    print(f"Text: {tweet_text}")
                    
                    # Look for video indicators
                    video_selectors = [
                        "[data-testid='videoPlayer']",
                        "video",
                        "[aria-label*='video']",
                        "[aria-label*='Video']",
                        ".r-1p0dtai",  # Twitter video container class
                        "[data-testid='card.layoutLarge.media']",
                        "[data-testid='card.layoutSmall.media']"
                    ]
                    
                    has_video = False
                    video_element = None
                    
                    for selector in video_selectors:
                        try:
                            video_elements = tweet_element.find_elements(By.CSS_SELECTOR, selector)
                            if video_elements:
                                has_video = True
                                video_element = video_elements[0]
                                print(f"   ✅ Found video with selector: {selector}")
                                break
                        except Exception:
                            continue
                    
                    if has_video:
                        print(f"   🎬 HAS VIDEO!")
                        
                        # Try to get tweet URL
                        tweet_url = ""
                        try:
                            # Look for links to the tweet
                            link_elements = tweet_element.find_elements(By.CSS_SELECTOR, "a[href*='/status/']")
                            if link_elements:
                                href = link_elements[0].get_attribute("href")
                                if href and "/status/" in href:
                                    tweet_url = href
                                    print(f"   🔗 Tweet URL: {tweet_url}")
                        except Exception:
                            pass
                        
                        # Try to extract tweet ID from URL
                        tweet_id = ""
                        if tweet_url and "/status/" in tweet_url:
                            try:
                                tweet_id = tweet_url.split("/status/")[1].split("?")[0]
                                print(f"   🆔 Tweet ID: {tweet_id}")
                            except Exception:
                                pass
                        
                        # Get username
                        username = "Unknown"
                        try:
                            # Look for username in tweet
                            username_elements = tweet_element.find_elements(By.CSS_SELECTOR, "[data-testid='User-Name']")
                            if username_elements:
                                username_text = username_elements[0].text
                                if "@" in username_text:
                                    username = username_text.split("@")[1].split()[0] if username_text.split("@") else "Unknown"
                                    username = f"@{username}"
                                    print(f"   👤 Username: {username}")
                        except Exception:
                            pass
                        
                        # Create video entry
                        if tweet_url and tweet_id:
                            video_entry = {
                                "search_term": "Szoboszlai Liverpool",
                                "tweet_url": tweet_url,
                                "tweet_id": tweet_id,
                                "tweet_text": tweet_text,
                                "username": username,
                                "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                                "discovered_at": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                                "search_index": 0,
                                "video_index": len(discovered_videos),
                                "source": "browser_automation",
                                "requires_ytdlp": True,
                                "video_page_url": f"https://x.com/i/status/{tweet_id}"
                            }
                            discovered_videos.append(video_entry)
                            print(f"   ✅ Added to discovered videos (#{len(discovered_videos)})")
                        else:
                            print(f"   ⚠️ Could not extract tweet URL/ID")
                    else:
                        print(f"   ⭕ No video found")
                    
                except Exception as e:
                    print(f"   ❌ Error processing tweet #{i+1}: {e}")
                    continue
        
        driver.quit()
        
        print(f"\n📊 FINAL RESULTS:")
        print("=" * 50)
        print(f"Total tweets checked: {len(tweet_elements)}")
        print(f"Videos discovered: {len(discovered_videos)}")
        
        if discovered_videos:
            print(f"\n🎬 DISCOVERED VIDEOS:")
            for i, video in enumerate(discovered_videos):
                print(f"  {i+1}. {video['tweet_url']}")
                print(f"     User: {video['username']}")
                print(f"     Text: {video['tweet_text'][:100]}...")
                print()
            
            return discovered_videos
        else:
            print("⚠️ No videos found")
            return []
        
    except ImportError:
        print("❌ Selenium not installed. Install with: pip install selenium")
        return []
    except Exception as e:
        print(f"❌ Error in Twitter video search: {e}")
        traceback.print_exc()
        return []

def test_real_szoboszlai_flow():
    """Test the actual Twitter flow implementation with Szoboszlai example - ENHANCED"""
    print("🧪 TESTING REAL SZOBOSZLAI TWITTER FLOW - ENHANCED")
    print("=" * 60)
    
    try:
        # Import the actual flow
        from found_footy.flows.twitter_flow import twitter_search_task
        from found_footy.storage.mongo_store import FootyMongoStore
        
        # Set up the goal in the database (like a real goal would be)
        store = FootyMongoStore()
        
        goal_id = "1378993_83_1096"  # Szoboszlai goal
        goal_doc = {
            "_id": goal_id,
            "time": {"elapsed": 83},
            "team": {"id": 40, "name": "Liverpool"},
            "player": {"id": 1096, "name": "D. Szoboszlai"},
            "type": "Goal",
            "detail": "Normal Goal"
            # ❌ REMOVE: "created_at", "status", etc.
        }
        
        # Insert the goal into goals_pending
        print(f"💾 Connecting to MongoDB...")
        store.goals_pending.replace_one({"_id": goal_id}, goal_doc, upsert=True)
        print(f"✅ Inserted Szoboszlai goal: {goal_id}")
        
        # NEW: First test our real browser automation
        print(f"🔍 Testing real Twitter search first...")
        real_videos = test_real_twitter_login_and_search()
        
        if real_videos:
            print(f"✅ Real browser automation found {len(real_videos)} videos!")
            
            # Update the goal with real discovered videos
            goal_doc["discovered_videos"] = real_videos
            goal_doc["twitter_search_completed"] = True
            goal_doc["search_method"] = "browser_automation"
            goal_doc["status"] = "videos_discovered"
            store.goals_pending.replace_one({"_id": goal_id}, goal_doc, upsert=True)
            print(f"✅ Updated goal with {len(real_videos)} real videos")
            
            # Now test if we can download them
            print(f"\n📥 TESTING REAL VIDEO DOWNLOAD")
            print("=" * 50)
            
            first_video = real_videos[0]
            print(f"Testing download of: {first_video['tweet_url']}")
            
            try:
                import yt_dlp
                
                with tempfile.TemporaryDirectory() as temp_dir:
                    output_template = f"{temp_dir}/szoboszlai_test.%(ext)s"
                    
                    ydl_opts = {
                        'format': 'best[height<=720]',
                        'outtmpl': output_template,
                        'quiet': True,
                        'no_warnings': True,
                    }
                    
                    with yt_dlp.YoutubeDL(ydl_opts) as ydl:
                        print("   📊 Extracting video info...")
                        info = ydl.extract_info(first_video['tweet_url'], download=False)
                        
                        if info:
                            print(f"   ✅ Video info extracted!")
                            print(f"      Title: {info.get('title', 'Unknown')}")
                            print(f"      Duration: {info.get('duration', 'Unknown')} seconds")
                            print(f"      Resolution: {info.get('width', '?')}x{info.get('height', '?')}")
                            
                            print("   📥 Downloading video...")
                            ydl.download([first_video['tweet_url']])
                            
                            # Check if file was downloaded
                            import os
                            downloaded_files = [f for f in os.listdir(temp_dir) if not f.endswith('.info.json')]
                            
                            if downloaded_files:
                                downloaded_file = os.path.join(temp_dir, downloaded_files[0])
                                file_size = os.path.getsize(downloaded_file)
                                print(f"   ✅ Downloaded: {downloaded_files[0]} ({file_size} bytes)")
                                
                                # Test S3 upload
                                print("   ☁️ Testing S3 upload...")
                                from found_footy.storage.s3_store import FootyS3Store
                                
                                s3_store = FootyS3Store()
                                upload_result = s3_store.upload_video_file(
                                    downloaded_file,
                                    goal_id,
                                    0,  # search_index
                                    0,  # video_index
                                    metadata={
                                        "test_run": True,
                                        "source_tweet": first_video['tweet_url'],
                                        "player": "D. Szoboszlai",
                                        "team": "Liverpool"
                                    }
                                )
                                
                                if upload_result["status"] == "success":
                                    print(f"   ✅ S3 upload successful!")
                                    print(f"      S3 Key: {upload_result['s3_key']}")
                                    print(f"      S3 URL: {upload_result['s3_url']}")
                                    
                                    return {
                                        "twitter_search": "success",
                                        "videos_found": len(real_videos),
                                        "download_test": "success",
                                        "s3_upload": "success",
                                        "s3_key": upload_result['s3_key']
                                    }
                                else:
                                    print(f"   ❌ S3 upload failed: {upload_result.get('error')}")
                            else:
                                print("   ⚠️ No files downloaded")
                        else:
                            print("   ⚠️ Could not extract video info")
                            
            except Exception as e:
                print(f"   ❌ Download test failed: {e}")
        else:
            print("❌ No real videos found - falling back to flow test")
            
            # Run the actual Twitter search task as fallback
            print(f"🔍 Running Twitter search task: {goal_doc['player_name']} {goal_doc['team_name']}")
            result = twitter_search_task(goal_id)
            
            print("\n📊 FLOW RESULTS:")
            print(f"Status: {result['status']}")
            print(f"Search method: {result.get('search_method', 'unknown')}")
            print(f"Videos found: {result.get('video_count', 0)}")
            print(f"Search terms: {result.get('search_terms', [])}")
            
            if result.get("discovered_videos"):
                print(f"\n📹 DISCOVERED VIDEOS:")
                for i, video in enumerate(result["discovered_videos"][:3]):
                    print(f"  {i+1}. {video.get('tweet_url', 'No URL')}")
                    print(f"     Source: {video.get('source', 'unknown')}")
                    print(f"     Text: {video.get('tweet_text', 'No text')[:100]}...")
                    print()
            
            # Check what was stored in the database
            updated_goal = store.goals_pending.find_one({"_id": goal_id})
            if updated_goal:
                print("💾 DATABASE UPDATE:")
                print(f"   Status: {updated_goal.get('status', 'unknown')}")
                print(f"   Search completed: {updated_goal.get('twitter_search_completed', False)}")
                print(f"   Videos stored: {len(updated_goal.get('discovered_videos', []))}")
            
            return {"flow_result": result}
        
    except Exception as e:
        print(f"❌ Error testing Szoboszlai flow: {e}")
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
    """Run all tests"""
    print("🧪 SZOBOSZLAI REAL FLOW TEST - ENHANCED WITH BROWSER AUTOMATION")
    print("⏰ " + "2025-09-14")
    print("=" * 80)
    
    # Test SSL bypass first
    ssl_ok = test_ssl_bypass()
    
    # Test the enhanced flow
    flow_result = test_real_szoboszlai_flow()
    
    print("\n" + "=" * 80)
    if ssl_ok and flow_result:
        print("✅ ALL TESTS PASSED!")
        if isinstance(flow_result, dict) and "s3_key" in flow_result:
            print("🎯 COMPLETE SUCCESS - Real video downloaded and uploaded to MinIO!")
            print(f"📹 S3 Key: {flow_result['s3_key']}")
            print("🌐 Check MinIO Console: http://localhost:9001")
            print("📂 Bucket: footy-videos")
            print("🔑 Login: footy_admin / footy_secure_pass")
        else:
            print("🎯 Twitter flow working - ready for real goals!")
        return 0
    else:
        print("❌ SOME TESTS FAILED")
        print("🔧 Check the errors above")
        return 1

if __name__ == "__main__":
    exit_code = main()
    sys.exit(exit_code)