"""STANDALONE Twitter Session Service - WITH STARTUP AUTHENTICATION"""
import time
import threading
import os
import random
import sys
from fastapi import FastAPI, HTTPException
from fastapi.responses import JSONResponse
from pydantic import BaseModel
from typing import List, Dict, Any
from selenium import webdriver
from selenium.webdriver.chrome.options import Options
from selenium.webdriver.chrome.service import Service
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC
from selenium.common.exceptions import TimeoutException, NoSuchElementException, WebDriverException
from contextlib import asynccontextmanager

# 🔥 FORCE UNBUFFERED OUTPUT - Fix log dumping issue
sys.stdout.reconfigure(line_buffering=True)
os.environ['PYTHONUNBUFFERED'] = '1'

# Add this right after imports, before the class definition
def flush_print(message):
    """Print with immediate flush"""
    print(message)
    sys.stdout.flush()

class TwitterSessionManager:
    """STANDALONE Twitter session with startup authentication"""
    
    _instance = None
    _lock = threading.Lock()
    
    def __new__(cls):
        if cls._instance is None:
            with cls._lock:
                if cls._instance is None:
                    cls._instance = super().__new__(cls)
                    cls._instance._initialized = False
        return cls._instance
    
    def __init__(self):
        if self._initialized:
            return
            
        self.driver = None
        self.authenticated = False
        self.last_activity = 0
        self.session_timeout = int(os.getenv('SESSION_TIMEOUT', 3600))
        self.startup_complete = False
        self._initialized = True
        
        print("🔧 TwitterSessionManager initialized (STANDALONE - WITH STARTUP AUTH)")
    
    def _setup_browser(self):
        """Setup browser with fallback from non-headless to headless"""
        
        # 🔥 TRY NON-HEADLESS FIRST (Better for bot detection)
        if self._setup_nonheadless_browser():
            return True
        
        # 🔥 FALLBACK TO HEADLESS IF NON-HEADLESS FAILS
        flush_print("🔧 Non-headless failed, falling back to headless mode...")
        return self._setup_headless_browser()
    
    def _setup_nonheadless_browser(self):
        """Try to setup non-headless browser"""
        try:
            # 🔥 KILL ANY EXISTING CHROME PROCESSES FIRST
            import subprocess
            try:
                subprocess.run(["pkill", "-f", "chrome"], check=False, capture_output=True)
                subprocess.run(["pkill", "-f", "chromedriver"], check=False, capture_output=True)
                time.sleep(3)  # Wait longer for processes to die
                flush_print("🔧 Cleaned up existing Chrome processes")
            except:
                pass
            
            chrome_options = Options()
            
            # 🔥 DON'T USE CUSTOM USER DATA DIR - Let Chrome use default
            # This was causing the "already in use" error
            
            # 🔥 NON-HEADLESS SETTINGS
            chrome_options.add_argument("--incognito")
            chrome_options.add_argument("--no-sandbox")
            chrome_options.add_argument("--disable-dev-shm-usage")
            chrome_options.add_argument("--disable-gpu")
            chrome_options.add_argument("--window-size=1920,1080")
            
            # 🔥 STEALTH SETTINGS
            chrome_options.add_argument("--disable-blink-features=AutomationControlled")
            chrome_options.add_experimental_option("excludeSwitches", ["enable-automation"])
            chrome_options.add_experimental_option('useAutomationExtension', False)
            
            # 🔥 REALISTIC USER AGENT
            chrome_options.add_argument("--user-agent=Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
            chrome_options.add_argument("--accept-language=en-US,en;q=0.9")
            
            # 🔥 ADDITIONAL STEALTH
            chrome_options.add_argument("--disable-web-security")
            chrome_options.add_argument("--disable-features=VizDisplayCompositor")
            chrome_options.add_argument("--no-first-run")
            chrome_options.add_argument("--no-default-browser-check")
            
            service = Service("/usr/bin/chromedriver")
            self.driver = webdriver.Chrome(service=service, options=chrome_options)
            
            # 🔥 STEALTH JAVASCRIPT
            stealth_js = """
                Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
                window.chrome = { runtime: {} };
                Object.defineProperty(navigator, 'plugins', {get: () => [1, 2, 3, 4, 5]});
                Object.defineProperty(navigator, 'languages', {get: () => ['en-US', 'en']});
                delete navigator.__proto__.webdriver;
            """
            
            try:
                self.driver.execute_script(stealth_js)
            except:
                pass
            
            self.driver.set_page_load_timeout(30)
            self.driver.implicitly_wait(5)
            
            flush_print("✅ NON-HEADLESS browser session created (no custom profile)")
            return True
            
        except Exception as e:
            flush_print(f"🔧 Non-headless setup failed: {e}")
            return False
    
    def _setup_headless_browser(self):
        """Fallback headless browser setup"""
        try:
            chrome_options = Options()
            
            # 🔥 HEADLESS MODE (FALLBACK)
            chrome_options.add_argument("--headless=new")
            
            # 🔥 DON'T USE CUSTOM PROFILE IN HEADLESS EITHER
            # Let Chrome handle its own profile management
            
            # 🔥 BASIC DOCKER COMPATIBILITY
            chrome_options.add_argument("--no-sandbox")
            chrome_options.add_argument("--disable-dev-shm-usage")
            chrome_options.add_argument("--disable-gpu")
            chrome_options.add_argument("--window-size=1920,1080")
            chrome_options.add_argument("--incognito")  # Add incognito to headless too
            
            # 🔥 AGGRESSIVE STEALTH SETTINGS
            chrome_options.add_argument("--disable-blink-features=AutomationControlled")
            chrome_options.add_experimental_option("excludeSwitches", ["enable-automation"])
            chrome_options.add_experimental_option('useAutomationExtension', False)
            
            # 🔥 HIDE AUTOMATION TRACES
            chrome_options.add_argument("--disable-web-security")
            chrome_options.add_argument("--disable-features=VizDisplayCompositor")
            chrome_options.add_argument("--disable-background-timer-throttling")
            chrome_options.add_argument("--disable-backgrounding-occluded-windows")
            chrome_options.add_argument("--disable-renderer-backgrounding")
            chrome_options.add_argument("--disable-field-trial-config")
            chrome_options.add_argument("--disable-back-forward-cache")
            chrome_options.add_argument("--disable-ipc-flooding-protection")
            
            # 🔥 MIMIC REAL BROWSER
            chrome_options.add_argument("--user-agent=Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
            chrome_options.add_argument("--accept-language=en-US,en;q=0.9")
            
            # 🔥 ADDITIONAL ANTI-DETECTION
            chrome_options.add_argument("--no-first-run")
            chrome_options.add_argument("--no-default-browser-check")
            chrome_options.add_argument("--disable-default-apps")
            chrome_options.add_argument("--disable-popup-blocking")
            chrome_options.add_argument("--disable-translate")
            chrome_options.add_argument("--disable-background-networking")
            chrome_options.add_argument("--disable-background-mode")
            chrome_options.add_argument("--disable-client-side-phishing-detection")
            chrome_options.add_argument("--disable-sync")
            chrome_options.add_argument("--hide-scrollbars")
            chrome_options.add_argument("--mute-audio")
            
            service = Service("/usr/bin/chromedriver")
            self.driver = webdriver.Chrome(service=service, options=chrome_options)
            
            # 🔥 ADVANCED STEALTH JAVASCRIPT
            stealth_js = """
                Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
                window.chrome = { runtime: {} };
                Object.defineProperty(navigator, 'plugins', {get: () => [1, 2, 3, 4, 5]});
                Object.defineProperty(navigator, 'languages', {get: () => ['en-US', 'en']});
                Object.defineProperty(navigator, 'permissions', {
                    get: () => ({
                        query: () => Promise.resolve({state: 'granted'})
                    })
                });
                delete navigator.__proto__.webdriver;
                Object.defineProperty(screen, 'width', {get: () => 1920});
                Object.defineProperty(screen, 'height', {get: () => 1080});
                Object.defineProperty(screen, 'availWidth', {get: () => 1920});
                Object.defineProperty(screen, 'availHeight', {get: () => 1040});
                try {
                    Intl.DateTimeFormat().resolvedOptions().timeZone = 'America/New_York';
                } catch(e) {}
            """
            
            try:
                self.driver.execute_script(stealth_js)
                flush_print("✅ Advanced stealth JavaScript executed")
            except Exception as js_e:
                flush_print(f"🔧 Stealth JS warning: {js_e}")
                pass
            
            self.driver.set_page_load_timeout(30)
            self.driver.implicitly_wait(5)
            
            flush_print("✅ AGGRESSIVE STEALTH browser session created (fallback, no custom profile)")
            return True
            
        except Exception as e:
            flush_print(f"❌ Headless setup also failed: {e}")
            return False
    
    def _safe_driver_action(self, action_func, max_attempts=3):
        """Execute driver action with crash recovery"""
        for attempt in range(max_attempts):
            try:
                if not self.driver:
                    print(f"🔧 Creating browser for attempt {attempt + 1}")
                    if not self._setup_browser():
                        continue
                
                return action_func()
                
            except (WebDriverException, ConnectionError) as e:
                print(f"❌ Driver action failed (attempt {attempt + 1}): {e}")
                
                # Clean up broken driver
                if self.driver:
                    try:
                        self.driver.quit()
                    except:
                        pass
                    self.driver = None
                
                if attempt < max_attempts - 1:
                    time.sleep(5)
                    continue
                else:
                    print("❌ All driver action attempts failed")
                    return None
    
    def authenticate(self, force_reauth=False):
        """Authenticate with crash recovery and detailed logging"""
        if self.authenticated and not force_reauth and self._session_valid():
            flush_print("✅ Already authenticated")
            return True
        
        flush_print(f"🔐 Authentication attempt 1/1")
        
        def auth_action():
            flush_print("🔐 Quick authentication check...")
            
            # 🔥 ALWAYS GO TO HOME - Let Twitter handle redirects
            self.driver.get("https://x.com/home")
            time.sleep(5)
            
            current_url = self.driver.current_url
            flush_print(f"✅ Current URL: {current_url}")
            
            # 🔥 CHECK IF WE'RE ALREADY AUTHENTICATED
            if ("x.com/home" in current_url or "twitter.com/home" in current_url) and "login" not in current_url and "flow" not in current_url:
                flush_print("✅ Already authenticated via session")
                self.authenticated = True
                self.last_activity = time.time()
                return True
            
            # If redirected to login flow, proceed with login
            if "login" in current_url or "flow" in current_url:
                flush_print("🔧 Redirected to login - attempting authentication...")
                return self._perform_login_xcom()
            else:
                flush_print("🔧 Unexpected page - attempting authentication anyway...")
                return self._perform_login_xcom()
        
        result = self._safe_driver_action(auth_action)
        return result if result is not None else False
    
    def _perform_login_xcom(self):
        """Simple login matching manual incognito experience"""
        try:
            twitter_username = os.getenv('TWITTER_USERNAME', 'REDACTED_USERNAME')
            twitter_password = os.getenv('TWITTER_PASSWORD', 'REDACTED_PASSWORD')
            
            from selenium.webdriver.common.keys import Keys
            
            flush_print("🔧 Starting simple login (matching incognito experience)...")
            
            # 🔥 STEP 1: Navigate to login page directly
            flush_print("🔧 Step 1: Navigate to login page...")
            self.driver.get("https://x.com/i/flow/login")
            time.sleep(5)
            
            # 🔥 STEP 2: Enter username
            flush_print("🔧 Step 2: Enter username...")
            
            try:
                username_input = WebDriverWait(self.driver, 10).until(
                    EC.presence_of_element_located((By.NAME, "text"))
                )
                username_input.clear()
                username_input.send_keys(twitter_username)
                time.sleep(1)
                flush_print("✅ Username entered")
            except Exception as e:
                flush_print(f"❌ Username entry failed: {e}")
                return False
            
            # 🔥 STEP 3: VERIFY USERNAME AND RETRY IF NEEDED
            flush_print("🔧 Step 3: Verify username was accepted...")
            
            try:
                # Check if username is still in the field
                current_value = username_input.get_attribute("value")
                flush_print(f"🔧 Username field current value: '{current_value}'")
                
                if not current_value or current_value != twitter_username:
                    flush_print("🔧 Username not in field - re-entering...")
                    username_input.clear()
                    time.sleep(1)
                    username_input.send_keys(twitter_username)
                    time.sleep(2)
                    
                    # Verify again
                    current_value = username_input.get_attribute("value")
                    flush_print(f"🔧 Username field after retry: '{current_value}'")
                
                # Now press Enter
                flush_print("🔧 Pressing Enter on username field...")
                username_input.send_keys(Keys.RETURN)
                flush_print("✅ Enter key pressed on username field")
                
                # 🔥 WAIT FOR PASSWORD FIELD TO APPEAR
                flush_print("🔧 Waiting for password field...")
                password_input = WebDriverWait(self.driver, 15).until(
                    EC.presence_of_element_located((By.NAME, "password"))
                )
                flush_print("✅ Password field found!")
                
            except TimeoutException:
                flush_print("❌ Password field never appeared after Enter - trying Next button as backup...")
                
                # 🔥 BEFORE CLICKING NEXT - CHECK IF USERNAME IS STILL THERE
                try:
                    current_username_value = username_input.get_attribute("value")
                    flush_print(f"🔧 Username before Next click: '{current_username_value}'")
                    
                    if not current_username_value or current_username_value != twitter_username:
                        flush_print("🔧 Username missing before Next click - re-entering...")
                        username_input.clear()
                        time.sleep(1)
                        username_input.send_keys(twitter_username)
                        time.sleep(2)
                        flush_print("🔧 Username re-entered before Next click")
                    
                    # Now click Next
                    next_button = self.driver.find_element(By.XPATH, "//span[text()='Next']/..")
                    next_button.click()
                    flush_print("✅ Next button clicked (fallback)")
                    
                    # 🔥 WAIT LONGER - Maybe Twitter needs more time
                    flush_print("🔧 Waiting longer for password field after Next...")
                    password_input = WebDriverWait(self.driver, 20).until(
                        EC.presence_of_element_located((By.NAME, "password"))
                    )
                    flush_print("✅ Password field found after Next button!")
                    
                except TimeoutException:
                    flush_print("❌ Password field still never appeared - final analysis...")
                    
                    # 🔥 COMPREHENSIVE DEBUG - Let's see what's really happening
                    try:
                        current_url = self.driver.current_url
                        page_source = self.driver.page_source
                        
                        # Save full page for analysis
                        with open("/tmp/no_password_debug.html", "w") as f:
                            f.write(page_source)
                        self.driver.save_screenshot("/tmp/no_password_debug.png")
                        
                        flush_print(f"🔧 Current URL: {current_url}")
                        flush_print("🔧 Page debug saved to /tmp/no_password_debug.*")
                        
                        # Check ALL input fields and their values
                        all_inputs = self.driver.find_elements(By.CSS_SELECTOR, "input")
                        flush_print(f"🔧 Found {len(all_inputs)} input fields:")
                        for i, inp in enumerate(all_inputs):
                            try:
                                name = inp.get_attribute("name") or "no-name"
                                inp_type = inp.get_attribute("type") or "text"
                                placeholder = inp.get_attribute("placeholder") or ""
                                value = inp.get_attribute("value") or ""
                                is_displayed = inp.is_displayed()
                                is_enabled = inp.is_enabled()
                                flush_print(f"🔧   Input {i}: name='{name}', type='{inp_type}', value='{value}', visible={is_displayed}, enabled={is_enabled}")
                            except:
                                pass
                        
                        # Check if there are any error messages
                        error_selectors = [
                            "[data-testid='error']",
                            ".error",
                            "[role='alert']",
                            ".alert"
                        ]
                        
                        for selector in error_selectors:
                            try:
                                errors = self.driver.find_elements(By.CSS_SELECTOR, selector)
                                for error in errors:
                                    if error.is_displayed():
                                        flush_print(f"🔧 ERROR MESSAGE: {error.text}")
                            except:
                                pass
                        
                        # Show first 1000 chars of page content to look for clues
                        flush_print("🔧 Page content (first 1000 chars):")
                        flush_print("---PAGE-START---")
                        flush_print(page_source[:1000])
                        flush_print("---PAGE-END---")
                        
                    except Exception as debug_e:
                        flush_print(f"🔧 Debug failed: {debug_e}")
                    
                    flush_print("❌ Login failed - Twitter is not progressing past username")
                    return False
                    
                except Exception as e:
                    flush_print(f"❌ Next button fallback failed: {e}")
                    return False
            
            # 🔥 STEP 4: Enter password
            flush_print("🔧 Step 4: Enter password...")
            
            try:
                password_input.clear()
                password_input.send_keys(twitter_password)
                time.sleep(1)
                flush_print("✅ Password entered")
            except Exception as e:
                flush_print(f"❌ Password entry failed: {e}")
                return False
            
            # 🔥 STEP 5: Submit login
            flush_print("🔧 Step 5: Submit login...")
            
            try:
                password_input.send_keys(Keys.RETURN)
                time.sleep(5)
                flush_print("✅ Login submitted")
            except Exception as e:
                flush_print(f"❌ Login submission failed: {e}")
                return False
            
            # 🔥 STEP 6: Verify success
            flush_print("🔧 Step 6: Verify login success...")
            
            try:
                # Wait for redirect to home
                time.sleep(5)
                current_url = self.driver.current_url
                flush_print(f"🔧 Final URL: {current_url}")
                
                if ("x.com/home" in current_url or "twitter.com/home" in current_url):
                    flush_print("✅ Login successful!")
                    self.authenticated = True
                    self.last_activity = time.time()
                    return True
                else:
                    # Try navigating to home directly
                    flush_print("🔧 Trying direct navigation to home...")
                    self.driver.get("https://x.com/home")
                    time.sleep(3)
                    
                    final_url = self.driver.current_url
                    flush_print(f"🔧 Direct navigation result: {final_url}")
                    
                    if ("x.com/home" in final_url or "twitter.com/home" in final_url):
                        flush_print("✅ Login successful via direct navigation!")
                        self.authenticated = True
                        self.last_activity = time.time()
                        return True
                    else:
                        flush_print("❌ Login failed - could not reach home page")
                        return False
                        
            except Exception as e:
                flush_print(f"❌ Login verification failed: {e}")
                return False
            
        except Exception as e:
            flush_print(f"❌ Login process failed: {e}")
            return False

    def _session_valid(self):
        """Check session validity with crash protection"""
        if not self.driver or not self.authenticated:
            return False
        
        if time.time() - self.last_activity > self.session_timeout:
            return False
        
        try:
            self.driver.current_url
            return True
        except WebDriverException:
            self.authenticated = False
            return False
        
    def _handle_phone_verification(self):
        """Handle phone verification request"""
        flush_print("❌ Twitter requires phone verification")
        flush_print("🔧 This account needs phone number verification setup")
        
        # Try to find skip button
        try:
            skip_selectors = [
                "//span[contains(text(), 'Skip')]/..",
                "//span[contains(text(), 'Not now')]/..",
                "//a[contains(text(), 'Skip')]",
                "//button[contains(text(), 'Skip')]",
                "//div[@role='button' and contains(., 'Skip')]"
            ]
            
            for selector in skip_selectors:
                try:
                    if selector.startswith("//"):
                        skip_btn = self.driver.find_element(By.XPATH, selector)
                    else:
                        skip_btn = self.driver.find_element(By.CSS_SELECTOR, selector)
                        
                    if skip_btn.is_displayed() and skip_btn.is_enabled():
                        flush_print(f"🔧 Found skip button: '{skip_btn.text[:30]}' - attempting to skip phone verification")
                        skip_btn.click()
                        time.sleep(3)
                        
                        # Check if skip worked
                        current_url = self.driver.current_url
                        if "phone" not in self.driver.page_source.lower():
                            flush_print("✅ Phone verification skipped successfully!")
                            # Try to find password field now
                            return self._try_password_after_skip()
                        else:
                            flush_print("🔧 Skip didn't work, trying next method...")
                            
                except:
                    continue
            
            flush_print("❌ No way to skip phone verification found")
            
            # Try entering a phone number if available
            twitter_phone = os.getenv('TWITTER_PHONE')
            if twitter_phone:
                flush_print(f"🔧 Trying to enter phone number: {twitter_phone[:3]}***")
                return self._try_phone_entry(twitter_phone)
            else:
                flush_print("❌ No phone number provided in environment variables")
                return False
            
        except Exception as e:
            flush_print(f"❌ Phone verification handling failed: {e}")
            return False
    
    def _handle_email_verification(self):
        """Handle email verification request"""
        flush_print("🔧 Twitter is asking for email verification")
        
        try:
            twitter_email = os.getenv('TWITTER_EMAIL', 'REDACTED_EMAIL')
            flush_print(f"🔧 Trying email: {twitter_email}")
            
            # Look for email input
            email_input = None
            try:
                email_input = WebDriverWait(self.driver, 5).until(
                    EC.presence_of_element_located((By.NAME, "text"))
                )
            except:
                flush_print("❌ Email input not found")
                return False
            
            # Enter email
            email_input.clear()
            email_input.send_keys(twitter_email)
            time.sleep(1)
            
            # Click Next
            try:
                next_btn = self.driver.find_element(By.XPATH, "//span[text()='Next']/..")
                next_btn.click()
                time.sleep(3)
                flush_print("✅ Email verification submitted")
                
                # Now try to find password field
                return self._try_password_after_skip()
                    
            except Exception as e:
                flush_print(f"❌ Email verification Next button failed: {e}")
                return False
                
        except Exception as e:
            flush_print(f"❌ Email verification failed: {e}")
            return False
    
    def _handle_suspicious_activity(self):
        """Handle suspicious activity detection"""
        flush_print("❌ Twitter detected suspicious activity")
        flush_print("🔧 This account may be flagged for automation")
        
        # Save debug info
        try:
            self.driver.save_screenshot("/tmp/suspicious_activity.png")
            with open("/tmp/suspicious_activity.html", "w") as f:
                f.write(self.driver.page_source)
            flush_print("🔧 Debug files saved: /tmp/suspicious_activity.*")
        except:
            pass
        
        return False
    
    def _handle_captcha(self):
        """Handle CAPTCHA challenge"""
        flush_print("❌ Twitter is showing CAPTCHA challenge")
        flush_print("🔧 CAPTCHA solving not implemented")
        
        # Save debug info
        try:
            self.driver.save_screenshot("/tmp/captcha_challenge.png")
            with open("/tmp/captcha_challenge.html", "w") as f:
                f.write(self.driver.page_source)
            flush_print("🔧 Debug files saved: /tmp/captcha_challenge.*")
        except:
            pass
        
        return False
    
    def _analyze_unknown_response(self):
        """Analyze unknown Twitter response"""
        flush_print("🔧 Analyzing unknown Twitter response...")
        
        try:
            page_source = self.driver.page_source
            
            # Save debug info
            with open("/tmp/unknown_response.html", "w") as f:
                f.write(page_source)
            self.driver.save_screenshot("/tmp/unknown_response.png")
            
            flush_print("🔧 Debug files saved: /tmp/unknown_response.*")
            
            # Show first 500 chars of page
            flush_print(f"🔧 Page content preview: {page_source[:500]}...")
            
            # Show all buttons and inputs
            all_inputs = self.driver.find_elements(By.CSS_SELECTOR, "input")
            all_buttons = self.driver.find_elements(By.CSS_SELECTOR, "button, [role='button']")
            
            flush_print(f"🔧 Found {len(all_inputs)} inputs and {len(all_buttons)} buttons")
            
            for i, inp in enumerate(all_inputs[:3]):
                try:
                    flush_print(f"🔧   Input {i}: name='{inp.get_attribute('name')}', type='{inp.get_attribute('type')}', placeholder='{inp.get_attribute('placeholder')}'")
                except:
                    pass
            
            for i, btn in enumerate(all_buttons[:3]):
                try:
                    flush_print(f"🔧   Button {i}: text='{btn.text[:30]}'")
                except:
                    pass
            
            return False
            
        except Exception as e:
            flush_print(f"❌ Unknown response analysis failed: {e}")
            return False
    
    def _find_any_new_input_field(self, original_username_input):
        """Find any new input field that appeared"""
        try:
            all_inputs = self.driver.find_elements(By.CSS_SELECTOR, "input")
            
            for inp in all_inputs:
                if inp != original_username_input and inp.is_displayed() and inp.is_enabled():
                    return inp
            
            return None
        except:
            return None
    
    def _try_password_after_skip(self):
        """Try to find password field after skipping verification"""
        flush_print("🔧 Looking for password field after skip...")
        
        # Wait a bit for page to load
        time.sleep(3)
        
        # Try to find password field
        password_selectors = [
            (By.NAME, "password"),
            (By.CSS_SELECTOR, "input[autocomplete='current-password']"),
            (By.CSS_SELECTOR, "input[type='password']")
        ]
        
        password_input = None
        for by_method, selector in password_selectors:
            try:
                password_input = self.driver.find_element(by_method, selector)
                if password_input.is_displayed() and password_input.is_enabled():
                    flush_print(f"✅ Found password field after skip using {selector}")
                    break
            except:
                continue
        
        if password_input:
            return self._complete_password_login(password_input)
        else:
            flush_print("❌ No password field found after skip")
            return False
    
    def _try_phone_entry(self, phone_number):
        """Try to enter phone number"""
        flush_print(f"🔧 Attempting to enter phone number...")
        
        try:
            # Look for phone input field
            phone_input = None
            phone_selectors = [
                (By.NAME, "text"),
                (By.CSS_SELECTOR, "input[type='tel']"),
                (By.CSS_SELECTOR, "input[autocomplete='tel']")
            ]
            
            for by_method, selector in phone_selectors:
                try:
                    phone_input = self.driver.find_element(by_method, selector)
                    if phone_input.is_displayed() and phone_input.is_enabled():
                        break
                except:
                    continue
            
            if not phone_input:
                flush_print("❌ Phone input field not found")
                return False
            
            # Enter phone number
            phone_input.clear()
            phone_input.send_keys(phone_number)
            time.sleep(1)
            
            # Click Next
            try:
                next_btn = self.driver.find_element(By.XPATH, "//span[text()='Next']/..")
                next_btn.click()
                time.sleep(3)
                flush_print("✅ Phone number submitted")
                
                # This would require SMS verification, which we can't handle
                flush_print("❌ Phone verification requires SMS code - cannot proceed automatically")
                return False
                
            except Exception as e:
                flush_print(f"❌ Phone submission failed: {e}")
                return False
                
        except Exception as e:
            flush_print(f"❌ Phone entry failed: {e}")
            return False
    
    def _complete_password_login(self, password_input):
        """Complete the password login process"""
        try:
            twitter_password = os.getenv('TWITTER_PASSWORD', 'REDACTED_PASSWORD')
            from selenium.webdriver.common.keys import Keys
            
            flush_print("🔧 Entering password...")
            password_input.clear()
            password_input.send_keys(twitter_password)
            time.sleep(1)
            
            flush_print("🔧 Submitting password...")
            password_input.send_keys(Keys.RETURN)
            time.sleep(5)
            
            # Check if login successful
            current_url = self.driver.current_url
            if ("x.com/home" in current_url or "twitter.com/home" in current_url) and "login" not in current_url:
                flush_print("✅ Login successful!")
                self.authenticated = True
                self.last_activity = time.time()
                return True
            else:
                flush_print(f"❌ Login failed - final URL: {current_url}")
                return False
                
        except Exception as e:
            flush_print(f"❌ Password completion failed: {e}")
            return False
    
    def _comprehensive_debug_and_fail(self):
        """Comprehensive debug before failing"""
        try:
            flush_print("🔧 COMPREHENSIVE DEBUG - Final analysis")
            
            current_url = self.driver.current_url
            page_title = self.driver.title
            
            flush_print(f"🔧 Final URL: {current_url}")
            flush_print(f"🔧 Page title: {page_title}")
            
            # Save debug files
            with open("/tmp/comprehensive_debug.html", "w") as f:
                f.write(self.driver.page_source)
            self.driver.save_screenshot("/tmp/comprehensive_debug.png")
            
            flush_print("🔧 Debug files saved: /tmp/comprehensive_debug.*")
            
            return False
            
        except Exception as e:
            flush_print(f"❌ Comprehensive debug failed: {e}")
            return False
    
    def search_videos(self, search_query, max_results=3):
        """Search for videos with crash recovery"""
        if not self.authenticate():
            return []
        
        def search_action():
            from urllib.parse import quote
            
            search_url = f"https://x.com/search?q={quote(search_query + ' filter:videos')}&src=typed_query&f=live"
            print(f"🔍 Searching: {search_url}")
            
            self.driver.get(search_url)
            time.sleep(10)
            
            # Check if logged out
            if "login" in self.driver.current_url:
                print("🔧 Got logged out, re-authenticating...")
                if self.authenticate(force_reauth=True):
                    self.driver.get(search_url)
                    time.sleep(10)
                else:
                    return []
            
            self.last_activity = time.time()
            
            # Extract video tweets
            tweet_elements = self.driver.find_elements(By.CSS_SELECTOR, "article[data-testid='tweet']")
            discovered_videos = []
            
            for i, tweet_element in enumerate(tweet_elements[:20]):
                try:
                    # Check for video
                    video_selectors = ["video", "[data-testid='videoPlayer']", "[data-testid='videoComponent']"]
                    has_video = any(tweet_element.find_elements(By.CSS_SELECTOR, sel) for sel in video_selectors)
                    
                    if not has_video:
                        continue
                    
                    # Extract URL
                    tweet_url = None
                    status_links = tweet_element.find_elements(By.CSS_SELECTOR, "a[href*='/status/']")
                    for link in status_links:
                        link_href = link.get_attribute("href")
                        if "/status/" in link_href:
                            tweet_url = link_href
                            break
                    
                    if not tweet_url:
                        continue
                    
                    # Extract text
                    tweet_text = "Text not found"
                    try:
                        text_element = tweet_element.find_element(By.CSS_SELECTOR, "[data-testid='tweetText']")
                        if text_element:
                            tweet_text = text_element.text.strip()
                    except:
                        pass
                    
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
                        "source": "browser_automation_enhanced",
                        "requires_ytdlp": True,
                        "video_page_url": f"https://x.com/i/status/{tweet_id}"
                    }
                    discovered_videos.append(video_entry)
                    
                    if len(discovered_videos) >= max_results:
                        break
                
                except Exception:
                    continue
            
            print(f"✅ Found {len(discovered_videos)} video tweets")
            return discovered_videos
        
        result = self._safe_driver_action(search_action)
        return result if result is not None else []
    
    async def startup(self):
        """🔥 STARTUP AUTHENTICATION - Authenticate IMMEDIATELY during service startup"""
        print("🚀 FastAPI lifespan startup...")
        print("🚀 Starting STANDALONE Twitter Session Service...")
        
        # 🔥 AUTHENTICATE IMMEDIATELY AT STARTUP - AS REQUESTED
        print("🔧 Quick authentication check...")
        
        # Run authentication during startup
        if self.authenticate():
            print("✅ Authentication successful during startup!")
            self.startup_complete = True
            return True
        else:
            print("❌ Authentication failed during startup")
            self.startup_complete = False
            # Still mark startup as complete so FastAPI can start
            return True
    
    def cleanup(self):
        """Cleanup with process killing"""
        if self.driver:
            try:
                self.driver.quit()
                flush_print("✅ Browser session cleaned up")
            except:
                pass
        
        # 🔥 KILL ANY REMAINING CHROME PROCESSES
        import subprocess
        try:
            subprocess.run(["pkill", "-f", "chrome"], check=False, capture_output=True)
            subprocess.run(["pkill", "-f", "chromedriver"], check=False, capture_output=True)
            flush_print("🔧 Killed remaining Chrome processes")
        except:
            pass
            
        self.authenticated = False
        self.startup_complete = False

# Create instance
twitter_session = TwitterSessionManager()

@asynccontextmanager
async def lifespan(app: FastAPI):
    # 🔥 STARTUP AUTHENTICATION - You'll see all the logs in Docker!
    await twitter_session.startup()
    yield
    # Cleanup
    twitter_session.cleanup()

# FastAPI app
app = FastAPI(
    title="Standalone Twitter Session Service", 
    version="2.0.0",
    lifespan=lifespan
)

class VideoSearchRequest(BaseModel):
    search_query: str
    max_results: int = 3

@app.get("/health")
async def health_check():
    """Health check"""
    return {
        "status": "healthy",
        "authenticated": twitter_session.authenticated,
        "startup_complete": twitter_session.startup_complete,
        "service": "standalone_twitter_v2_with_startup_auth"
    }

@app.post("/search")
async def search_videos(request: VideoSearchRequest):
    """Search videos"""
    try:
        videos = twitter_session.search_videos(request.search_query, request.max_results)
        return {"status": "success", "videos": videos, "count": len(videos)}
    except Exception as e:
        print(f"❌ Search error: {e}")
        raise HTTPException(status_code=500, detail=str(e))

@app.post("/authenticate")
async def force_authenticate():
    """Force authentication"""
    try:
        success = twitter_session.authenticate(force_reauth=True)
        return {"status": "success" if success else "failed", "authenticated": success}
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

if __name__ == "__main__":
    import uvicorn
    print("🚀 Starting Enhanced Twitter Service on port 8888...")
    uvicorn.run(app, host="0.0.0.0", port=8888)