#!/usr/bin/env python3
"""
Comprehensive Twitter Worker Debug Script
Run this to diagnose environment and network issues
"""
import os
import sys
import time
import json
import platform
import subprocess
from datetime import datetime

def print_header(title):
    print(f"\n{'='*60}")
    print(f"🔍 {title}")
    print(f"{'='*60}")

def check_system_info():
    """Check basic system information"""
    print_header("SYSTEM INFORMATION")
    
    info = {
        "Platform": platform.platform(),
        "Architecture": platform.machine(),
        "Python Version": sys.version,
        "Current Time": datetime.now().isoformat(),
        "Working Directory": os.getcwd(),
        "User": os.getenv('USER', 'unknown'),
    }
    
    for key, value in info.items():
        print(f"  {key}: {value}")
    
    # Check if we're in Docker
    if os.path.exists('/.dockerenv'):
        print("  🐳 Running inside Docker container")
    else:
        print("  💻 Running on host system")

def check_environment_variables():
    """Check all required environment variables"""
    print_header("ENVIRONMENT VARIABLES")
    
    required_vars = [
        'PREFECT_API_URL',
        'MONGODB_URL',
        'PYTHONPATH',
        'RAPIDAPI_KEY',
        'TWITTER_USERNAME',
        'TWITTER_PASSWORD', 
        'TWITTER_EMAIL',
        'SSL_CERT_FILE',
        'PYTHONHTTPSVERIFY'
    ]
    
    missing_vars = []
    for var in required_vars:
        value = os.getenv(var)
        if value:
            if 'PASSWORD' in var or 'KEY' in var:
                display_value = f"{value[:4]}***{value[-4:]}" if len(value) > 8 else "***"
            else:
                display_value = value
            print(f"  ✅ {var}: {display_value}")
        else:
            print(f"  ❌ {var}: NOT SET")
            missing_vars.append(var)
    
    if missing_vars:
        print(f"\n⚠️ Missing environment variables: {', '.join(missing_vars)}")
        return False
    else:
        print(f"\n✅ All required environment variables are set")
        return True

def check_network_connectivity():
    """Check network connectivity to required services"""
    print_header("NETWORK CONNECTIVITY")
    
    services = [
        ("Prefect Server", "http://prefect-server:4200/api/health"),
        ("MongoDB", "mongodb:27017"),
        ("Twitter", "https://twitter.com"),
        ("API-Football", "https://api-football-v1.p.rapidapi.com"),
        ("MinIO", "http://minio:9000/minio/health/live")
    ]
    
    network_ok = True
    
    for service_name, url in services:
        try:
            if url.startswith('http'):
                import requests
                response = requests.get(url, timeout=10, verify=False)
                if response.status_code < 400:
                    print(f"  ✅ {service_name}: {response.status_code}")
                else:
                    print(f"  ⚠️ {service_name}: {response.status_code}")
                    network_ok = False
            else:
                # For non-HTTP services like MongoDB
                import socket
                host, port = url.split(':')
                sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
                sock.settimeout(5)
                result = sock.connect_ex((host, int(port)))
                sock.close()
                if result == 0:
                    print(f"  ✅ {service_name}: Connected")
                else:
                    print(f"  ❌ {service_name}: Connection failed")
                    network_ok = False
        except Exception as e:
            print(f"  ❌ {service_name}: {e}")
            network_ok = False
    
    return network_ok

def check_prefect_connection():
    """Check Prefect server connection and deployment status"""
    print_header("PREFECT CONNECTION")
    
    try:
        # Check if we can import Prefect
        from prefect import get_client
        print("  ✅ Prefect import successful")
        
        # Check async client connection
        import asyncio
        
        async def check_prefect_async():
            try:
                async with get_client() as client:
                    # Test connection
                    health = await client.api_healthcheck()
                    print(f"  ✅ Prefect API health: {health}")
                    
                    # Check deployments
                    deployments = await client.read_deployments()
                    print(f"  ✅ Found {len(deployments)} deployments")
                    
                    # Look for twitter-flow specifically
                    twitter_deployments = [d for d in deployments if 'twitter' in d.name.lower()]
                    if twitter_deployments:
                        for dep in twitter_deployments:
                            print(f"    📋 {dep.name}: {dep.status if hasattr(dep, 'status') else 'Unknown status'}")
                    else:
                        print("  ⚠️ No twitter-flow deployments found")
                    
                    # Check work pools
                    work_pools = await client.read_work_pools()
                    twitter_pools = [p for p in work_pools if 'twitter' in p.name.lower()]
                    if twitter_pools:
                        for pool in twitter_pools:
                            print(f"    🏊 {pool.name}: {pool.type}")
                    else:
                        print("  ⚠️ No twitter work pools found")
                    
                    return True
            except Exception as e:
                print(f"  ❌ Prefect connection failed: {e}")
                return False
        
        return asyncio.run(check_prefect_async())
        
    except Exception as e:
        print(f"  ❌ Prefect check failed: {e}")
        return False

def check_mongodb_connection():
    """Check MongoDB connection"""
    print_header("MONGODB CONNECTION")
    
    try:
        from pymongo import MongoClient
        
        mongodb_url = os.getenv('MONGODB_URL')
        if not mongodb_url:
            print("  ❌ MONGODB_URL not set")
            return False
        
        print(f"  🔗 Connecting to: {mongodb_url.split('@')[1] if '@' in mongodb_url else mongodb_url}")
        
        client = MongoClient(mongodb_url, serverSelectionTimeoutMS=5000)
        
        # Test connection
        client.admin.command('ping')
        print("  ✅ MongoDB connection successful")
        
        # Check collections
        db = client.found_footy
        collections = db.list_collection_names()
        print(f"  📊 Collections: {', '.join(collections)}")
        
        # Check some data
        for collection_name in ['fixtures_active', 'goals_pending']:
            if collection_name in collections:
                count = db[collection_name].count_documents({})
                print(f"    📄 {collection_name}: {count} documents")
        
        client.close()
        return True
        
    except Exception as e:
        print(f"  ❌ MongoDB connection failed: {e}")
        return False

def check_browser_dependencies():
    """Check browser automation dependencies"""
    print_header("BROWSER DEPENDENCIES")
    
    checks = [
        ("Chrome/Chromium", "/usr/bin/chromium"),
        ("ChromeDriver", "/usr/bin/chromedriver"),
        ("Chrome Wrapper", "/usr/local/bin/chrome"),
        ("Display Server", "DISPLAY"),
        ("Temp Directory", "/tmp")
    ]
    
    browser_ok = True
    
    for name, path_or_env in checks:
        if path_or_env == "DISPLAY":
            # Check display
            display = os.getenv('DISPLAY')
            if display:
                print(f"  ✅ {name}: {display}")
            else:
                print(f"  ℹ️ {name}: Not set (headless mode)")
        elif path_or_env.startswith('/'):
            # Check file path
            if os.path.exists(path_or_env):
                # Check if executable
                if os.access(path_or_env, os.X_OK):
                    print(f"  ✅ {name}: {path_or_env} (executable)")
                else:
                    print(f"  ⚠️ {name}: {path_or_env} (not executable)")
                    browser_ok = False
            else:
                print(f"  ❌ {name}: {path_or_env} not found")
                browser_ok = False
    
    # Test Selenium import
    try:
        from selenium import webdriver
        from selenium.webdriver.chrome.options import Options
        print("  ✅ Selenium import successful")
    except ImportError as e:
        print(f"  ❌ Selenium import failed: {e}")
        browser_ok = False
    
    return browser_ok

def test_twitter_credentials():
    """Test Twitter credentials format"""
    print_header("TWITTER CREDENTIALS")
    
    credentials = {
        'TWITTER_USERNAME': os.getenv('TWITTER_USERNAME'),
        'TWITTER_PASSWORD': os.getenv('TWITTER_PASSWORD'),
        'TWITTER_EMAIL': os.getenv('TWITTER_EMAIL')
    }
    
    creds_ok = True
    for name, value in credentials.items():
        if value:
            if name == 'TWITTER_EMAIL':
                if '@' in value and '.' in value:
                    print(f"  ✅ {name}: Valid email format")
                else:
                    print(f"  ⚠️ {name}: Invalid email format")
                    creds_ok = False
            else:
                print(f"  ✅ {name}: Set ({len(value)} chars)")
        else:
            print(f"  ❌ {name}: Not set")
            creds_ok = False
    
    return creds_ok

def test_worker_startup_simulation():
    """Simulate what happens when twitter-worker starts"""
    print_header("WORKER STARTUP SIMULATION")
    
    try:
        print("  🚀 Simulating worker startup...")
        
        # ✅ CHANGE: Import the new Twitter architecture
        from found_footy.flows.twitter_flow import twitter_flow, TwitterAPIClient
        print("  ✅ Twitter flow import successful")
        
        # Test new session service client
        client = TwitterAPIClient()
        print("  ✅ TwitterAPIClient initialization successful")
        
        # Test session service connectivity
        import requests
        try:
            response = requests.get("http://twitter-session:8888/health", timeout=5)
            if response.status_code == 200:
                print("  ✅ Twitter Session Service reachable")
            else:
                print(f"  ⚠️ Twitter Session Service returned {response.status_code}")
        except Exception as e:
            print(f"  ⚠️ Twitter Session Service unreachable: {e}")
        
        # Test basic flow structure
        import inspect
        sig = inspect.signature(twitter_flow.fn)
        print(f"  ✅ Twitter flow signature: {sig}")
        
        return True
        
    except Exception as e:
        print(f"  ❌ Worker startup simulation failed: {e}")
        import traceback
        traceback.print_exc()
        return False

def generate_debug_report():
    """Generate comprehensive debug report"""
    print_header("COMPREHENSIVE DEBUG REPORT")
    
    checks = [
        ("System Info", lambda: (check_system_info(), True)[1]),
        ("Environment Variables", check_environment_variables),
        ("Network Connectivity", check_network_connectivity),
        ("Prefect Connection", check_prefect_connection),
        ("MongoDB Connection", check_mongodb_connection),
        ("Browser Dependencies", check_browser_dependencies),
        ("Twitter Credentials", test_twitter_credentials),
        ("Worker Startup", test_worker_startup_simulation)
    ]
    
    results = {}
    for check_name, check_func in checks:
        try:
            result = check_func()
            results[check_name] = "✅ PASS" if result else "❌ FAIL"
        except Exception as e:
            results[check_name] = f"❌ ERROR: {e}"
    
    print_header("SUMMARY")
    for check_name, result in results.items():
        print(f"  {result}: {check_name}")
    
    # Overall status
    failed_checks = [name for name, result in results.items() if "❌" in result]
    if failed_checks:
        print(f"\n❌ OVERALL: {len(failed_checks)} checks failed")
        print(f"🔧 Failed checks: {', '.join(failed_checks)}")
        return False
    else:
        print(f"\n✅ OVERALL: All checks passed!")
        return True

if __name__ == "__main__":
    print("🔍 TWITTER WORKER DEBUG SCRIPT")
    print(f"⏰ Started at: {datetime.now().isoformat()}")
    
    success = generate_debug_report()
    
    if success:
        print("\n🎉 No issues detected! Twitter worker should be ready.")
    else:
        print("\n🚨 Issues detected. Review the failed checks above.")
        print("💡 Share this output for debugging assistance.")
    
    sys.exit(0 if success else 1)