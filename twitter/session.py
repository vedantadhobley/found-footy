"""Twitter Session Manager - Browser automation for Twitter scraping

Manages persistent browser session with authentication.
Uses a two-mode approach:
1. Manual login: Firefox launched WITHOUT Selenium (no bot detection)
2. Scraping: Selenium uses the same profile (already authenticated)

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
        self.last_activity = 0
        self.startup_complete = False
        self.manual_firefox_pid = None  # Track manually launched Firefox
        self._initialized = True
        
        # Profile directory shared between manual Firefox and Selenium
        self.profile_dir = "/data/firefox_profile"
        
        # Backup cookie file - configurable via env var, defaults to ~/.config/found-footy/
        # This should be OUTSIDE the repo to avoid accidental commits
        default_backup_path = os.path.expanduser("~/.config/found-footy/twitter_cookies.json")
        self.cookie_backup_file = os.environ.get('TWITTER_COOKIE_BACKUP_PATH', default_backup_path)
        
        # Ensure backup directory exists
        backup_dir = os.path.dirname(self.cookie_backup_file)
        if backup_dir:
            os.makedirs(backup_dir, exist_ok=True)
        
        # Notification state - only notify once per "login required" session
        self._login_notification_sent = False
        self._login_notification_file = "/data/.login_notification_sent"
        
        # Check if we already sent notification (persists across container restarts)
        if os.path.exists(self._login_notification_file):
            self._login_notification_sent = True
        
        print(f"🔧 TwitterSessionManager initialized")
        print(f"   📁 Cookie backup: {self.cookie_backup_file}")
    
    def _setup_browser(self, headless: bool = None) -> bool:
        """Setup Firefox browser with Selenium (for scraping, not login)
        
        Args:
            headless: Run in headless mode (default: from config)
            
        Returns:
            True if browser setup successful, False otherwise
        """
        if headless is None:
            headless = self.config.headless
        
        try:
            options = FirefoxOptions()
            
            if headless:
                options.add_argument("--headless")
            
            # Use persistent profile directory (same as manual Firefox)
            os.makedirs(self.profile_dir, exist_ok=True)
            options.add_argument("-profile")
            options.add_argument(self.profile_dir)
            
            print(f"   📁 Using Firefox profile: {self.profile_dir}", flush=True)
            
            # Create Firefox instance
            service = FirefoxService(executable_path="/usr/local/bin/geckodriver")
            self.driver = webdriver.Firefox(service=service, options=options)
            
            mode = "headless" if headless else "GUI"
            print(f"✅ Browser session created ({mode} mode, Firefox)", flush=True)
            return True
            
        except Exception as e:
            print(f"❌ Browser setup failed: {e}", flush=True)
            import traceback
            traceback.print_exc()
            return False
    
    def _load_cookies_from_file(self) -> bool:
        """Load cookies from exported cookie file
        
        Returns:
            True if cookies loaded and valid, False otherwise
        """
        cookie_file = "/data/twitter_cookies.pkl"
        if not os.path.exists(cookie_file):
            print("   ⚠️  No cookie file found at /data/twitter_cookies.pkl", flush=True)
            return False
        
        try:
            with open(cookie_file, 'rb') as f:
                cookies = pickle.load(f)
            
            print(f"   📦 Found {len(cookies)} cookies in file", flush=True)
            
            # Navigate to twitter first (cookies need matching domain)
            self.driver.get("https://x.com")
            time.sleep(2)
            
            # Add each cookie
            added = 0
            for cookie in cookies:
                try:
                    # Selenium needs specific format
                    selenium_cookie = {
                        'name': cookie['name'],
                        'value': cookie['value'],
                        'domain': cookie.get('domain', '.x.com'),
                        'path': cookie.get('path', '/'),
                        'secure': cookie.get('secure', True),
                    }
                    self.driver.add_cookie(selenium_cookie)
                    added += 1
                except Exception as e:
                    pass  # Some cookies might not be valid for this domain
            
            print(f"   ✅ Added {added} cookies to browser", flush=True)
            
            # Verify login by going to home
            self.driver.get("https://x.com/home")
            time.sleep(3)
            
            current_url = self.driver.current_url
            if "home" in current_url and "login" not in current_url:
                print("   ✅ Cookies valid - logged in!", flush=True)
                return True
            else:
                print("   ❌ Cookies expired or invalid", flush=True)
                return False
                
        except Exception as e:
            print(f"   ❌ Failed to load cookies: {e}", flush=True)
            return False
    
    def _backup_cookies_to_host(self) -> bool:
        """Export current session cookies to host-mounted backup file
        
        This file survives volume deletes since it's in /workspace (mounted from host).
        
        Returns:
            True if backup successful
        """
        if not self.driver or not self.authenticated:
            return False
        
        try:
            # Get all cookies from Selenium
            cookies = self.driver.get_cookies()
            
            # Filter to Twitter cookies only
            twitter_cookies = [c for c in cookies if 'twitter' in c.get('domain', '') or 'x.com' in c.get('domain', '')]
            
            if not twitter_cookies:
                print("   ⚠️  No Twitter cookies to backup", flush=True)
                return False
            
            # Save to JSON (human readable, easy to debug)
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
    
    def _restore_cookies_from_backup(self) -> bool:
        """Restore cookies from host-mounted backup file
        
        Returns:
            True if restore successful and login valid
        """
        if not os.path.exists(self.cookie_backup_file):
            print("   ⚠️  No cookie backup found", flush=True)
            return False
        
        try:
            with open(self.cookie_backup_file, 'r') as f:
                backup_data = json.load(f)
            
            cookies = backup_data.get('cookies', [])
            exported_at = backup_data.get('exported_at', 'unknown')
            
            print(f"   📦 Found backup from {exported_at} with {len(cookies)} cookies", flush=True)
            
            # Need a browser to add cookies
            if not self.driver:
                if not self._setup_browser(headless=True):
                    return False
            
            # Navigate to twitter first (cookies need matching domain)
            self.driver.get("https://x.com")
            time.sleep(2)
            
            # Add each cookie
            added = 0
            for cookie in cookies:
                try:
                    # Clean up cookie for Selenium
                    selenium_cookie = {
                        'name': cookie['name'],
                        'value': cookie['value'],
                        'domain': cookie.get('domain', '.x.com'),
                        'path': cookie.get('path', '/'),
                        'secure': cookie.get('secure', True),
                    }
                    # Remove expiry if it's in the past or invalid
                    if 'expiry' in cookie:
                        selenium_cookie['expiry'] = int(cookie['expiry'])
                    
                    self.driver.add_cookie(selenium_cookie)
                    added += 1
                except Exception as e:
                    pass  # Some cookies might fail
            
            print(f"   ✅ Restored {added} cookies from backup", flush=True)
            
            # Verify login
            self.driver.get("https://x.com/home")
            time.sleep(3)
            
            current_url = self.driver.current_url
            if "home" in current_url and "login" not in current_url and "flow" not in current_url:
                print("   ✅ Backup cookies valid - logged in!", flush=True)
                self.authenticated = True
                self.last_activity = time.time()
                return True
            else:
                print("   ❌ Backup cookies expired", flush=True)
                self.driver.quit()
                self.driver = None
                return False
                
        except Exception as e:
            print(f"   ❌ Cookie restore failed: {e}", flush=True)
            if self.driver:
                try:
                    self.driver.quit()
                except:
                    pass
                self.driver = None
            return False
    
    def _open_browser_on_host(self, url: str):
        """Try to open a browser on the HOST machine (not container)
        
        Uses a simple webhook/file-based approach since we can't directly
        open browsers from inside Docker.
        """
        # Write a signal file that a host-side watcher could pick up
        signal_file = "/workspace/.open_vnc_signal"
        try:
            with open(signal_file, 'w') as f:
                f.write(f"{url}\n{time.time()}")
            print(f"   📝 Signal file written: {signal_file}", flush=True)
        except:
            pass
    
    def _send_login_notification(self, vnc_url: str) -> bool:
        """Send email notification that Twitter login is required
        
        Only sends once per "login required" session to avoid spam.
        Clears the flag when login succeeds.
        
        Args:
            vnc_url: The VNC URL to include in the notification
            
        Returns:
            True if notification sent successfully
        """
        # Don't spam - only send once
        if self._login_notification_sent:
            print("   📧 Notification already sent (skipping)", flush=True)
            return False
        
        # Get notification config from environment
        notify_email = os.environ.get('TWITTER_NOTIFY_EMAIL', '')
        smtp_host = os.environ.get('SMTP_HOST', 'smtp.gmail.com')
        smtp_port = int(os.environ.get('SMTP_PORT', '587'))
        smtp_user = os.environ.get('SMTP_USER', '')
        smtp_pass = os.environ.get('SMTP_PASS', '')
        
        if not notify_email:
            print("   📧 No TWITTER_NOTIFY_EMAIL configured", flush=True)
            return False
        
        if not smtp_user or not smtp_pass:
            print("   📧 SMTP credentials not configured (SMTP_USER/SMTP_PASS)", flush=True)
            return False
        
        try:
            # Get hostname for context
            hostname = os.environ.get('HOSTNAME', 'unknown')
            environment = os.environ.get('ENVIRONMENT', 'development')
            
            # Create email
            msg = MIMEMultipart('alternative')
            msg['Subject'] = f'🔐 Twitter Login Required - Found Footy ({environment})'
            msg['From'] = smtp_user
            msg['To'] = notify_email
            
            # Plain text version
            text = f"""
Twitter Login Required!

The Found Footy Twitter scraper needs you to login.

Environment: {environment}
Container: {hostname}
VNC URL: {vnc_url}

Steps:
1. Open the VNC URL above in your browser
2. Login to Twitter in the Firefox window
3. The system will auto-detect login and resume scraping

This is a one-time notification. You won't receive another until the next time login is required.

---
Found Footy Automated Notification
"""
            
            # HTML version
            html = f"""
<html>
<body style="font-family: Arial, sans-serif; max-width: 600px; margin: 0 auto;">
    <div style="background: #1DA1F2; color: white; padding: 20px; text-align: center;">
        <h1 style="margin: 0;">🔐 Twitter Login Required</h1>
    </div>
    <div style="padding: 20px; background: #f5f5f5;">
        <p>The Found Footy Twitter scraper needs you to login.</p>
        
        <table style="width: 100%; margin: 20px 0;">
            <tr><td><strong>Environment:</strong></td><td>{environment}</td></tr>
            <tr><td><strong>Container:</strong></td><td>{hostname}</td></tr>
        </table>
        
        <div style="text-align: center; margin: 30px 0;">
            <a href="{vnc_url}" style="background: #1DA1F2; color: white; padding: 15px 30px; text-decoration: none; border-radius: 5px; font-size: 18px;">
                Open VNC Login
            </a>
        </div>
        
        <h3>Steps:</h3>
        <ol>
            <li>Click the button above to open VNC</li>
            <li>Login to Twitter in the Firefox window</li>
            <li>The system will auto-detect login and resume scraping</li>
        </ol>
        
        <p style="color: #666; font-size: 12px; margin-top: 30px;">
            This is a one-time notification. You won't receive another until the next time login is required.
        </p>
    </div>
</body>
</html>
"""
            
            msg.attach(MIMEText(text, 'plain'))
            msg.attach(MIMEText(html, 'html'))
            
            # Send email
            print(f"   📧 Sending notification to {notify_email}...", flush=True)
            
            with smtplib.SMTP(smtp_host, smtp_port) as server:
                server.starttls()
                server.login(smtp_user, smtp_pass)
                server.send_message(msg)
            
            # Mark as sent (persists across restarts)
            self._login_notification_sent = True
            with open(self._login_notification_file, 'w') as f:
                f.write(time.strftime("%Y-%m-%dT%H:%M:%SZ"))
            
            print(f"   ✅ Notification sent to {notify_email}", flush=True)
            return True
            
        except Exception as e:
            print(f"   ❌ Failed to send notification: {e}", flush=True)
            return False
    
    def _clear_login_notification_flag(self):
        """Clear the notification flag when login succeeds
        
        This allows a new notification to be sent next time login is required.
        """
        self._login_notification_sent = False
        try:
            if os.path.exists(self._login_notification_file):
                os.remove(self._login_notification_file)
        except:
            pass
    
    def _launch_manual_firefox(self, url: str = "https://x.com/i/flow/login") -> bool:
        """Launch Firefox directly (without Selenium) for manual login
        
        This bypasses bot detection since there's no webdriver flag.
        
        Args:
            url: URL to open (default: Twitter login)
            
        Returns:
            True if Firefox launched successfully
        """
        try:
            # Kill any existing Firefox first
            subprocess.run(["pkill", "-9", "firefox"], capture_output=True)
            time.sleep(1)
            
            # Launch Firefox directly with the shared profile
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
            print(f"❌ Failed to launch manual Firefox: {e}", flush=True)
            return False
    
    def _kill_manual_firefox(self):
        """Kill the manually launched Firefox"""
        try:
            subprocess.run(["pkill", "-9", "firefox"], capture_output=True)
            self.manual_firefox_pid = None
            print("🔪 Manual Firefox killed", flush=True)
        except:
            pass
    
    def _check_profile_authenticated(self) -> bool:
        """Check if the Firefox profile has valid Twitter session
        
        Uses Selenium briefly to check, then closes it.
        
        Returns:
            True if profile is authenticated
        """
        try:
            # Setup Selenium with the shared profile
            if not self._setup_browser(headless=True):
                return False
            
            # Try to access Twitter home
            self.driver.get("https://x.com/home")
            time.sleep(3)
            
            current_url = self.driver.current_url
            is_authenticated = "home" in current_url and "login" not in current_url and "flow" not in current_url
            
            if is_authenticated:
                # Keep the browser open for scraping
                self.authenticated = True
                self.last_activity = time.time()
                return True
            else:
                # Close browser - need manual login
                self.driver.quit()
                self.driver = None
                return False
                
        except Exception as e:
            print(f"   ❌ Auth check failed: {e}", flush=True)
            if self.driver:
                try:
                    self.driver.quit()
                except:
                    pass
                self.driver = None
            return False
    
    def authenticate(self, force_reauth: bool = False) -> bool:
        """Authenticate with Twitter
        
        If not authenticated, launches manual Firefox for login.
        
        Args:
            force_reauth: Force re-authentication even if already authenticated
            
        Returns:
            True if authenticated, False otherwise
        """
        # Check if already authenticated and session is valid
        if self.authenticated and not force_reauth and self._session_valid():
            print("✅ Already authenticated - session valid")
            return True
        
        # Close existing Selenium browser if force reauth
        if force_reauth and self.driver:
            try:
                self.driver.quit()
            except:
                pass
            self.driver = None
            self.authenticated = False
        
        # Check if profile is authenticated (via Selenium)
        if self._check_profile_authenticated():
            print("✅ Twitter authentication successful (from saved profile)")
            return True
        
        # Not authenticated - need manual login
        print("❌ Not authenticated - launching Firefox for manual login...")
        print("   📺 Open VNC at http://localhost:4103 to login", flush=True)
        self._launch_manual_firefox()
        
        return False
    
    def _session_valid(self) -> bool:
        """Check if current session is still valid
        
        Returns:
            True if session is valid, False otherwise
        """
        if not self.driver or not self.authenticated:
            return False
        
        # Check timeout
        if time.time() - self.last_activity > self.config.session_timeout:
            print("⚠️  Session timeout exceeded")
            return False
        
        # Check if browser is still alive
        try:
            _ = self.driver.current_url
            return True
        except WebDriverException:
            print("⚠️  Browser session lost")
            self.authenticated = False
            return False
    
    def search_videos(self, search_query: str, max_results: int = None) -> List[Dict[str, Any]]:
        """Search Twitter for videos matching query
        
        Args:
            search_query: Search terms (e.g., "Messi Barcelona")
            max_results: Maximum number of videos to return (default: from config)
            
        Returns:
            List of video dictionaries with metadata
        """
        if max_results is None:
            max_results = self.config.default_max_results
        
        # Ensure authenticated
        if not self.authenticate():
            print("❌ Cannot search - authentication failed")
            return []
        
        try:
            # Build search URL with video filter
            video_search_query = f"{search_query} filter:videos"
            search_url = f"https://twitter.com/search?q={quote(video_search_query)}&src=typed_query&f=live"
            
            print(f"🔍 Searching: {search_url}")
            self.driver.get(search_url)
            time.sleep(self.config.search_timeout)
            self.last_activity = time.time()
            
            # Extract tweet elements
            tweet_elements = self.driver.find_elements(By.CSS_SELECTOR, "article[data-testid='tweet']")
            print(f"🔍 Found {len(tweet_elements)} tweets to check")
            
            discovered_videos = []
            
            for i, tweet_element in enumerate(tweet_elements[:20]):  # Check up to 20 tweets
                try:
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
                    
                    # Check for video presence
                    has_video = False
                    video_selectors = [
                        "video",
                        "[data-testid='videoPlayer']",
                        "[data-testid='videoComponent']"
                    ]
                    
                    for selector in video_selectors:
                        try:
                            if tweet_element.find_elements(By.CSS_SELECTOR, selector):
                                has_video = True
                                break
                        except:
                            continue
                    
                    # Add video if found
                    if has_video and tweet_url:
                        tweet_id = tweet_url.split("/status/")[-1] if "/status/" in tweet_url else f"unknown_{i}"
                        
                        video_entry = {
                            "search_term": search_query,
                            "tweet_url": tweet_url,
                            "tweet_id": tweet_id,
                            "tweet_text": tweet_text[:200],  # Truncate to 200 chars
                            "username": "Unknown",  # Could extract if needed
                            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                            "discovered_at": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                            "search_index": 0,
                            "video_index": len(discovered_videos),
                            "source": "browser_automation",
                            "requires_ytdlp": True,
                            "video_page_url": f"https://x.com/i/status/{tweet_id}"
                        }
                        
                        discovered_videos.append(video_entry)
                        print(f"✅ Found video #{len(discovered_videos)}: {tweet_text[:50]}...")
                        
                        if len(discovered_videos) >= max_results:
                            break
                
                except Exception as e:
                    # Skip problematic tweets
                    continue
            
            print(f"✅ Search complete - found {len(discovered_videos)} videos")
            return discovered_videos
            
        except Exception as e:
            print(f"❌ Search failed: {e}")
            import traceback
            traceback.print_exc()
            return []
    
    def startup(self) -> bool:
        """Startup routine - Check profile auth, launch manual Firefox if needed
        
        Authentication priority:
        1. Check existing Firefox profile
        2. Try restore from host backup (survives volume deletes)
        3. Launch manual Firefox for login + send notification
        
        Returns:
            True if authenticated, False if manual login needed
        """
        print("🚀 Starting Twitter Session Service...", flush=True)
        print("   📺 VNC GUI available at: http://localhost:4103", flush=True)
        print(flush=True)
        
        # Determine VNC URL based on environment
        vnc_host = os.environ.get('VNC_PUBLIC_URL', 'http://localhost:4103')
        
        with self._lock:
            self.startup_complete = True
            
            # Ensure profile directory exists
            os.makedirs(self.profile_dir, exist_ok=True)
            
            # 1. Check if already authenticated via saved profile
            print("🔍 Checking Twitter login status...", flush=True)
            
            if self._check_profile_authenticated():
                print("✅ Already logged in via saved profile!", flush=True)
                print("   🤖 Selenium ready for scraping", flush=True)
                # Clear notification flag since we're logged in
                self._clear_login_notification_flag()
                # Backup cookies in case of future volume delete
                self._backup_cookies_to_host()
                print(flush=True)
                return True
            
            # 2. Try to restore from host backup
            print("🔍 Checking for cookie backup...", flush=True)
            if self._restore_cookies_from_backup():
                print("✅ Restored from cookie backup!", flush=True)
                print("   🤖 Selenium ready for scraping", flush=True)
                self._clear_login_notification_flag()
                print(flush=True)
                return True
            
            # 3. Not authenticated - launch manual Firefox for login
            print(flush=True)
            print("=" * 60, flush=True)
            print("🔐 TWITTER LOGIN REQUIRED", flush=True)
            print("=" * 60, flush=True)
            print(flush=True)
            print(f"   VNC URL: {vnc_host}", flush=True)
            print(flush=True)
            print("   After login, it will auto-verify in ~30 seconds", flush=True)
            print("   Or call: curl -X POST http://localhost:8888/auth/verify", flush=True)
            print(flush=True)
            print("=" * 60, flush=True)
            
            # Send notification (email) - only sends once
            self._send_login_notification(vnc_host)
            
            # Signal host to open browser (dev only, when workspace is mounted)
            self._open_browser_on_host(vnc_host)
            
            # Launch manual Firefox
            self._launch_manual_firefox()
            
            # Start background thread to auto-verify login
            def auto_verify_loop():
                """Background thread that checks for login completion"""
                check_interval = 10  # seconds
                max_wait = 86400  # 24 hours - be patient!
                waited = 0
                
                while waited < max_wait and not self.authenticated:
                    time.sleep(check_interval)
                    waited += check_interval
                    
                    # Check if user logged in by looking at cookies file
                    cookies_db = os.path.join(self.profile_dir, "cookies.sqlite")
                    if os.path.exists(cookies_db):
                        # Cookies file was modified recently?
                        mtime = os.path.getmtime(cookies_db)
                        if time.time() - mtime < check_interval + 5:
                            print(f"🔍 Detected cookie change, verifying login...", flush=True)
                            # Give Firefox a moment to finish writing
                            time.sleep(3)
                            
                            # Kill Firefox and check with Selenium
                            self._kill_manual_firefox()
                            time.sleep(2)
                            
                            if self._check_profile_authenticated():
                                print("✅ Auto-verified! Login successful!", flush=True)
                                self._clear_login_notification_flag()
                                self._backup_cookies_to_host()
                                return
                            else:
                                # Not logged in yet, relaunch Firefox
                                print("   Still not logged in, continuing to wait...", flush=True)
                                self._launch_manual_firefox()
                
                if not self.authenticated:
                    print("⚠️  Auto-verify timeout - call /auth/verify manually", flush=True)
            
            verify_thread = threading.Thread(target=auto_verify_loop, daemon=True)
            verify_thread.start()
            
            return False
    
    def verify_and_switch_to_selenium(self) -> bool:
        """After manual login, verify auth and switch to Selenium for scraping
        
        Call this after user completes manual login via VNC.
        
        Returns:
            True if authenticated and Selenium ready
        """
        print("🔍 Verifying manual login...", flush=True)
        
        # Kill manual Firefox (saves cookies to profile)
        self._kill_manual_firefox()
        time.sleep(2)  # Let Firefox save profile
        
        # Now check with Selenium
        if self._check_profile_authenticated():
            print("✅ Login verified! Selenium ready for scraping", flush=True)
            # Clear notification flag - login successful
            self._clear_login_notification_flag()
            # Backup cookies to host for persistence
            self._backup_cookies_to_host()
            return True
        else:
            print("❌ Login not detected - please try again", flush=True)
            self._launch_manual_firefox()
            return False
    
    def cleanup(self):
        """Cleanup browser session"""
        if self.driver:
            try:
                self.driver.quit()
                print("🧹 Browser session closed")
            except:
                pass
        
        self.authenticated = False
        self.startup_complete = False
