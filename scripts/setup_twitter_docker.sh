#!/bin/bash
# One-time Twitter authentication setup for Docker
# Run this ONCE to save Twitter cookies

echo "🐦 Twitter Authentication Setup"
echo "================================"
echo ""
echo "This will:"
echo "1. Start a temporary container"
echo "2. Prompt you to login to Twitter"
echo "3. Save cookies to persistent volume"
echo ""
read -p "Press ENTER to continue..."

# Build the image if needed
echo "📦 Building Docker image..."
docker compose build

# Create a simple setup script that runs inside container
cat > /tmp/twitter_setup.py << 'EOF'
import pickle
import os
from selenium import webdriver
from selenium.webdriver.chrome.options import Options
from selenium.webdriver.chrome.service import Service
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC
import time

print("🔐 Setting up Twitter authentication...")
print("")

# Setup browser
chrome_options = Options()
chrome_options.add_argument("--no-sandbox")
chrome_options.add_argument("--disable-dev-shm-usage")
chrome_options.add_argument("--window-size=1920,1080")

service = Service("/usr/bin/chromedriver")
driver = webdriver.Chrome(service=service, options=chrome_options)

# Navigate to Twitter login
driver.get("https://twitter.com/login")
time.sleep(3)

# Get credentials from environment
username = os.getenv('TWITTER_USERNAME', '')
password = os.getenv('TWITTER_PASSWORD', '')
email = os.getenv('TWITTER_EMAIL', '')

if not username:
    username = input("Twitter username: ")
if not password:
    import getpass
    password = getpass.getpass("Twitter password: ")

# Login
try:
    username_input = WebDriverWait(driver, 10).until(
        EC.presence_of_element_located((By.NAME, "text"))
    )
    username_input.send_keys(username)
    
    next_button = driver.find_element(By.XPATH, "//span[text()='Next']/..")
    next_button.click()
    time.sleep(3)
    
    # Handle email verification if needed
    try:
        email_input = driver.find_element(By.NAME, "text")
        if not email:
            email = input("Twitter email (if prompted): ")
        email_input.send_keys(email)
        next_button = driver.find_element(By.XPATH, "//span[text()='Next']/..")
        next_button.click()
        time.sleep(3)
    except:
        pass
    
    # Password
    password_input = WebDriverWait(driver, 10).until(
        EC.presence_of_element_located((By.NAME, "password"))
    )
    password_input.send_keys(password)
    
    login_button = driver.find_element(By.XPATH, "//span[text()='Log in']/..")
    login_button.click()
    time.sleep(5)
    
    # Verify success
    current_url = driver.current_url
    if "home" in current_url:
        print("✅ Login successful!")
        
        # Save cookies
        cookies = driver.get_cookies()
        cookies_file = os.getenv('TWITTER_COOKIES_FILE', '/data/twitter_cookies.pkl')
        os.makedirs(os.path.dirname(cookies_file), exist_ok=True)
        
        with open(cookies_file, 'wb') as f:
            pickle.dump(cookies, f)
        
        print(f"✅ Saved {len(cookies)} cookies to {cookies_file}")
        print("")
        print("Setup complete! Start the service with:")
        print("  docker compose up -d twitter-session")
    else:
        print("❌ Login failed - please check credentials")

except Exception as e:
    print(f"❌ Setup failed: {e}")

finally:
    driver.quit()
EOF

# Run setup in container
docker compose run --rm \
  -e DISPLAY=:99 \
  -v /tmp/twitter_setup.py:/app/twitter_setup.py \
  twitter-session bash -c "
  Xvfb :99 -screen 0 1920x1080x24 > /dev/null 2>&1 &
  sleep 2
  python /app/twitter_setup.py
  "

# Cleanup
rm /tmp/twitter_setup.py

echo ""
echo "✅ Setup complete!"
