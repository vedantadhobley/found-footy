#!/usr/bin/env python3
"""Real pipeline test - No mocks, actual video download to MinIO"""
import os
import sys
import tempfile
import ssl
import urllib3
from datetime import datetime, timezone

# Add the project root to the path
sys.path.insert(0, '/app')

def setup_ssl_environment():
    """Configure SSL environment for snscrape"""
    print("🔧 Configuring SSL environment...")
    
    # Fix SSL configuration
    os.environ['PYTHONHTTPSVERIFY'] = '0'
    os.environ['SSL_CERT_FILE'] = '/etc/ssl/certs/ca-certificates.crt'
    os.environ['SSL_CERT_DIR'] = '/etc/ssl/certs'
    
    # Remove problematic empty CA bundle
    if 'CURL_CA_BUNDLE' in os.environ:
        del os.environ['CURL_CA_BUNDLE']
    if 'REQUESTS_CA_BUNDLE' in os.environ:
        del os.environ['REQUESTS_CA_BUNDLE']
    
    # Disable SSL warnings
    urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
    
    # Configure SSL context
    try:
        ssl_context = ssl.create_default_context()
        ssl_context.check_hostname = False
        ssl_context.verify_mode = ssl.CERT_NONE
        print("✅ SSL context configured")
    except Exception as e:
        print(f"⚠️ SSL context warning: {e}")

def test_real_snscrape():
    """Test real snscrape without mocks"""
    print("🐦 Testing real snscrape...")
    
    try:
        import snscrape.modules.twitter as sntwitter
        
        # Test search for a recent popular goal - use simpler search
        search_queries = [
            "Haaland goal",
            "Messi goal", 
            "football goal",
            "soccer goal"
        ]
        
        all_videos_found = []
        
        for search_query in search_queries:
            try:
                print(f"🔍 Searching: '{search_query}'...")
                scraper = sntwitter.TwitterSearchScraper(f"{search_query} filter:videos")
                
                videos_found = []
                for i, tweet in enumerate(scraper.get_items()):
                    if i >= 2:  # Limit to 2 tweets per query
                        break
                        
                    media_list = getattr(tweet, "media", None)
                    if not media_list:
                        continue
                        
                    has_video = any(
                        (getattr(m, "type", None) and "video" in str(getattr(m, "type")).lower())
                        or ("video" in type(m).__name__.lower())
                        for m in media_list
                    )
                    
                    if has_video:
                        username = getattr(getattr(tweet, "user", None), "username", "user")
                        tweet_id = str(getattr(tweet, "id", ""))
                        tweet_url = f"https://x.com/{username}/status/{tweet_id}"
                        
                        video_info = {
                            "tweet_url": tweet_url,
                            "tweet_id": tweet_id,
                            "username": username,
                            "text": getattr(tweet, "rawContent", "")[:100],
                            "search_query": search_query
                        }
                        videos_found.append(video_info)
                        all_videos_found.append(video_info)
                        
                        print(f"✅ Found video tweet: {tweet_url}")
                        print(f"   Text: {getattr(tweet, 'rawContent', '')[:100]}...")
                
                if videos_found:
                    print(f"✅ Query '{search_query}' found {len(videos_found)} videos")
                    break  # Use first successful query
                else:
                    print(f"⚠️ Query '{search_query}' found no videos")
                    
            except Exception as e:
                print(f"❌ Query '{search_query}' failed: {e}")
                continue
        
        if all_videos_found:
            print(f"✅ snscrape found {len(all_videos_found)} total videos")
            return all_videos_found
        else:
            print("❌ No videos found with any search query")
            return []
        
    except Exception as e:
        print(f"❌ snscrape failed: {e}")
        print("💡 This might be due to:")
        print("   1. Network connectivity issues")
        print("   2. Twitter rate limiting")
        print("   3. snscrape version compatibility")
        return []

def test_real_ytdlp(tweet_url):
    """Test real yt-dlp download"""
    print(f"📥 Testing real yt-dlp download: {tweet_url}")
    
    try:
        import yt_dlp
        
        with tempfile.TemporaryDirectory() as temp_dir:
            output_template = os.path.join(temp_dir, "test_video.%(ext)s")
            
            # Configure yt-dlp with more permissive settings
            ydl_opts = {
                'format': 'best[height<=720]/best',
                'outtmpl': output_template,
                'quiet': True,
                'no_warnings': True,
                'ignore_errors': True,
                'no_check_certificate': True,
            }
            
            with yt_dlp.YoutubeDL(ydl_opts) as ydl:
                print(f"   Extracting info...")
                try:
                    info = ydl.extract_info(tweet_url, download=False)
                except Exception as e:
                    print(f"   ⚠️ Info extraction failed: {e}")
                    print(f"   🔄 Trying direct download...")
                    info = None
                
                if info:
                    print(f"   Title: {info.get('title', 'Unknown')}")
                    print(f"   Duration: {info.get('duration', 'Unknown')} seconds")
                    print(f"   Format: {info.get('ext', 'Unknown')}")
                    
                print(f"   Downloading...")
                try:
                    ydl.download([tweet_url])
                    
                    # Find downloaded file
                    files = [f for f in os.listdir(temp_dir) if not f.endswith('.info.json')]
                    if files:
                        downloaded_file = os.path.join(temp_dir, files[0])
                        file_size = os.path.getsize(downloaded_file)
                        print(f"✅ Downloaded: {files[0]} ({file_size} bytes)")
                        return downloaded_file, info or {"title": "Downloaded video", "duration": "unknown"}
                    else:
                        print("❌ No video file found after download")
                        return None, None
                        
                except Exception as e:
                    print(f"❌ Download failed: {e}")
                    return None, None
                    
    except Exception as e:
        print(f"❌ yt-dlp failed: {e}")
        return None, None

def test_real_s3_upload(video_file, video_info):
    """Test real S3 upload to MinIO"""
    print(f"☁️ Testing real S3 upload: {video_file}")
    
    try:
        from found_footy.storage.s3_store import FootyS3Store
        
        s3_store = FootyS3Store()
        
        # Test upload
        result = s3_store.upload_video_file(
            video_file,
            "test_12345_67_789",  # goal_id
            0,  # search_index
            0,  # video_index
            metadata={
                "test_run": True,
                "video_title": video_info.get("title", "Test Video"),
                "video_duration": str(video_info.get("duration", "unknown")),
                "uploaded_at": datetime.now(timezone.utc).isoformat(),
                "source": "real_pipeline_test"
            }
        )
        
        if result["status"] == "success":
            print(f"✅ S3 upload successful!")
            print(f"   S3 Key: {result['s3_key']}")
            print(f"   S3 URL: {result['s3_url']}")
            print(f"   File Size: {result['file_size']} bytes")
            return result
        else:
            print(f"❌ S3 upload failed: {result.get('error')}")
            return None
            
    except Exception as e:
        print(f"❌ S3 upload failed: {e}")
        return None

def main():
    """Run the complete real pipeline test"""
    print("🧪 REAL PIPELINE TEST - NO MOCKS")
    print("=" * 50)
    
    # Step 0: Configure SSL environment
    setup_ssl_environment()
    
    # Step 1: Test snscrape
    videos = test_real_snscrape()
    if not videos:
        print("❌ No videos found, stopping test")
        print("💡 Try running the test again - Twitter scraping can be flaky")
        return False
    
    # Step 2: Test yt-dlp with first video
    first_video = videos[0]
    print(f"\n📥 Testing download of: {first_video['tweet_url']}")
    
    downloaded_file, video_info = test_real_ytdlp(first_video['tweet_url'])
    if not downloaded_file:
        print("❌ Download failed, stopping test")
        print("💡 The tweet might not have downloadable video content")
        return False
    
    # Step 3: Test S3 upload
    print(f"\n☁️ Testing S3 upload...")
    upload_result = test_real_s3_upload(downloaded_file, video_info)
    if not upload_result:
        print("❌ S3 upload failed")
        return False
    
    print("\n" + "=" * 50)
    print("✅ REAL PIPELINE TEST COMPLETE!")
    print(f"✅ Video successfully downloaded and uploaded to MinIO")
    print(f"✅ Check MinIO Console: http://localhost:9001")
    print(f"✅ Bucket: footy-videos")
    print(f"✅ Key: {upload_result['s3_key']}")
    print("=" * 50)
    
    return True

if __name__ == "__main__":
    success = main()
    if success:
        print("\n🎉 REAL PIPELINE WORKING!")
        print("🚀 Your Twitter → yt-dlp → S3 pipeline is functional!")
    else:
        print("\n💻 PIPELINE NEEDS DEBUGGING")
        print("🔧 Check the error messages above for specific issues")
    
    sys.exit(0 if success else 1)