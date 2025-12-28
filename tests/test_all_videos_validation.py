#!/usr/bin/env python3
"""
Test smart 2-3 check AI validation on ALL production videos.

Usage:
    docker exec found-footy-prod-worker python /workspace/tests/test_all_videos_validation.py
"""
import boto3
import os
import subprocess
import base64
import asyncio
import httpx
import time

OLLAMA_URL = os.getenv('OLLAMA_URL', 'http://ollama-server:11434')

s3 = boto3.client('s3',
    endpoint_url=os.getenv('S3_ENDPOINT_URL'),
    aws_access_key_id=os.getenv('S3_ACCESS_KEY'),
    aws_secret_access_key=os.getenv('S3_SECRET_KEY'),
    region_name='us-east-1'
)

def get_duration(path):
    cmd = ['ffprobe', '-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', path]
    result = subprocess.run(cmd, capture_output=True, text=True, timeout=10)
    return float(result.stdout.strip()) if result.returncode == 0 else 10.0

def extract_frame(path, timestamp):
    cmd = ['ffmpeg', '-ss', str(timestamp), '-i', path, '-vframes', '1', '-f', 'image2pipe', '-vcodec', 'png', '-']
    result = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=15)
    if result.returncode == 0 and result.stdout:
        return base64.b64encode(result.stdout).decode('utf-8')
    return None

async def call_vision(frame_b64, prompt):
    async with httpx.AsyncClient(timeout=60.0) as client:
        response = await client.post(
            f'{OLLAMA_URL}/api/generate',
            json={
                'model': 'qwen3-vl:8b-instruct',
                'prompt': prompt,
                'images': [frame_b64],
                'stream': False,
                'options': {'num_predict': 100, 'temperature': 0.1}
            }
        )
        if response.status_code == 200:
            return response.json()
    return None

def parse_response(resp):
    if not resp:
        return None
    text = resp.get('response', '').strip().upper()
    return 'YES' in text

PROMPT = """/no_think
Look at this image and answer: Is this showing a soccer/football match being played?

Answer ONLY with one word: YES or NO

If you see ANY of these, answer YES:
- Soccer/football field with green grass and white lines
- Players playing soccer/football
- Soccer goal posts or nets
- Soccer/football match action

If you see ANY of these, answer NO:
- Advertisements or commercials
- Interviews or press conferences  
- Still graphics or logos only
- Different sport (basketball, tennis, etc.)
- Empty screens or test patterns"""

async def smart_validate(key):
    """Smart 2-3 check validation"""
    local_path = '/tmp/test_video.mp4'
    try:
        s3.download_file('footy-videos', key, local_path)
    except Exception as e:
        return {'key': key, 'error': str(e)}
    
    duration = get_duration(local_path)
    
    # Check 25% and 75% first
    t_25, t_75 = duration * 0.25, duration * 0.75
    frame_25 = extract_frame(local_path, t_25)
    frame_75 = extract_frame(local_path, t_75)
    
    checks = 0
    resp_25 = await call_vision(frame_25, PROMPT) if frame_25 else None
    vote_25 = parse_response(resp_25)
    checks += 1 if frame_25 else 0
    
    resp_75 = await call_vision(frame_75, PROMPT) if frame_75 else None
    vote_75 = parse_response(resp_75)
    checks += 1 if frame_75 else 0
    
    tiebreaker = False
    vote_50 = None
    
    if vote_25 == vote_75:
        is_soccer = vote_25
    else:
        tiebreaker = True
        t_50 = duration * 0.50
        frame_50 = extract_frame(local_path, t_50)
        resp_50 = await call_vision(frame_50, PROMPT) if frame_50 else None
        vote_50 = parse_response(resp_50)
        checks += 1
        
        yes_votes = sum([vote_25 or False, vote_50 or False, vote_75 or False])
        is_soccer = yes_votes >= 2
    
    os.remove(local_path)
    
    return {
        'key': key,
        'duration': duration,
        'is_soccer': is_soccer,
        'checks': checks,
        'tiebreaker': tiebreaker,
        'votes': {'25': vote_25, '50': vote_50, '75': vote_75}
    }

async def main():
    # Get all videos
    response = s3.list_objects_v2(Bucket='footy-videos')
    videos = [obj['Key'] for obj in response.get('Contents', []) if obj['Key'].endswith('.mp4')]
    
    print(f'Testing {len(videos)} videos with smart 2-3 check validation')
    print('='*70)
    
    passed = []
    failed = []
    tiebreakers = 0
    total_checks = 0
    
    start = time.time()
    
    for i, key in enumerate(videos):
        result = await smart_validate(key)
        
        if 'error' in result:
            print(f'[{i+1}/{len(videos)}] ERROR: {key} - {result["error"]}')
            continue
        
        is_soccer = result['is_soccer']
        checks = result['checks']
        tb = result['tiebreaker']
        dur = result['duration']
        
        total_checks += checks
        if tb:
            tiebreakers += 1
        
        fname = key.split('/')[-1]
        status = '✅' if is_soccer else '❌'
        tb_str = ' [TB]' if tb else ''
        
        if is_soccer:
            passed.append(key)
        else:
            failed.append(key)
            # Print failed ones with details
            v = result['votes']
            print(f'[{i+1}/{len(videos)}] {status} {fname} ({dur:.0f}s) - 25%={v["25"]}, 75%={v["75"]}, 50%={v["50"]}{tb_str}')
        
        # Progress every 20
        if (i+1) % 20 == 0:
            elapsed = time.time() - start
            rate = (i+1) / elapsed
            remaining = (len(videos) - i - 1) / rate
            print(f'--- Progress: {i+1}/{len(videos)} ({len(passed)} passed, {len(failed)} failed) - ETA: {remaining:.0f}s ---')
    
    elapsed = time.time() - start
    
    print()
    print('='*70)
    print(f'RESULTS: {len(passed)} passed, {len(failed)} failed out of {len(videos)}')
    print(f'Tiebreakers needed: {tiebreakers} ({tiebreakers/len(videos)*100:.1f}%)')
    print(f'Total AI checks: {total_checks} (avg {total_checks/len(videos):.2f} per video)')
    print(f'Time: {elapsed:.0f}s ({elapsed/len(videos):.1f}s per video)')
    print()
    
    if failed:
        print('FAILED VIDEOS:')
        for f in failed:
            print(f'  {f}')

if __name__ == '__main__':
    asyncio.run(main())
