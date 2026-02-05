"""
Twitter Instance Registry

Simple service discovery for Twitter browser instances.
- Twitter instances register themselves on startup with a heartbeat
- Workers query the registry to get available instances
- Load balancing via round-robin selection
"""

import os
import random
import time
from datetime import datetime, timezone
from typing import Optional, List
import threading

from src.scaler.scaler_logging import ScalerLogger

MODULE = "registry"
log = ScalerLogger()


class TwitterRegistry:
    """
    Simple registry for Twitter browser instances.
    Uses MongoDB as the backing store for distributed access.
    """
    
    _instance = None
    _lock = threading.Lock()
    
    def __new__(cls):
        """Singleton pattern for registry."""
        if cls._instance is None:
            with cls._lock:
                if cls._instance is None:
                    cls._instance = super().__new__(cls)
                    cls._instance._initialized = False
        return cls._instance
    
    def __init__(self):
        if self._initialized:
            return
            
        self._initialized = True
        self._mongo_store = None
        self._round_robin_index = 0
        self._local_cache = []
        self._cache_time = 0
        self._cache_ttl = 5  # Cache for 5 seconds
        
        # Default instance for backward compatibility
        self._default_url = os.getenv("TWITTER_SESSION_URL", "http://found-footy-prod-twitter:8888")
    
    def _get_store(self):
        """Lazy-load MongoDB store."""
        if self._mongo_store is None:
            from src.data.mongo_store import FootyMongoStore
            self._mongo_store = FootyMongoStore()
        return self._mongo_store
    
    def register(self, instance_id: str, url: str) -> bool:
        """
        Register a Twitter instance as available.
        Called by Twitter service on startup and periodically as heartbeat.
        """
        try:
            store = self._get_store()
            store.db.twitter_instances.update_one(
                {"instance_id": instance_id},
                {
                    "$set": {
                        "instance_id": instance_id,
                        "url": url,
                        "last_heartbeat": datetime.now(timezone.utc),
                        "status": "available",
                        "registered_at": datetime.now(timezone.utc),
                    }
                },
                upsert=True
            )
            log.info(MODULE, "instance_registered", f"Registered Twitter instance: {instance_id} at {url}", instance_id=instance_id, url=url)
            return True
        except Exception as e:
            log.error(MODULE, "register_failed", f"Failed to register Twitter instance: {instance_id}", error=str(e), instance_id=instance_id)
            return False
    
    def unregister(self, instance_id: str) -> bool:
        """Mark a Twitter instance as unavailable."""
        try:
            store = self._get_store()
            store.db.twitter_instances.update_one(
                {"instance_id": instance_id},
                {"$set": {"status": "unavailable"}}
            )
            log.info(MODULE, "instance_unregistered", f"Unregistered Twitter instance: {instance_id}", instance_id=instance_id)
            return True
        except Exception as e:
            log.error(MODULE, "unregister_failed", f"Failed to unregister Twitter instance: {instance_id}", error=str(e), instance_id=instance_id)
            return False
    
    def heartbeat(self, instance_id: str) -> bool:
        """Update heartbeat for an instance."""
        try:
            store = self._get_store()
            store.db.twitter_instances.update_one(
                {"instance_id": instance_id},
                {"$set": {"last_heartbeat": datetime.now(timezone.utc)}}
            )
            return True
        except Exception as e:
            log.error(MODULE, "heartbeat_failed", f"Heartbeat failed for {instance_id}", error=str(e), instance_id=instance_id)
            return False
    
    def get_available_instances(self, max_age_seconds: int = 30) -> List[str]:
        """
        Get list of available Twitter instance URLs.
        Only returns instances with heartbeat within max_age_seconds.
        """
        # Check cache first
        now = time.time()
        if now - self._cache_time < self._cache_ttl and self._local_cache:
            return self._local_cache
        
        try:
            store = self._get_store()
            cutoff = datetime.now(timezone.utc).timestamp() - max_age_seconds
            cutoff_dt = datetime.fromtimestamp(cutoff, timezone.utc)
            
            instances = store.db.twitter_instances.find({
                "status": "available",
                "last_heartbeat": {"$gte": cutoff_dt}
            })
            
            urls = [i["url"] for i in instances]
            
            # Log if instance count changed (cache refresh with change)
            if self._local_cache and len(urls) != len(self._local_cache):
                log.info(MODULE, "instance_cache_refreshed", "Twitter instance cache refreshed",
                         previous_count=len(self._local_cache), new_count=len(urls))
            
            # Update cache
            self._local_cache = urls
            self._cache_time = now
            
            return urls
            
        except Exception as e:
            log.error(MODULE, "get_instances_failed", "Failed to get Twitter instances", error=str(e))
            # Return cached or default on error
            if self._local_cache:
                return self._local_cache
            return [self._default_url]
    
    def get_instance_url(self, strategy: str = "round_robin") -> str:
        """
        Get a Twitter instance URL using the specified strategy.
        
        Strategies:
        - round_robin: Cycle through instances in order
        - random: Random selection
        - first: Always return first available
        """
        instances = self.get_available_instances()
        
        if not instances:
            log.warning(MODULE, "no_instances_available", f"No Twitter instances available, using default", default_url=self._default_url)
            return self._default_url
        
        if len(instances) == 1:
            return instances[0]
        
        if strategy == "round_robin":
            with self._lock:
                url = instances[self._round_robin_index % len(instances)]
                self._round_robin_index += 1
            return url
        elif strategy == "random":
            return random.choice(instances)
        else:  # first
            return instances[0]
    
    def mark_instance_busy(self, instance_id: str):
        """Mark an instance as busy (optional - for smarter load balancing)."""
        try:
            store = self._get_store()
            store.db.twitter_instances.update_one(
                {"instance_id": instance_id},
                {"$set": {"status": "busy"}}
            )
        except Exception:
            pass
    
    def mark_instance_available(self, instance_id: str):
        """Mark an instance as available after completing work."""
        try:
            store = self._get_store()
            store.db.twitter_instances.update_one(
                {"instance_id": instance_id},
                {"$set": {"status": "available"}}
            )
        except Exception:
            pass
    
    def get_stats(self) -> dict:
        """Get registry statistics."""
        try:
            store = self._get_store()
            total = store.db.twitter_instances.count_documents({})
            available = store.db.twitter_instances.count_documents({"status": "available"})
            
            cutoff = datetime.now(timezone.utc).timestamp() - 30
            cutoff_dt = datetime.fromtimestamp(cutoff, timezone.utc)
            healthy = store.db.twitter_instances.count_documents({
                "status": "available",
                "last_heartbeat": {"$gte": cutoff_dt}
            })
            
            return {
                "total_registered": total,
                "available": available,
                "healthy": healthy,
            }
        except Exception as e:
            return {"error": str(e)}


# Global singleton instance
registry = TwitterRegistry()


def get_twitter_url() -> str:
    """Convenience function to get a Twitter instance URL."""
    return registry.get_instance_url()
