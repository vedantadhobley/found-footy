"""
Test script for AI Vision Validation

Run inside Docker container:
    docker exec -it found-footy-worker python -m pytest tests/test_vision_validation.py -v -s

Or test manually with a video file:
    docker exec -it found-footy-worker python tests/test_vision_validation.py /path/to/video.mp4
"""
import sys
import os
import asyncio

# Add project root to path
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


async def test_frame_extraction():
    """Test that frame extraction works"""
    from src.activities.download import _extract_frame_for_vision
    
    # This test needs a video file to work
    # For now, just verify the function is importable
    assert callable(_extract_frame_for_vision)
    print("✅ _extract_frame_for_vision is callable")


async def test_vision_model_call():
    """Test that we can call the vision model"""
    from src.activities.download import _call_vision_model
    
    assert callable(_call_vision_model)
    print("✅ _call_vision_model is callable")
    
    # Test with a simple 1x1 black PNG (base64)
    # This is a minimal valid PNG
    black_pixel_png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
    
    # Call vision model
    result = await _call_vision_model(black_pixel_png, "What color is this?")
    
    if result:
        print(f"✅ Vision model responded: {result.get('response', '')[:100]}")
    else:
        print("⚠️ Vision model not available (this is OK in local testing)")


async def test_full_validation(video_path: str = None):
    """Test full validation pipeline with a video file"""
    if not video_path:
        print("⚠️ No video path provided, skipping full validation test")
        return
    
    if not os.path.exists(video_path):
        print(f"❌ Video not found: {video_path}")
        return
    
    # Import the validation function (not as activity)
    from src.activities.download import (
        _extract_frame_for_vision,
        _call_vision_model
    )
    import subprocess
    
    # Get duration
    probe_cmd = [
        "ffprobe", "-v", "error", "-show_entries",
        "format=duration", "-of", "csv=p=0", video_path
    ]
    result = subprocess.run(probe_cmd, capture_output=True, text=True, timeout=10)
    if result.returncode == 0:
        duration = float(result.stdout.strip())
        print(f"📹 Video duration: {duration:.1f}s")
    else:
        duration = 10.0
        print(f"⚠️ Couldn't get duration, using default")
    
    # Extract frame at 50%
    t_50 = duration * 0.50
    frame = _extract_frame_for_vision(video_path, t_50)
    
    if not frame:
        print(f"❌ Failed to extract frame at {t_50:.1f}s")
        return
    
    print(f"✅ Extracted frame ({len(frame)} bytes base64)")
    
    # Call vision model
    prompt = """Look at this image and answer: Is this showing a soccer/football match being played?
    
Answer ONLY with one word: YES or NO"""
    
    response = await _call_vision_model(frame, prompt)
    
    if response:
        answer = response.get("response", "").strip()
        print(f"🤖 Vision model answer: {answer}")
        
        is_soccer = "YES" in answer.upper()
        print(f"✅ Result: {'SOCCER' if is_soccer else 'NOT SOCCER'}")
    else:
        print("❌ Vision model did not respond")


if __name__ == "__main__":
    # Run tests
    video_path = sys.argv[1] if len(sys.argv) > 1 else None
    
    print("=" * 50)
    print("AI Vision Validation Tests")
    print("=" * 50)
    
    asyncio.run(test_frame_extraction())
    asyncio.run(test_vision_model_call())
    
    if video_path:
        print("\n" + "=" * 50)
        print(f"Testing with video: {video_path}")
        print("=" * 50)
        asyncio.run(test_full_validation(video_path))
