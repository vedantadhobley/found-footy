#!/usr/bin/env python3
"""Check what's actually in MinIO"""
import sys
sys.path.insert(0, '/app')

def check_minio_contents():
    """Check MinIO bucket contents"""
    print("☁️ Checking MinIO Contents...")
    print("=" * 40)
    
    try:
        from found_footy.storage.s3_store import FootyS3Store
        
        s3_store = FootyS3Store()
        
        # Get bucket stats
        stats = s3_store.get_bucket_stats()
        print(f"📊 Bucket Stats:")
        print(f"   Bucket: {stats.get('bucket_name')}")
        print(f"   Total Videos: {stats.get('total_videos', 0)}")
        print(f"   Total Size: {stats.get('total_size_mb', 0)} MB")
        print(f"   Endpoint: {stats.get('endpoint')}")
        
        # List all objects
        print(f"\n📁 Objects in bucket:")
        
        response = s3_store.s3_client.list_objects_v2(Bucket=s3_store.bucket_name)
        
        if 'Contents' in response:
            for obj in response['Contents']:
                size_mb = obj['Size'] / (1024 * 1024)
                print(f"   📄 {obj['Key']}")
                print(f"      Size: {size_mb:.2f} MB")
                print(f"      Modified: {obj['LastModified']}")
                print(f"      URL: {s3_store.endpoint_url}/{s3_store.bucket_name}/{obj['Key']}")
                print()
        else:
            print("   (empty)")
        
        return True
        
    except Exception as e:
        print(f"❌ Error checking MinIO: {e}")
        return False

if __name__ == "__main__":
    check_minio_contents()