# Geo-Restriction Bypass: Future Implementation

## Problem Statement

Some Twitter video CDN URLs return `403 Forbidden` due to IP-based geo-restrictions. This happens because:

1. **Broadcaster accounts** (DSports, somos_FOX, ssporttr, footballontnt, S Sport Turkey) geo-restrict their video content
2. **CDN restriction is IP-based**, not cookie-based - even authenticated browser sessions get 403 from these CDNs
3. **Current impact**: ~20-30% of discovered goal videos fail to download due to geo-restrictions

### Confirmed Geo-Restricted Accounts
- `DSports` - Latin America broadcaster (Argentina focus)
- `somos_FOX` - Latin America broadcaster  
- `ssporttr` / `ssportplustr` / `sspaborsa` - Turkish broadcaster (S Sport)
- `footballontnt` - UK/US broadcaster (TNT Sports)

### Technical Details

The syndication API successfully returns video metadata and CDN URLs:
```
https://cdn.syndication.twimg.com/tweet-result?id={tweet_id}&token=x
→ Returns video variants with CDN URLs like:
   https://video.twimg.com/amplify_video/...mp4
```

But the CDN request fails:
```
curl -I "https://video.twimg.com/amplify_video/..."
HTTP/2 403
```

This is **IP-based restriction** - the CDN checks the client IP and blocks based on geography, regardless of cookies or authentication headers.

---

## Proposed Solution: Multi-Region Proxy Pool

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                        Download Activity                                 │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  1. First attempt: Direct download (no proxy)                           │
│     ↓                                                                    │
│  2. If 403: Check account → select region-appropriate proxy             │
│     ↓                                                                    │
│  3. Retry via proxy for that region                                     │
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘
```

### Implementation Plan

#### 1. Account-to-Region Mapping

Create a mapping of known broadcaster accounts to their target regions:

```python
# src/utils/geo_config.py

GEO_RESTRICTED_ACCOUNTS = {
    # Latin America broadcasters
    "DSports": "AR",      # Argentina
    "somos_FOX": "MX",    # Mexico/Latin America
    
    # Turkish broadcasters  
    "ssporttr": "TR",
    "ssportplustr": "TR",
    "sspaborsa": "TR",
    
    # UK/US broadcasters
    "footballontnt": "GB",  # UK first, fallback to US
    
    # Default for unknown
    "_default": "US",
}

def get_region_for_account(username: str) -> str:
    """Get the target region for a geo-restricted account."""
    return GEO_RESTRICTED_ACCOUNTS.get(username, GEO_RESTRICTED_ACCOUNTS["_default"])
```

#### 2. Proxy Configuration

Environment variables for proxy pool:

```bash
# .env
PROXY_AR=socks5://user:pass@proxy-ar.example.com:1080
PROXY_MX=socks5://user:pass@proxy-mx.example.com:1080
PROXY_TR=socks5://user:pass@proxy-tr.example.com:1080
PROXY_GB=socks5://user:pass@proxy-gb.example.com:1080
PROXY_US=socks5://user:pass@proxy-us.example.com:1080
```

#### 3. Download Logic with Proxy Fallback

```python
# In src/activities/download.py

import os
import httpx

def get_proxy_for_region(region: str) -> Optional[str]:
    """Get proxy URL for a specific region."""
    return os.getenv(f"PROXY_{region}")

async def download_with_proxy_fallback(
    cdn_url: str,
    username: str,
    headers: dict
) -> Tuple[bytes, bool]:  # (content, used_proxy)
    """
    Try direct download first, fallback to proxy on 403.
    
    Returns:
        Tuple of (video_bytes, used_proxy_flag)
    """
    # First attempt: Direct
    async with httpx.AsyncClient(timeout=60.0) as client:
        response = await client.get(cdn_url, headers=headers, follow_redirects=True)
        
        if response.status_code == 200:
            return response.content, False
        
        if response.status_code != 403:
            response.raise_for_status()
    
    # Got 403 - try proxy
    region = get_region_for_account(username)
    proxy_url = get_proxy_for_region(region)
    
    if not proxy_url:
        activity.logger.warning(
            f"⚠️ No proxy configured for region={region} | account={username}"
        )
        raise Exception(f"CDN 403 and no proxy for region {region}")
    
    activity.logger.info(
        f"🌐 Retrying with proxy | region={region} | account={username}"
    )
    
    async with httpx.AsyncClient(
        timeout=60.0,
        proxy=proxy_url
    ) as client:
        response = await client.get(cdn_url, headers=headers, follow_redirects=True)
        response.raise_for_status()
        return response.content, True
```

#### 4. Proxy Service Options

**Option A: Commercial Proxy Service (Recommended)**

| Service | Coverage | Cost | Notes |
|---------|----------|------|-------|
| BrightData | 195 countries | ~$10-15/GB | Residential IPs, low block rate |
| Oxylabs | 195 countries | ~$10-15/GB | Good for streaming |
| Smartproxy | 195 countries | ~$7-10/GB | Budget option |

**Option B: VPS-based Proxies (Self-hosted)**

Deploy lightweight SOCKS5 proxies on cloud VPS in target regions:

```bash
# On each VPS (e.g., DigitalOcean, Vultr)
# Install microsocks
apt install microsocks
microsocks -i 0.0.0.0 -p 1080 -u proxyuser -P proxypass
```

VPS costs: ~$5/month per region

**Option C: SSH Tunnel Proxies**

If you have VPS in target regions, use SSH as SOCKS5 proxy:

```bash
# Create tunnel
ssh -D 1080 -f -N user@vps-argentina.example.com

# Use in Python
proxy = "socks5://localhost:1080"
```

---

## Determining Which Region to Use

### Strategy 1: Account-Based Mapping (Current Plan)

Pre-map known broadcaster accounts to their primary region. This is the simplest and most reliable approach for known accounts.

**Pros:**
- Simple, deterministic
- No trial-and-error overhead
- Can be maintained in code or config

**Cons:**
- Requires manual mapping for each account
- Unknown accounts need fallback

### Strategy 2: Region Detection via Error Analysis

Some CDNs return region hints in headers or error pages:

```python
async def detect_restricted_region(cdn_url: str) -> Optional[str]:
    """Try to detect which region is required from CDN response."""
    async with httpx.AsyncClient() as client:
        response = await client.get(cdn_url, follow_redirects=False)
        
        # Check for geo headers
        geo_hint = response.headers.get("X-Geo-Country")
        cf_country = response.headers.get("CF-IPCountry")
        
        if geo_hint:
            return geo_hint
        if cf_country:
            return cf_country
    
    return None
```

### Strategy 3: Waterfall Fallback

Try proxies in priority order until one works:

```python
PROXY_PRIORITY = ["US", "GB", "DE", "AR", "TR"]

async def download_with_waterfall(cdn_url: str, headers: dict) -> bytes:
    """Try proxies in priority order until success."""
    for region in PROXY_PRIORITY:
        proxy = get_proxy_for_region(region)
        if not proxy:
            continue
        
        try:
            async with httpx.AsyncClient(proxy=proxy, timeout=30.0) as client:
                response = await client.get(cdn_url, headers=headers)
                if response.status_code == 200:
                    return response.content
        except Exception:
            continue
    
    raise Exception("All proxy regions failed")
```

---

## Metrics and Monitoring

Track proxy usage for cost optimization:

```python
# Log when proxy is used
activity.logger.info(
    f"📊 Download complete | "
    f"used_proxy={used_proxy} | "
    f"region={region if used_proxy else 'direct'} | "
    f"account={username}"
)

# Store in MongoDB for analysis
{
    "_id": video_id,
    "download_method": "proxy" if used_proxy else "direct",
    "proxy_region": region,
    "broadcaster_account": username,
    "downloaded_at": datetime.utcnow()
}
```

---

## Cost Analysis

### Current State (No Proxy)
- ~20-30% of discovered videos fail due to geo-restrictions
- Zero cost, but missing content

### With Proxy (Estimated)

Assuming:
- 100 goal events/day with average 3 videos each = 300 videos
- 30% geo-restricted = 90 videos/day need proxy
- Average video size: 5MB

**Monthly proxy bandwidth**: 90 × 5MB × 30 days = ~13.5 GB/month

| Option | Monthly Cost |
|--------|--------------|
| Commercial proxy (13.5 GB × $10/GB) | ~$135/month |
| Self-hosted VPS (5 regions × $5) | ~$25/month |
| SSH tunnels (if existing VPS) | $0 |

---

## Implementation Priority

### Phase 1: Quick Win (Low Effort)
1. Add account-to-region mapping for known broadcasters
2. Log which accounts are failing for better mapping

### Phase 2: Self-Hosted Proxies (Medium Effort)
1. Deploy VPS in Argentina, Turkey, UK
2. Set up SOCKS5 proxy on each
3. Add proxy fallback to download activity

### Phase 3: Commercial Proxy (If Needed)
1. Sign up for BrightData or similar
2. Use their API for on-demand regional proxies
3. Track costs and optimize regions

---

## Current Workaround

Until proxy infrastructure is implemented:

1. **Accept missing videos** from geo-restricted broadcasters
2. **Prioritize non-geo-restricted content** - most fan uploads and highlight accounts work fine
3. **Monitor which accounts fail** to build the account-region mapping

The current implementation logs all CDN 403 failures with the account username, making it easy to identify patterns.
