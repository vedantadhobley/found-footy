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
from .twitter_logging import log

MODULE = "twitter_session"


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
        self.busy = False  # True when a search is in progress
        self._initialized = True
        
        # Profile directory - use instance-specific path for parallel instances
        # With docker compose --scale, container names are like: found-footy-prod-twitter-1
        # Extract instance number from hostname or use TWITTER_INSTANCE_ID env var
        instance_id = os.environ.get('TWITTER_INSTANCE_ID')
        if not instance_id:
            hostname = os.environ.get('HOSTNAME', '')
            # Try to extract number from end of hostname (e.g., "115ca28fe001" won't work, but that's a container ID)
            # With --scale, the container name is the hostname: found-footy-prod-twitter-1
            # Actually, HOSTNAME is just the container ID. Use a hash of it for uniqueness.
            import hashlib
            instance_id = hashlib.md5(hostname.encode()).hexdigest()[:8]
        self.profile_dir = f"/data/firefox_profile_{instance_id}"
        log.info(MODULE, "profile_dir", "Using Firefox profile", profile_dir=self.profile_dir)
        
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
        
        log.info(MODULE, "initialized", "TwitterSessionManager initialized",
                 cookie_backup=self.cookie_backup_file)
    
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
            
            log.debug(MODULE, "browser_profile", "Using Firefox profile", profile_dir=self.profile_dir)
            
            service = FirefoxService(executable_path="/usr/local/bin/geckodriver")
            self.driver = webdriver.Firefox(service=service, options=options)
            
            mode = "headless" if headless else "GUI"
            log.info(MODULE, "browser_created", "Browser created", mode=mode)
            return True
            
        except Exception as e:
            import traceback
            log.error(MODULE, "browser_setup_failed", "Browser setup failed", error=str(e), traceback=traceback.format_exc())
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
                log.info(MODULE, "not_logged_in", "Not logged in - redirected", current_url=current_url)
                return False
            
            if "home" in current_url:
                log.info(MODULE, "logged_in", "Logged in", current_url=current_url)
                return True
            
            log.warning(MODULE, "login_unknown_state", "Unknown login state", current_url=current_url)
            return False
            
        except Exception as e:
            log.error(MODULE, "login_check_failed", "Login check failed", error=str(e))
            return False
    
    def _restore_cookies_from_backup(self) -> bool:
        """Restore cookies from host-mounted backup file
        
        This is the PRIMARY method for restoring auth after container restart.
        
        Returns:
            True if restore successful AND login verified
        """
        if not os.path.exists(self.cookie_backup_file):
            log.warning(MODULE, "no_cookie_backup", "No cookie backup found",
                        path=self.cookie_backup_file)
            return False
        
        try:
            with open(self.cookie_backup_file, 'r') as f:
                backup_data = json.load(f)
            
            cookies = backup_data.get('cookies', [])
            exported_at = backup_data.get('exported_at', 'unknown')
            
            log.info(MODULE, "cookie_backup_found", "Found cookie backup",
                     exported_at=exported_at, cookie_count=len(cookies))
            
            if not cookies:
                log.warning(MODULE, "cookie_backup_empty", "Backup file is empty")
                return False
            
            # Check for critical auth cookie
            cookie_names = [c.get('name') for c in cookies]
            if 'auth_token' not in cookie_names:
                log.warning(MODULE, "cookie_missing_auth", "Backup missing auth_token cookie")
                return False
            
            # Setup browser if needed
            if not self.driver:
                if not self._setup_browser(headless=True):
                    return False
            
            # Navigate to Twitter (cookies need matching domain)
            log.info(MODULE, "navigating_xcom", "Navigating to x.com")
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
            
            log.info(MODULE, "cookies_added", "Added cookies", added=added, failed=failed)
            
            # VERIFY login actually works
            log.info(MODULE, "verifying_login", "Verifying login...")
            if self._verify_logged_in():
                self.authenticated = True
                self.last_activity = time.time()
                log.info(MODULE, "cookie_restore_success", "Cookie restore SUCCESSFUL - authenticated")
                return True
            else:
                log.warning(MODULE, "cookie_restore_failed", "Cookies restored but login FAILED - cookies may be expired")
                self.authenticated = False
                return False
                
        except Exception as e:
            import traceback
            log.error(MODULE, "cookie_restore_error", "Cookie restore error", error=str(e), traceback=traceback.format_exc())
            return False
    
    def _backup_cookies_to_host(self) -> bool:
        """Export current session cookies to host-mounted backup file
        
        Returns:
            True if backup successful
        """
        if not self.driver:
            log.warning(MODULE, "no_browser_for_backup", "No browser to backup cookies from")
            return False
        
        try:
            cookies = self.driver.get_cookies()
            
            # Filter to Twitter cookies
            twitter_cookies = [c for c in cookies if 'x.com' in c.get('domain', '')]
            
            if not twitter_cookies:
                log.warning(MODULE, "no_twitter_cookies", "No Twitter cookies to backup")
                return False
            
            # Check for auth_token
            has_auth = any(c['name'] == 'auth_token' for c in twitter_cookies)
            if not has_auth:
                log.warning(MODULE, "no_auth_token_backup", "No auth_token in cookies - not backing up")
                return False
            
            backup_data = {
                'exported_at': time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                'cookies': twitter_cookies
            }
            
            with open(self.cookie_backup_file, 'w') as f:
                json.dump(backup_data, f, indent=2)
            
            log.info(MODULE, "cookies_backed_up", "Backed up cookies", count=len(twitter_cookies), file=self.cookie_backup_file)
            return True
            
        except Exception as e:
            log.error(MODULE, "cookie_backup_failed", "Cookie backup failed", error=str(e))
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
            log.info(MODULE, "firefox_launched", "Manual Firefox launched", pid=proc.pid)
            return True
            
        except Exception as e:
            log.error(MODULE, "firefox_launch_failed", "Failed to launch Firefox", error=str(e))
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
            
            log.info(MODULE, "notification_sent", "Notification sent", email=notify_email)
            return True
            
        except Exception as e:
            log.error(MODULE, "notification_failed", "Notification failed", error=str(e))
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
        
        OPTIMIZATION: If we've searched successfully in the last 60 seconds,
        skip the full verification (which loads x.com/home) and just check
        the browser is alive. This saves ~3-4 seconds per search!
        
        Returns:
            True if authenticated and ready for search
            False if manual login required (caller should handle gracefully)
        """
        # OPTIMIZATION: Skip full verify if we were active recently
        # (less than 60 seconds since last successful activity)
        if self.driver and self.authenticated:
            time_since_activity = time.time() - self.last_activity
            if time_since_activity < 60:
                try:
                    # Quick check - is browser alive? (just check we can get URL)
                    _ = self.driver.current_url
                    # Browser is alive and we were active recently - trust it
                    return True
                except WebDriverException:
                    log.warning(MODULE, "browser_session_died", "Browser session died, will try cookie restore")
                    self.driver = None
                    self.authenticated = False
        
        log.info(MODULE, "ensuring_auth", "Ensuring Twitter authentication")
        
        # Step 1: Check if existing session is valid (full verification)
        if self.driver and self.authenticated:
            log.info(MODULE, "checking_session", "Checking existing session")
            try:
                # Quick check - is browser alive?
                _ = self.driver.current_url
                
                # Full verify - are we actually logged in?
                if self._verify_logged_in():
                    self.last_activity = time.time()
                    log.info(MODULE, "session_valid", "Existing session is valid")
                    return True
                else:
                    log.warning(MODULE, "session_invalid", "Session invalid, will try cookie restore")
                    self.authenticated = False
                    # Close the invalid browser
                    try:
                        self.driver.quit()
                    except:
                        pass
                    self.driver = None
            except WebDriverException:
                log.warning(MODULE, "browser_session_died", "Browser session died, will try cookie restore")
                self.driver = None
                self.authenticated = False
        
        # Step 2: Try to restore from cookie backup
        log.info(MODULE, "attempting_cookie_restore", "Attempting cookie restore")
        if self._restore_cookies_from_backup():
            self._backup_cookies_to_host()  # Re-backup in case format changed
            self._clear_login_notification_flag()
            log.info(MODULE, "auth_cookie_restore", "Authenticated via cookie restore")
            return True
        
        # Step 3: Try checking profile directly (maybe manual login happened)
        log.info(MODULE, "checking_profile", "Checking Firefox profile")
        if self._setup_browser(headless=True) and self._verify_logged_in():
            self.authenticated = True
            self.last_activity = time.time()
            self._backup_cookies_to_host()
            self._clear_login_notification_flag()
            log.info(MODULE, "auth_firefox_profile", "Authenticated via Firefox profile")
            return True
        
        # Step 4: All automatic methods failed - need manual login
        log.error(MODULE, "auth_failed", "All authentication methods failed - manual login required")
        return False
    
    def search_videos(self, search_query: str, exclude_urls: List[str] = None, max_age_minutes: int = 5) -> List[Dict[str, Any]]:
        """Search Twitter for videos matching query
        
        Uses time-based scrolling: scrolls through "Latest" results until finding
        a tweet older than max_age_minutes, then stops. Returns ALL videos found.
        
        Args:
            search_query: Search terms (e.g., "Messi goal Barcelona")
            exclude_urls: List of URLs to skip (already processed videos)
            max_age_minutes: Stop scrolling when tweet is older than this (default: 5)
            
        Returns:
            List of video dictionaries (all videos found, no limit)
            
        Raises:
            TwitterAuthError: If not authenticated and can't auto-restore
        """
        from datetime import datetime, timezone
        
        # Mark as busy for scaler
        self.busy = True
        
        try:
            return self._do_search(search_query, exclude_urls, max_age_minutes)
        finally:
            # Always mark as not busy when done
            self.busy = False

    def _do_search(self, search_query: str, exclude_urls: List[str] = None, max_age_minutes: int = 5) -> List[Dict[str, Any]]:
        """Internal search implementation."""
        from datetime import datetime, timezone
        
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
            log.error(MODULE, "search_auth_failed", error_msg, cookie_backup_exists=os.path.exists(self.cookie_backup_file))
            raise TwitterAuthError(error_msg)
        
        if exclude_urls:
            log.info(MODULE, "search_start", "Searching", query=search_query, exclude_count=len(exclude_urls), max_age_minutes=max_age_minutes)
        else:
            log.info(MODULE, "search_start", "Searching", query=search_query, max_age_minutes=max_age_minutes)
        
        try:
            # Build search URL with video filter - sorted by "Latest" (f=live)
            # No date range filter - we'll filter by checking actual tweet timestamps
            video_search_query = f"{search_query} filter:videos"
            search_url = f"https://twitter.com/search?q={quote(video_search_query)}&src=typed_query&f=live"
            
            log.debug(MODULE, "search_url", "Search URL built", url=search_url)
            
            # Set a reasonable timeout - if page takes >30s, something is WRONG
            self.driver.set_page_load_timeout(30)
            
            try:
                self.driver.get(search_url)
            except Exception as e:
                # Page load timeout or other error
                log.error(MODULE, "page_load_failed", "Page load failed after 30s", error=str(e), current_url=self.driver.current_url)
                
                # Try to get page source to see if we got blocked/challenged
                try:
                    page_source = self.driver.page_source[:500]
                    log.debug(MODULE, "page_preview", "Page preview", content=page_source)
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
            
            discovered_videos = []
            processed_tweet_urls = set()  # Track processed tweets to avoid duplicates when scrolling
            found_old_tweet = False
            scroll_count = 0
            max_scrolls = 10  # Safety limit to prevent infinite scrolling
            
            # Time-based scrolling: scroll until we find a tweet older than max_age_minutes
            while not found_old_tweet and scroll_count < max_scrolls:
                tweet_elements = self.driver.find_elements(By.CSS_SELECTOR, "article[data-testid='tweet']")
                
                if scroll_count == 0:
                    log.info(MODULE, "initial_tweets", "Initial load", tweet_count=len(tweet_elements))
                
                for tweet_element in tweet_elements:
                    try:
                        # Extract tweet URL first to check if already processed
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
                        
                        if not tweet_url:
                            continue
                            
                        # Skip if already processed in this scroll session
                        if tweet_url in processed_tweet_urls:
                            continue
                        processed_tweet_urls.add(tweet_url)
                        
                        # Extract tweet timestamp from <time datetime="..."> element
                        tweet_age_minutes = None
                        try:
                            time_element = tweet_element.find_element(By.CSS_SELECTOR, "time[datetime]")
                            datetime_str = time_element.get_attribute("datetime")
                            if datetime_str:
                                # Parse ISO format like "2026-01-10T13:05:00.000Z"
                                tweet_dt = datetime.fromisoformat(datetime_str.replace('Z', '+00:00'))
                                now = datetime.now(timezone.utc)
                                age = now - tweet_dt
                                tweet_age_minutes = age.total_seconds() / 60
                        except:
                            pass
                        
                        # Check if tweet is too old - if so, stop scrolling
                        if tweet_age_minutes is not None and tweet_age_minutes > max_age_minutes:
                            log.info(MODULE, "old_tweet_found", "Found old tweet, stopping scroll", age_minutes=round(tweet_age_minutes, 1), max_age=max_age_minutes)
                            found_old_tweet = True
                            break
                        
                        # Skip promoted/ad tweets
                        try:
                            promoted_indicators = tweet_element.find_elements(By.XPATH, ".//*[contains(text(), 'Promoted') or contains(text(), 'Ad')]")
                            if promoted_indicators:
                                log.debug(MODULE, "skip_promoted", "Skipping promoted tweet")
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
                                            duration_selectors = [
                                                "[aria-label*='Duration']",
                                                "[data-testid='videoPlayerDuration']",
                                                ".r-1e081e0",
                                                "div[dir='ltr'][style*='color']",
                                            ]
                                            for dur_selector in duration_selectors:
                                                duration_elements = tweet_element.find_elements(By.CSS_SELECTOR, dur_selector)
                                                for dur_elem in duration_elements:
                                                    dur_text = dur_elem.text.strip()
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
                        
                        if has_video:
                            # Skip URLs that were already discovered in previous searches
                            if tweet_url in exclude_set:
                                log.debug(MODULE, "skip_discovered", "Skipping already-discovered URL", url=tweet_url[:60])
                                continue
                            
                            tweet_id = tweet_url.split("/status/")[-1] if "/status/" in tweet_url else f"unknown"
                            age_str = f"{tweet_age_minutes:.1f}min ago" if tweet_age_minutes else "unknown age"
                            
                            # Extract username from tweet URL: https://x.com/USERNAME/status/123
                            username = "Unknown"
                            try:
                                # URL format: https://x.com/CBSSportsGolazo/status/2011864786460409910
                                url_parts = tweet_url.replace("https://", "").replace("http://", "").split("/")
                                if len(url_parts) >= 3 and url_parts[1] != "i":  # Skip /i/ URLs like x.com/i/status/...
                                    username = url_parts[1]
                            except:
                                pass
                            
                            video_entry = {
                                "search_term": search_query,
                                "tweet_url": tweet_url,
                                "tweet_id": tweet_id,
                                "tweet_text": tweet_text[:200],
                                "username": username,
                                "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                                "discovered_at": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                                "search_index": 0,
                                "video_index": len(discovered_videos),
                                "source": "browser_automation",
                                "requires_ytdlp": True,
                                "video_page_url": f"https://x.com/i/status/{tweet_id}",
                                "duration_seconds": video_duration_seconds,
                                "tweet_age_minutes": tweet_age_minutes
                            }
                            
                            discovered_videos.append(video_entry)
                            log.info(MODULE, "video_found", "Video found", video_num=len(discovered_videos), username=username, age=age_str, text=tweet_text[:50])
                    
                    except Exception as e:
                        continue
                
                # If we haven't found an old tweet yet, scroll down for more
                if not found_old_tweet:
                    # If no tweets at all after scrolling, stop (empty results page)
                    if len(tweet_elements) == 0 and scroll_count >= 1:
                        log.info(MODULE, "no_tweets_found", "No tweets found, stopping", scroll_count=scroll_count)
                        break
                    
                    scroll_count += 1
                    self.driver.execute_script("window.scrollBy(0, window.innerHeight);")
                    time.sleep(1.5)  # Wait for lazy-loaded content
                    self.last_activity = time.time()
                    log.debug(MODULE, "scroll_progress", "Scrolling", scroll_num=scroll_count, videos_found=len(discovered_videos))
            
            log.info(MODULE, "search_complete", "Search complete", videos_found=len(discovered_videos))
            
            # Update last_activity after successful search
            self.last_activity = time.time()
            
            # Backup cookies after successful search (keeps them fresh)
            self._backup_cookies_to_host()
            
            return discovered_videos
            
        except TwitterAuthError:
            raise
        except Exception as e:
            import traceback
            log.error(MODULE, "search_error", "Search error", error=str(e), traceback=traceback.format_exc())
            # Re-raise as auth error if it looks like an auth issue
            if "login" in str(e).lower():
                raise TwitterAuthError(f"Search failed due to auth issue: {e}")
            raise
    
    def startup(self) -> bool:
        """Startup routine - try to establish authenticated session
        
        Returns:
            True if authenticated, False if manual login needed
        """
        log.info(MODULE, "startup", "Starting Twitter Session Service")
        log.info(MODULE, "vnc_url", "VNC available", url="http://localhost:4103")
        
        vnc_host = os.environ.get('VNC_PUBLIC_URL', 'http://localhost:4103')
        
        with self._lock:
            self.startup_complete = True
            os.makedirs(self.profile_dir, exist_ok=True)
            
            # Try to authenticate
            if self.ensure_authenticated():
                log.info(MODULE, "service_ready", "Twitter service ready")
                return True
            
            # Need manual login
            log.warning(MODULE, "manual_login_required", "Manual Twitter login required", vnc_url=vnc_host)
            
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
                            log.info(MODULE, "cookie_change_detected", "Cookie change detected, verifying")
                            time.sleep(3)
                            self._kill_manual_firefox()
                            time.sleep(2)
                            
                            if self._setup_browser(headless=True) and self._verify_logged_in():
                                self.authenticated = True
                                self.last_activity = time.time()
                                self._backup_cookies_to_host()
                                self._clear_login_notification_flag()
                                log.info(MODULE, "login_successful", "Login successful")
                                return
                            else:
                                self._launch_manual_firefox()
            
            threading.Thread(target=auto_verify_loop, daemon=True).start()
            return False
    
    def verify_and_switch_to_selenium(self) -> bool:
        """After manual login, verify and switch to Selenium"""
        log.info(MODULE, "verifying_manual_login", "Verifying manual login")
        
        self._kill_manual_firefox()
        time.sleep(2)
        
        if self._setup_browser(headless=True) and self._verify_logged_in():
            self.authenticated = True
            self.last_activity = time.time()
            self._backup_cookies_to_host()
            self._clear_login_notification_flag()
            log.info(MODULE, "login_verified", "Login verified! Ready for scraping")
            return True
        else:
            log.error(MODULE, "login_not_detected", "Login not detected")
            self._launch_manual_firefox()
            return False
    
    def download_video_direct(self, tweet_url: str, output_path: str) -> dict:
        """Download video by navigating to tweet and extracting CDN URL
        
        This bypasses yt-dlp entirely by using the browser to:
        1. Navigate to the tweet
        2. Extract the video source URL from the video element or page source
        3. Download directly from video.twimg.com CDN
        
        Args:
            tweet_url: URL of the tweet containing the video
            output_path: Where to save the downloaded video
            
        Returns:
            Dict with status, path, cdn_url, etc.
        """
        import requests
        import re
        
        if not self.authenticated or not self.driver:
            raise TwitterAuthError("Not authenticated - cannot download video")
        
        log.info(MODULE, "browser_download_start", "Navigating to tweet", url=tweet_url[:60])
        
        try:
            self.driver.set_page_load_timeout(20)
            self.driver.get(tweet_url)
            time.sleep(2)  # Wait for video player to initialize
            self.last_activity = time.time()
            
            # Check for auth redirect
            current_url = self.driver.current_url
            if "login" in current_url or "flow" in current_url:
                self.authenticated = False
                raise TwitterAuthError(f"Got logged out during download! Redirected to: {current_url}")
            
            # Method 1: Extract from video element's src attribute
            video_url = None
            try:
                video_elements = self.driver.find_elements(By.CSS_SELECTOR, "video[src]")
                for video_elem in video_elements:
                    src = video_elem.get_attribute("src")
                    if src and "video.twimg.com" in src:
                        video_url = src
                        log.info(MODULE, "video_src_found", "Found video src", src=src[:80])
                        break
            except:
                pass
            
            # Method 2: Look for blob URLs and try to extract from page source
            if not video_url:
                try:
                    page_source = self.driver.page_source
                    # Look for video.twimg.com URLs in page source
                    pattern = r'https://video\.twimg\.com/[^"\'>\s]+'
                    matches = re.findall(pattern, page_source)
                    if matches:
                        # Prefer .mp4 URLs
                        mp4_urls = [m for m in matches if '.mp4' in m]
                        if mp4_urls:
                            video_url = mp4_urls[0]
                        else:
                            video_url = matches[0]
                        log.info(MODULE, "video_url_page_source", "Found video URL in page source", url=video_url[:80])
                except:
                    pass
            
            # Method 3: Execute JS to get video source from media elements
            if not video_url:
                try:
                    video_url = self.driver.execute_script("""
                        const videos = document.querySelectorAll('video');
                        for (const v of videos) {
                            if (v.src && v.src.includes('video.twimg.com')) return v.src;
                            // Check source elements
                            const sources = v.querySelectorAll('source');
                            for (const s of sources) {
                                if (s.src && s.src.includes('video.twimg.com')) return s.src;
                            }
                        }
                        return null;
                    """)
                    if video_url:
                        log.info(MODULE, "video_url_js", "Found video via JS", url=video_url[:80])
                except:
                    pass
            
            if not video_url:
                log.error(MODULE, "video_extraction_failed", "Could not extract video URL from page")
                return {
                    "status": "error",
                    "error": "Could not extract video URL - video may be in different format or tweet deleted"
                }
            
            # Download from CDN directly
            log.info(MODULE, "cdn_download_start", "Downloading from CDN")
            
            # Get cookies from browser for the download request
            cookies = {}
            for cookie in self.driver.get_cookies():
                cookies[cookie['name']] = cookie['value']
            
            headers = {
                'User-Agent': self.driver.execute_script("return navigator.userAgent"),
                'Referer': 'https://x.com/',
                'Origin': 'https://x.com'
            }
            
            response = requests.get(video_url, cookies=cookies, headers=headers, stream=True, timeout=30)
            
            if response.status_code != 200:
                log.error(MODULE, "cdn_error", "CDN returned error", status_code=response.status_code)
                return {
                    "status": "error",
                    "error": f"CDN returned status {response.status_code}"
                }
            
            # Save video
            with open(output_path, 'wb') as f:
                for chunk in response.iter_content(chunk_size=8192):
                    f.write(chunk)
            
            file_size = os.path.getsize(output_path)
            log.info(MODULE, "download_success", "Downloaded video", size_mb=round(file_size / 1024 / 1024, 2), path=output_path)
            
            return {
                "status": "success",
                "path": output_path,
                "cdn_url": video_url,
                "size_bytes": file_size
            }
            
        except TwitterAuthError:
            raise
        except Exception as e:
            log.error(MODULE, "download_error", "Download error", error=str(e))
            return {
                "status": "error",
                "error": str(e)
            }
    
    def cleanup(self):
        """Cleanup browser session"""
        if self.driver:
            try:
                self.driver.quit()
            except:
                pass
        self.authenticated = False
        self.startup_complete = False
