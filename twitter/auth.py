"""Authentication module for Twitter service

Handles:
- Cookie loading/saving
- Automated login with credentials
- Cookie validation
- Manual login fallback
"""
import os
import time
import pickle
from typing import Optional
from selenium.webdriver.remote.webdriver import WebDriver
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC
from selenium.common.exceptions import NoSuchElementException, TimeoutException

from .config import TwitterConfig
from .twitter_logging import log

MODULE = "twitter_auth"


class TwitterAuthenticator:
    """Handles Twitter authentication via cookies and automated login"""
    
    def __init__(self, config: TwitterConfig):
        self.config = config
        self.cookies_file = config.cookies_file
    
    def load_cookies(self, driver: WebDriver) -> bool:
        """Load cookies from file if available
        
        Args:
            driver: Selenium WebDriver instance
            
        Returns:
            True if cookies loaded successfully, False otherwise
        """
        if not os.path.exists(self.cookies_file):
            log.warning(MODULE, "no_cookies_file", "No cookies file", path=self.cookies_file)
            return False
        
        try:
            with open(self.cookies_file, 'rb') as f:
                cookies = pickle.load(f)
            
            # Navigate to twitter.com first (cookies need matching domain)
            driver.get("https://x.com")
            time.sleep(2)
            
            # Add cookies
            for cookie in cookies:
                try:
                    driver.add_cookie(cookie)
                except Exception:
                    pass  # Some cookies might be invalid
            
            log.info(MODULE, "cookies_loaded", "Loaded cookies", count=len(cookies), file=self.cookies_file)
            return True
            
        except Exception as e:
            log.error(MODULE, "cookies_load_failed", "Failed to load cookies", error=str(e))
            return False
    
    def save_cookies(self, driver: WebDriver) -> bool:
        """Save current cookies to file
        
        Args:
            driver: Selenium WebDriver instance
            
        Returns:
            True if cookies saved successfully, False otherwise
        """
        try:
            cookies = driver.get_cookies()
            
            # Ensure directory exists
            os.makedirs(os.path.dirname(self.cookies_file), exist_ok=True)
            
            with open(self.cookies_file, 'wb') as f:
                pickle.dump(cookies, f)
            
            log.info(MODULE, "cookies_saved", "Saved cookies", count=len(cookies), file=self.cookies_file)
            return True
            
        except Exception as e:
            log.error(MODULE, "cookies_save_failed", "Failed to save cookies", error=str(e))
            return False
    
    def verify_cookies(self, driver: WebDriver) -> bool:
        """Verify that loaded cookies are still valid
        
        Args:
            driver: Selenium WebDriver instance with cookies loaded
            
        Returns:
            True if cookies are valid (user is logged in), False otherwise
        """
        try:
            # Navigate to home page
            driver.get("https://x.com/home")
            time.sleep(5)  # Give it time to load
            
            current_url = driver.current_url
            log.info(MODULE, "cookies_url_check", "After loading cookies", url=current_url)
            
            # Check if we're on home page (logged in)
            if "home" in current_url and "login" not in current_url and "flow" not in current_url:
                log.info(MODULE, "cookies_valid", "Cookies are valid - user is logged in")
                return True
            else:
                log.warning(MODULE, "cookies_invalid", "Cookies invalid - redirected", redirect_url=current_url)
                return False
                
        except Exception as e:
            log.error(MODULE, "cookies_verify_failed", "Cookie verification failed", error=str(e))
            return False
    
    def automated_login(self, driver: WebDriver) -> bool:
        """Attempt automated login using credentials from config
        
        Args:
            driver: Selenium WebDriver instance
            
        Returns:
            True if login successful, False otherwise
        """
        try:
            log.info(MODULE, "automated_login_start", "Attempting automated login with human-like behavior")
            
            # Navigate to login page
            driver.get("https://twitter.com/login")
            time.sleep(5)  # Give page time to fully load
            
            # Enter username with human-like typing
            try:
                username_input = WebDriverWait(driver, 15).until(
                    EC.presence_of_element_located((By.NAME, "text"))
                )
                
                # Clear field slowly
                username_input.click()
                time.sleep(0.5)
                username_input.clear()
                time.sleep(0.3)
                
                # Type username character by character with random delays
                for char in self.config.username:
                    username_input.send_keys(char)
                    time.sleep(0.1 + (0.1 * (hash(char) % 10) / 10))  # 0.1-0.2s per char
                
                time.sleep(1)  # Pause before clicking
                
                next_button = driver.find_element(By.XPATH, "//span[text()='Next']/..")
                next_button.click()
                time.sleep(5)  # Wait longer for page transition
            except TimeoutException:
                log.error(MODULE, "username_input_not_found", "Username input not found")
                return False
            
            # Handle email verification if prompted
            try:
                email_input = driver.find_element(By.NAME, "text")
                log.info(MODULE, "email_verification_required", "Email verification required")
                email_input.clear()
                email_input.send_keys(self.config.email)
                
                next_button = driver.find_element(By.XPATH, "//span[text()='Next']/..")
                next_button.click()
                time.sleep(3)
            except NoSuchElementException:
                log.info(MODULE, "no_email_verification", "No email verification needed")
            
            # Enter password with human-like typing
            try:
                password_input = WebDriverWait(driver, 15).until(
                    EC.presence_of_element_located((By.NAME, "password"))
                )
                
                password_input.click()
                time.sleep(0.5)
                password_input.clear()
                time.sleep(0.3)
                
                # Type password character by character with random delays
                for char in self.config.password:
                    password_input.send_keys(char)
                    time.sleep(0.1 + (0.1 * (hash(char) % 10) / 10))
                
                time.sleep(1)
                
                login_button = driver.find_element(By.XPATH, "//span[text()='Log in']/..")
                login_button.click()
                time.sleep(8)  # Wait longer for login to complete
            except TimeoutException:
                log.error(MODULE, "password_input_not_found", "Password input not found")
                return False
            
            # Verify success
            current_url = driver.current_url
            if "home" in current_url:
                log.info(MODULE, "automated_login_success", "Automated login successful")
                # Save cookies for future use
                self.save_cookies(driver)
                return True
            else:
                log.error(MODULE, "login_failed", "Login failed - redirected", redirect_url=current_url)
                return False
                
        except Exception as e:
            import traceback
            log.error(MODULE, "automated_login_failed", "Automated login failed", error=str(e), traceback=traceback.format_exc())
            return False
    
    def interactive_login(self, driver: WebDriver, timeout: int = 300) -> bool:
        """Open browser and wait for user to manually login
        
        This is a fallback when automated login fails.
        
        Args:
            driver: Selenium WebDriver instance (must be non-headless)
            timeout: Maximum seconds to wait for login (default: 5 minutes)
            
        Returns:
            True if login detected, False if timeout
        """
        try:
            log.info(MODULE, "interactive_login_start", "Opening Twitter login page for manual authentication")
            driver.get("https://twitter.com/login")
            
            log.info(MODULE, "waiting_manual_login", "Waiting for manual login - complete login in browser window")
            
            start_time = time.time()
            
            while time.time() - start_time < timeout:
                try:
                    current_url = driver.current_url
                    
                    # Check if we're on home page (successful login)
                    if "home" in current_url:
                        log.info(MODULE, "login_detected", "Login detected! Saving cookies")
                        
                        # Save cookies for next time
                        if self.save_cookies(driver):
                            log.info(MODULE, "cookies_saved_auto", "Cookies saved - future startups will be automatic")
                        
                        return True
                    
                    time.sleep(5)
                    
                except Exception as e:
                    log.warning(MODULE, "login_status_check_error", "Error checking login status", error=str(e))
                    time.sleep(5)
            
            log.error(MODULE, "login_timeout", "Login timeout - no login detected", timeout_seconds=timeout)
            return False
            
        except Exception as e:
            import traceback
            log.error(MODULE, "interactive_login_failed", "Interactive login failed", error=str(e), traceback=traceback.format_exc())
            return False
    
    def authenticate(self, driver: WebDriver, force_reauth: bool = False, 
                    allow_interactive: bool = True) -> bool:
        """Main authentication flow - tries cookies, then automated, then interactive
        
        Args:
            driver: Selenium WebDriver instance
            force_reauth: Skip cookie loading and force fresh login
            allow_interactive: Allow fallback to interactive manual login (requires GUI)
            
        Returns:
            True if authenticated successfully, False otherwise
        """
        # Try loading cookies first (unless forced)
        if not force_reauth:
            if self.load_cookies(driver):
                if self.verify_cookies(driver):
                    return True
                else:
                    log.warning(MODULE, "cookies_expired", "Loaded cookies are invalid/expired")
        
        # Try automated login with credentials
        log.info(MODULE, "trying_automated_login", "Attempting automated login with credentials")
        if self.automated_login(driver):
            return True
        
        # Fallback to interactive manual login (if allowed)
        if allow_interactive and not self.config.headless:
            log.warning(MODULE, "fallback_interactive", "Automated login failed, falling back to interactive login")
            return self.interactive_login(driver)
        
        # All authentication methods failed
        log.error(MODULE, "all_auth_failed", "All authentication methods failed - manual login required via VNC")
        return False
