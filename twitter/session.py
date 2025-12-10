"""Twitter Session Manager - Browser automation for Twitter scraping

Manages persistent browser session with authentication.
Uses a two-mode approach:
1. Manual login: Firefox launched WITHOUT Selenium (no bot detection)
2. Scraping: Selenium uses the same profile (already authenticated)

Authentication flow:
1. Check if existing browser session is valid → use it
2. If browser dead/missing, try to restore from cookie backup → use it  
3. If cookies fail/expired, launch manual Firefox for login
4. If search is attempted without auth, RAISE ERROR (don't silently fail)

Cookie backup: Exports cookies to host-mounted volume for persistence across volume deletes.
Notifications: Sends email when login is required (production).
"""
import os
import time
import json
import pickle
import smtplib
import subprocess
import threading
from email.mime.text import MIMEText
from email.mime.multipart import MIMEMultipart
from typing import List, Dict, Any, Optional
from selenium import webdriver
from selenium.webdriver.firefox.options import Options as FirefoxOptions
from selenium.webdriver.firefox.service import Service as FirefoxService
from selenium.webdriver.common.by import By
from selenium.common.exceptions import WebDriverException
from urllib.parse import quote

from .config import TwitterConfig


class TwitterAuthError(Exception):
    """Raised when Twitter authentication fails and manual login is required"""
    pass


class TwitterSessionManager:
    """Singleton browser session manager for Twitter scraping"""
    
    _instance = None
    _lock = threading.Lock()
    
    def __new__(cls, config: TwitterConfig = None):
        if cls._instance is None:
            with cls._lock:
                if cls._instance is None:
                    cls._instance = super().__new__(cls)
                    cls._instance._initialized = False
        return cls._instance
    
    def __init__(self, config: TwitterConfig = None):
        if self._initialized:
            return
        
        self.config = config or TwitterConfig()
        self.driver: Optional[webdriver.Firefox] = None
        self.authenticated = False
        self.last_activity = time.time()  # Initialize to now, not 0!
        self.startup_complete = False
        self.manual_firefox_pid = None
        self._initialized = True
        
        # Profile directory shared between manual Firefox and Selenium
        self.profile_dir = "/data/firefox_profile"
        
        # Backup cookie file - configurable via env var
        default_backup_path = os.path.expanduser("~/.config/found-footy/twitter_cookies.json")
        self.cookie_backup_file = os.environ.get('TWITTER_COOKIE_BACKUP_PATH', default_backup_path)
        
        # Ensure backup directory exists
        backup_dir = os.path.dirname(self.cookie_backup_file)
        if backup_dir:
            os.makedirs(backup_dir, exist_ok=True)
        
        # Notification state
        self._login_notification_sent = False
        self._login_notification_file = "/data/.login_notification_sent"
        
        if os.path.exists(self._login_notification_file):
            self._login_notification_sent = True
        
        print(f"🔧 TwitterSessionManager initialized")
        print(f"   📁 Cookie backup: {self.cookie_backup_file}")
    
    def _setup_browser(self, headless: bool = None) -> bool:
        """Setup Firefox browser with Selenium
        
        Returns:
            True if browser setup successful
        """
        if headless is None:
            headless = self.config.headless
        
        try:
            # Close existing browser if any
            if self.driver:
                try:
                    self.driver.quit()
                except:
                    pass
                self.driver = None
            
            options = FirefoxOptions()
            
            if headless:
                options.add_argument("--headless")
            
            os.makedirs(self.profile_dir, exist_ok=True)
            options.add_argument("-profile")
            options.add_argument(self.profile_dir)
            
            print(f"   📁 Using Firefox profile: {self.profile_dir}", flush=True)
            
            service = FirefoxService(executable_path="/usr/local/bin/geckodriver")
            self.driver = webdriver.Firefox(service=service, options=options)
            
            mode = "headless" if headless else "GUI"
            print(f"✅ Browser created ({mode} mode)", flush=True)
            return True
            
        except Exception as e:
            print(f"❌ Browser setup failed: {e}", flush=True)
            import traceback
            traceback.print_exc()
            return False
    
    def _verify_logged_in(self) -> bool:
        """Check if current browser session is logged into Twitter
        
        Returns:
            True if logged in, False otherwise
        """
        if not self.driver:
            return False
        
        try:
            self.driver.get("https://x.com/home")
            time.sleep(3)
            
            current_url = self.driver.current_url
            # Check for login/flow redirects which indicate NOT logged in
            if "login" in current_url or "flow" in current_url:
                print(f"   ❌ Not logged in (redirected to: {current_url})", flush=True)
                return False
            
            if "home" in current_url:
                print(f"   ✅ Logged in (at: {current_url})", flush=True)
                return True
            
            print(f"   ⚠️  Unknown state (at: {current_url})", flush=True)
            return False
            
        except Exception as e:
            print(f"   ❌ Login check failed: {e}", flush=True)
            return False
    
    def _restore_cookies_from_backup(self) -> bool:
        """Restore cookies from host-mounted backup file
        
        This is the PRIMARY method for restoring auth after container restart.
        
        Returns:
            True if restore successful AND login verified
        """
        if not os.path.exists(self.cookie_backup_file):
            print(f"   ⚠️  No cookie backup at {self.cookie_backup_file}", flush=True)
            return False
        
        try:
            with open(self.cookie_backup_file, 'r') as f:
                backup_data = json.load(f)
            
            cookies = backup_data.get('cookies', [])
            exported_at = backup_data.get('exported_at', 'unknown')
            
            print(f"   📦 Found backup from {exported_at} with {len(cookies)} cookies", flush=True)
            
            if not cookies:
                print(f"   ❌ Backup file is empty", flush=True)
                return False
            
            # Check for critical auth cookie
            cookie_names = [c.get('name') for c in cookies]
            if 'auth_token' not in cookie_names:
                print(f"   ❌ Backup missing auth_token cookie", flush=True)
                return False
            
            # Setup browser if needed
            if not self.driver:
                if not self._setup_browser(headless=True):
                    return False
            
            # Navigate to Twitter (cookies need matching domain)
            print(f"   🌐 Navigating to x.com...", flush=True)
            self.driver.get("https://x.com")
            time.sleep(2)
            
            # Clear any existing cookies first
            self.driver.delete_all_cookies()
            
            # Add cookies from backup
            added = 0
            failed = 0
            for cookie in cookies:
                try:
                    selenium_cookie = {
                        'name': cookie['name'],
                        'value': cookie['value'],
                        'domain': cookie.get('domain', '.x.com'),
                        'path': cookie.get('path', '/'),
                        'secure': cookie.get('secure', True),
                    }
                    if 'expiry' in cookie:
                        selenium_cookie['expiry'] = int(cookie['expiry'])
                    
                    self.driver.add_cookie(selenium_cookie)
                    added += 1
                except Exception as e:
                    failed += 1
            
            print(f"   ✅ Added {added} cookies ({failed} failed)", flush=True)
            
            # VERIFY login actually works
            print(f"   🔍 Verifying login...", flush=True)
            if self._verify_logged_in():
                self.authenticated = True
                self.last_activity = time.time()
                print(f"   ✅ Cookie restore SUCCESSFUL - authenticated!", flush=True)
                return True
            else:
                print(f"   ❌ Cookies restored but login FAILED - cookies may be expired", flush=True)
                self.authenticated = False
                return False
                
        except Exception as e:
            print(f"   ❌ Cookie restore error: {e}", flush=True)
            import traceback
            traceback.print_exc()
            return False
    
    def _backup_cookies_to_host(self) -> bool:
        """Export current session cookies to host-mounted backup file
        
        Returns:
            True if backup successful
        """
        if not self.driver:
            print("   ⚠️  No browser to backup cookies from", flush=True)
            return False
        
        try:
            cookies = self.driver.get_cookies()
            
            # Filter to Twitter cookies
            twitter_cookies = [c for c in cookies if 'x.com' in c.get('domain', '')]
            
            if not twitter_cookies:
                print("   ⚠️  No Twitter cookies to backup", flush=True)
                return False
            
            # Check for auth_token
            has_auth = any(c['name'] == 'auth_token' for c in twitter_cookies)
            if not has_auth:
                print("   ⚠️  No auth_token in cookies - not backing up", flush=True)
                return False
            
            backup_data = {
                'exported_at': time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                'cookies': twitter_cookies
            }
            
            with open(self.cookie_backup_file, 'w') as f:
                json.dump(backup_data, f, indent=2)
            
            print(f"   💾 Backed up {len(twitter_cookies)} cookies to {self.cookie_backup_file}", flush=True)
            return True
            
        except Exception as e:
            print(f"   ❌ Cookie backup failed: {e}", flush=True)
            return False
    
    def _launch_manual_firefox(self, url: str = "https://x.com/i/flow/login") -> bool:
        """Launch Firefox WITHOUT Selenium for manual login (bypasses bot detection)"""
        try:
            subprocess.run(["pkill", "-9", "firefox"], capture_output=True)
            time.sleep(1)
            
            env = os.environ.copy()
            env["DISPLAY"] = ":99"
            env["HOME"] = "/root"
            
            proc = subprocess.Popen(
                ["firefox", "--new-instance", "-profile", self.profile_dir, url],
                env=env,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL
            )
            
            self.manual_firefox_pid = proc.pid
            print(f"🦊 Manual Firefox launched (PID: {proc.pid})", flush=True)
            return True
            
        except Exception as e:
            print(f"❌ Failed to launch Firefox: {e}", flush=True)
            return False
    
    def _kill_manual_firefox(self):
        """Kill the manually launched Firefox"""
        try:
            subprocess.run(["pkill", "-9", "firefox"], capture_output=True)
            self.manual_firefox_pid = None
        except:
            pass
    
    def _send_login_notification(self, vnc_url: str) -> bool:
        """Send email notification that login is required"""
        if self._login_notification_sent:
            return False
        
        notify_email = os.environ.get('TWITTER_NOTIFY_EMAIL', '')
        smtp_host = os.environ.get('SMTP_HOST', 'smtp.gmail.com')
        smtp_port = int(os.environ.get('SMTP_PORT', '587'))
        smtp_user = os.environ.get('SMTP_USER', '')
        smtp_pass = os.environ.get('SMTP_PASS', '')
        
        if not all([notify_email, smtp_user, smtp_pass]):
            return False
        
        try:
            hostname = os.environ.get('HOSTNAME', 'unknown')
            environment = os.environ.get('ENVIRONMENT', 'development')
            
            msg = MIMEMultipart('alternative')
            msg['Subject'] = f'🔐 Twitter Login Required - Found Footy ({environment})'
            msg['From'] = smtp_user
            msg['To'] = notify_email
            
            text = f"""Twitter Login Required!
            
VNC URL: {vnc_url}
Environment: {environment}

Steps:
1. Open VNC URL
2. Login to Twitter
3. System auto-detects login"""
            
            msg.attach(MIMEText(text, 'plain'))
            
            with smtplib.SMTP(smtp_host, smtp_port) as server:
                server.starttls()
                server.login(smtp_user, smtp_pass)
                server.send_message(msg)
            
            self._login_notification_sent = True
            with open(self._login_notification_file, 'w') as f:
                f.write(time.strftime("%Y-%m-%dT%H:%M:%SZ"))
            
            print(f"   📧 Notification sent to {notify_email}", flush=True)
            return True
            
        except Exception as e:
            print(f"   ❌ Notification failed: {e}", flush=True)
            return False
    
    def _clear_login_notification_flag(self):
        """Clear notification flag when login succeeds"""
        self._login_notification_sent = False
        try:
            if os.path.exists(self._login_notification_file):
                os.remove(self._login_notification_file)
        except:
            pass
    
    def ensure_authenticated(self) -> bool:
        """Ensure we have a valid authenticated session.
        
        This is the MAIN authentication method. It tries everything before failing.
        
        Flow:
        1. If browser exists and authenticated flag set → verify it's still valid
        2. If browser dead/missing → try restore from cookie backup
        3. If cookies fail → we need manual login (returns False)
        
        Returns:
            True if authenticated and ready for search
            False if manual login required (caller should handle gracefully)
        """
        print("🔐 Ensuring Twitter authentication...", flush=True)
        
        # Step 1: Check if existing session is valid
        if self.driver and self.authenticated:
            print("   📋 Checking existing session...", flush=True)
            try:
                # Quick check - is browser alive?
                _ = self.driver.current_url
                
                # Full verify - are we actually logged in?
                if self._verify_logged_in():
                    self.last_activity = time.time()
                    print("✅ Existing session is valid", flush=True)
                    return True
                else:
                    print("   ⚠️  Session invalid, will try cookie restore...", flush=True)
                    self.authenticated = False
                    # Close the invalid browser
                    try:
                        self.driver.quit()
                    except:
                        pass
                    self.driver = None
            except WebDriverException:
                print("   ⚠️  Browser session died, will try cookie restore...", flush=True)
                self.driver = None
                self.authenticated = False
        
        # Step 2: Try to restore from cookie backup
        print("   🍪 Attempting cookie restore...", flush=True)
        if self._restore_cookies_from_backup():
            self._backup_cookies_to_host()  # Re-backup in case format changed
            self._clear_login_notification_flag()
            print("✅ Authenticated via cookie restore!", flush=True)
            return True
        
        # Step 3: Try checking profile directly (maybe manual login happened)
        print("   📁 Checking Firefox profile...", flush=True)
        if self._setup_browser(headless=True) and self._verify_logged_in():
            self.authenticated = True
            self.last_activity = time.time()
            self._backup_cookies_to_host()
            self._clear_login_notification_flag()
            print("✅ Authenticated via Firefox profile!", flush=True)
            return True
        
        # Step 4: All automatic methods failed - need manual login
        print("❌ All authentication methods failed - manual login required", flush=True)
        return False
    
    def search_videos(self, search_query: str, max_results: int = None, exclude_urls: List[str] = None) -> List[Dict[str, Any]]:
        """Search Twitter for videos matching query
        
        Args:
            search_query: Search terms (e.g., "Messi goal Barcelona")
            max_results: Maximum videos to return
            exclude_urls: List of URLs to skip (already processed videos)
            
        Returns:
            List of video dictionaries
            
        Raises:
            TwitterAuthError: If not authenticated and can't auto-restore
        """
        if max_results is None:
            max_results = self.config.default_max_results
        
        if exclude_urls is None:
            exclude_urls = []
        
        # Normalize exclude_urls for comparison
        exclude_set = set(exclude_urls)
        
        # CRITICAL: Ensure we're authenticated before searching
        if not self.ensure_authenticated():
            # This is a FAILURE - we cannot search without auth
            error_msg = (
                "Twitter authentication failed! Manual login required. "
                f"Open VNC at http://localhost:4103 to login. "
                f"Cookie backup exists: {os.path.exists(self.cookie_backup_file)}"
            )
            print(f"❌ {error_msg}", flush=True)
            raise TwitterAuthError(error_msg)
        
        if exclude_urls:
            print(f"🔍 Searching: {search_query} (excluding {len(exclude_urls)} already-discovered URLs)", flush=True)
        else:
            print(f"🔍 Searching: {search_query}", flush=True)
        
        try:
            # Build search URL with video filter
            video_search_query = f"{search_query} filter:videos"
            search_url = f"https://twitter.com/search?q={quote(video_search_query)}&src=typed_query&f=live"
            
            print(f"   URL: {search_url}", flush=True)
            
            # Set a reasonable timeout - if page takes >30s, something is WRONG
            self.driver.set_page_load_timeout(30)
            
            try:
                self.driver.get(search_url)
            except Exception as e:
                # Page load timeout or other error
                print(f"❌ Page load failed after 30s: {e}", flush=True)
                print(f"   Current URL: {self.driver.current_url}", flush=True)
                
                # Try to get page source to see if we got blocked/challenged
                try:
                    page_source = self.driver.page_source[:500]
                    print(f"   Page preview: {page_source}", flush=True)
                except:
                    pass
                
                raise RuntimeError(f"Twitter search page failed to load within 30s: {e}")
            
            time.sleep(self.config.search_timeout)
            self.last_activity = time.time()
            
            # Double-check we didn't get logged out during navigation
            current_url = self.driver.current_url
            if "login" in current_url or "flow" in current_url:
                self.authenticated = False
                raise TwitterAuthError(f"Got logged out during search! Redirected to: {current_url}")
            
            # Extract tweets - scan up to 50 tweets to find videos
            tweet_elements = self.driver.find_elements(By.CSS_SELECTOR, "article[data-testid='tweet']")
            print(f"   📄 Found {len(tweet_elements)} tweets", flush=True)
            
            discovered_videos = []
            
            for i, tweet_element in enumerate(tweet_elements[:50]):
                try:
                    # Skip promoted/ad tweets
                    try:
                        # Check for "Promoted" label or ad indicators
                        promoted_indicators = tweet_element.find_elements(By.XPATH, ".//*[contains(text(), 'Promoted') or contains(text(), 'Ad')]")
                        if promoted_indicators:
                            print(f"   ⏭️  Skipping promoted tweet", flush=True)
                            continue
                    except:
                        pass
                    
                    # Extract tweet text
                    tweet_text = "Text not found"
                    try:
                        text_element = tweet_element.find_element(By.CSS_SELECTOR, "[data-testid='tweetText']")
                        if text_element:
                            tweet_text = text_element.text.strip()
                    except:
                        pass
                    
                    # Extract tweet URL
                    tweet_url = None
                    try:
                        status_links = tweet_element.find_elements(By.CSS_SELECTOR, "a[href*='/status/']")
                        for link in status_links:
                            link_href = link.get_attribute("href")
                            if link_href and "/status/" in link_href:
                                tweet_url = link_href
                                break
                    except:
                        pass
                    
                    # Check for video and extract duration
                    has_video = False
                    video_duration_seconds = None
                    
                    for selector in ["video", "[data-testid='videoPlayer']", "[data-testid='videoComponent']"]:
                        try:
                            video_elements = tweet_element.find_elements(By.CSS_SELECTOR, selector)
                            if video_elements:
                                has_video = True
                                
                                # Try to extract duration from video element's duration attribute
                                try:
                                    video_elem = video_elements[0]
                                    duration = video_elem.get_attribute("duration")
                                    if duration:
                                        video_duration_seconds = float(duration)
                                except:
                                    pass
                                
                                # Try to find duration text in player overlay (e.g., "0:15")
                                if not video_duration_seconds:
                                    try:
                                        # Common selectors for video duration display
                                        duration_selectors = [
                                            "[aria-label*='Duration']",
                                            "[data-testid='videoPlayerDuration']",
                                            ".r-1e081e0",  # Twitter's duration class
                                            "div[dir='ltr'][style*='color']",  # Styled duration text
                                        ]
                                        for dur_selector in duration_selectors:
                                            duration_elements = tweet_element.find_elements(By.CSS_SELECTOR, dur_selector)
                                            for dur_elem in duration_elements:
                                                dur_text = dur_elem.text.strip()
                                                # Parse formats like "0:15" or "1:23"
                                                if ":" in dur_text and len(dur_text) <= 6:
                                                    parts = dur_text.split(":")
                                                    if len(parts) == 2:
                                                        try:
                                                            minutes = int(parts[0])
                                                            seconds = int(parts[1])
                                                            video_duration_seconds = minutes * 60 + seconds
                                                            break
                                                        except:
                                                            pass
                                            if video_duration_seconds:
                                                break
                                    except:
                                        pass
                                
                                break
                        except:
                            continue
                    
                    if has_video and tweet_url:
                        # Skip URLs that were already discovered/processed
                        if tweet_url in exclude_set:
                            print(f"   ⏭️ Skipping already-discovered URL: {tweet_url[:60]}...", flush=True)
                            continue
                        
                        tweet_id = tweet_url.split("/status/")[-1] if "/status/" in tweet_url else f"unknown_{i}"
                        
                        video_entry = {
                            "search_term": search_query,
                            "tweet_url": tweet_url,
                            "tweet_id": tweet_id,
                            "tweet_text": tweet_text[:200],
                            "username": "Unknown",
                            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                            "discovered_at": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                            "search_index": 0,
                            "video_index": len(discovered_videos),
                            "source": "browser_automation",
                            "requires_ytdlp": True,
                            "video_page_url": f"https://x.com/i/status/{tweet_id}",
                            "duration_seconds": video_duration_seconds  # May be None if not found
                        }
                        
                        discovered_videos.append(video_entry)
                        print(f"   ✅ Video #{len(discovered_videos)}: {tweet_text[:50]}...", flush=True)
                        
                        if len(discovered_videos) >= max_results:
                            break
                
                except Exception as e:
                    continue
            
            print(f"✅ Search complete - {len(discovered_videos)} videos found", flush=True)
            
            # Backup cookies after successful search (keeps them fresh)
            self._backup_cookies_to_host()
            
            return discovered_videos
            
        except TwitterAuthError:
            raise
        except Exception as e:
            print(f"❌ Search error: {e}", flush=True)
            import traceback
            traceback.print_exc()
            # Re-raise as auth error if it looks like an auth issue
            if "login" in str(e).lower():
                raise TwitterAuthError(f"Search failed due to auth issue: {e}")
            raise
    
    def startup(self) -> bool:
        """Startup routine - try to establish authenticated session
        
        Returns:
            True if authenticated, False if manual login needed
        """
        print("🚀 Starting Twitter Session Service...", flush=True)
        print("   📺 VNC: http://localhost:4103", flush=True)
        print(flush=True)
        
        vnc_host = os.environ.get('VNC_PUBLIC_URL', 'http://localhost:4103')
        
        with self._lock:
            self.startup_complete = True
            os.makedirs(self.profile_dir, exist_ok=True)
            
            # Try to authenticate
            if self.ensure_authenticated():
                print("✅ Twitter service ready!", flush=True)
                return True
            
            # Need manual login
            print(flush=True)
            print("=" * 60, flush=True)
            print("🔐 MANUAL TWITTER LOGIN REQUIRED", flush=True)
            print("=" * 60, flush=True)
            print(f"   VNC URL: {vnc_host}", flush=True)
            print("   Login will be auto-detected", flush=True)
            print("=" * 60, flush=True)
            
            self._send_login_notification(vnc_host)
            self._launch_manual_firefox()
            
            # Start auto-verify thread
            def auto_verify_loop():
                check_interval = 10
                while not self.authenticated:
                    time.sleep(check_interval)
                    
                    cookies_db = os.path.join(self.profile_dir, "cookies.sqlite")
                    if os.path.exists(cookies_db):
                        mtime = os.path.getmtime(cookies_db)
                        if time.time() - mtime < check_interval + 5:
                            print("🔍 Cookie change detected, verifying...", flush=True)
                            time.sleep(3)
                            self._kill_manual_firefox()
                            time.sleep(2)
                            
                            if self._setup_browser(headless=True) and self._verify_logged_in():
                                self.authenticated = True
                                self.last_activity = time.time()
                                self._backup_cookies_to_host()
                                self._clear_login_notification_flag()
                                print("✅ Login successful!", flush=True)
                                return
                            else:
                                self._launch_manual_firefox()
            
            threading.Thread(target=auto_verify_loop, daemon=True).start()
            return False
    
    def verify_and_switch_to_selenium(self) -> bool:
        """After manual login, verify and switch to Selenium"""
        print("🔍 Verifying manual login...", flush=True)
        
        self._kill_manual_firefox()
        time.sleep(2)
        
        if self._setup_browser(headless=True) and self._verify_logged_in():
            self.authenticated = True
            self.last_activity = time.time()
            self._backup_cookies_to_host()
            self._clear_login_notification_flag()
            print("✅ Login verified! Ready for scraping", flush=True)
            return True
        else:
            print("❌ Login not detected", flush=True)
            self._launch_manual_firefox()
            return False
    
    def cleanup(self):
        """Cleanup browser session"""
        if self.driver:
            try:
                self.driver.quit()
            except:
                pass
        self.authenticated = False
        self.startup_complete = False
