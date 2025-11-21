#!/usr/bin/env python3
"""
Test script for Twitter service

Run this to verify the Twitter service is working correctly.
"""
import requests
import sys
import json
import time


BASE_URL = "http://localhost:3103"


def print_section(title):
    """Print a section header"""
    print()
    print("=" * 60)
    print(f"  {title}")
    print("=" * 60)
    print()


def test_health():
    """Test the health endpoint"""
    print_section("Testing Health Endpoint")
    
    try:
        response = requests.get(f"{BASE_URL}/health", timeout=5)
        data = response.json()
        
        print(f"Status Code: {response.status_code}")
        print(f"Response: {json.dumps(data, indent=2)}")
        
        if data.get("authenticated"):
            print("✅ Service is authenticated and healthy!")
            return True
        else:
            print("⚠️  Service is running but NOT authenticated")
            print(f"   Visit {BASE_URL}/login to authenticate")
            return False
            
    except requests.exceptions.ConnectionError:
        print("❌ Cannot connect to Twitter service")
        print(f"   Is it running? Try: docker compose up -d twitter-session")
        return False
    except Exception as e:
        print(f"❌ Error: {e}")
        return False


def test_search():
    """Test the search endpoint"""
    print_section("Testing Search Endpoint")
    
    search_query = "Messi goal"
    print(f"Searching for: '{search_query}'")
    print()
    
    try:
        response = requests.post(
            f"{BASE_URL}/search",
            json={"search_query": search_query, "max_results": 2},
            timeout=30
        )
        
        print(f"Status Code: {response.status_code}")
        
        if response.status_code == 200:
            data = response.json()
            videos = data.get("videos", [])
            
            print(f"✅ Found {len(videos)} videos")
            print()
            
            for i, video in enumerate(videos, 1):
                print(f"Video {i}:")
                print(f"  Tweet URL: {video.get('tweet_url')}")
                print(f"  Text: {video.get('tweet_text', '')[:100]}...")
                print()
            
            return len(videos) > 0
        else:
            print(f"❌ Search failed with status {response.status_code}")
            print(f"Response: {response.text}")
            return False
            
    except Exception as e:
        print(f"❌ Error: {e}")
        return False


def test_root():
    """Test the root endpoint"""
    print_section("Testing Root Endpoint")
    
    try:
        response = requests.get(f"{BASE_URL}/", timeout=5)
        data = response.json()
        
        print(f"Service: {data.get('service')}")
        print(f"Version: {data.get('version')}")
        print(f"Status: {data.get('status')}")
        print(f"Authenticated: {data.get('authenticated')}")
        print()
        print("Available endpoints:")
        for name, path in data.get('endpoints', {}).items():
            print(f"  {name:15} → {path}")
        
        return True
        
    except Exception as e:
        print(f"❌ Error: {e}")
        return False


def main():
    """Run all tests"""
    print()
    print("🐦 Twitter Service Test Suite")
    print()
    
    # Test 1: Root endpoint
    if not test_root():
        print("\n❌ Root endpoint test failed")
        return 1
    
    # Test 2: Health check
    authenticated = test_health()
    
    if not authenticated:
        print()
        print("=" * 60)
        print("  ⚠️  Service is not authenticated")
        print("=" * 60)
        print()
        print("To authenticate:")
        print(f"  1. Visit {BASE_URL}/login")
        print("  2. Follow instructions to copy cookies")
        print("  3. Run this test again")
        print()
        return 1
    
    # Test 3: Search (only if authenticated)
    time.sleep(1)  # Brief pause
    if not test_search():
        print("\n⚠️  Search test failed (but service is authenticated)")
        print("   This might be a temporary Twitter issue")
        return 1
    
    # All tests passed
    print()
    print("=" * 60)
    print("  ✅ All Tests Passed!")
    print("=" * 60)
    print()
    print("Twitter service is fully operational! 🎉")
    print()
    
    return 0


if __name__ == "__main__":
    sys.exit(main())
