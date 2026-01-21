"""
Dynamic Scaler Service

Monitors application load and scales Twitter browser instances and workers dynamically.

Scaling Rules (unified for both Twitter and Workers):
- Scale up when: active_goals > instances * 3
- Scale down when: active_goals < instances * 2 AND instance is not busy
- Min: 2, Max: 8

The scaler runs as a background service, checking metrics every 30 seconds.
Uses docker compose commands for clean container management.
"""

import asyncio
import os
import requests
import subprocess
import time
from datetime import datetime, timezone

# Compose project directory (where docker-compose.yml is)
# When running in container, use mounted workspace; otherwise use local path
COMPOSE_DIR = os.getenv("COMPOSE_DIR", "/home/vedanta/workspace/dev/found-footy")
PROJECT_NAME = "found-footy-prod"

class ScalerService:
    """Manages dynamic scaling of Twitter browsers and workers."""
    
    def __init__(self):
        # Scaling configuration
        self.twitter_min = int(os.getenv("MIN_TWITTER_INSTANCES", "2"))
        self.twitter_max = int(os.getenv("MAX_TWITTER_INSTANCES", "8"))
        self.worker_min = int(os.getenv("MIN_WORKER_INSTANCES", "2"))
        self.worker_max = int(os.getenv("MAX_WORKER_INSTANCES", "8"))
        self.check_interval = int(os.getenv("SCALE_CHECK_INTERVAL", "30"))
        
        # Use workspace mount if running in container
        self.compose_file = os.getenv("COMPOSE_DIR", COMPOSE_DIR) + "/docker-compose.yml"
        if os.path.exists("/workspace/docker-compose.yml"):
            self.compose_file = "/workspace/docker-compose.yml"
        
        # Cooldown to prevent thrashing (seconds)
        self.scale_cooldown = 60  # 1 minute between scaling actions
        self.last_twitter_scale = 0
        self.last_worker_scale = 0
        
        # MongoDB connection for metrics
        self.mongo_store = None
        
    def _get_mongo_store(self):
        """Lazy-load MongoDB store."""
        if self.mongo_store is None:
            from src.data.mongo_store import FootyMongoStore
            self.mongo_store = FootyMongoStore()
        return self.mongo_store
    
    def _run_compose(self, *args) -> tuple[bool, str]:
        """Run a docker compose command."""
        cmd = ["docker", "compose", "-f", self.compose_file, "--profile", "scale", *args]
        print(f"🔧 Running: {' '.join(cmd)}")
        try:
            result = subprocess.run(cmd, capture_output=True, text=True, timeout=120)
            return result.returncode == 0, result.stdout + result.stderr
        except subprocess.TimeoutExpired:
            return False, "Command timed out"
        except Exception as e:
            return False, str(e)

    def get_pending_goals_count(self) -> int:
        """Get count of goals still being processed (not _twitter_complete)."""
        try:
            store = self._get_mongo_store()
            pipeline = [
                {"$unwind": "$events"},
                {"$match": {
                    "events.type": "Goal",
                    "events._twitter_complete": {"$ne": True}
                }},
                {"$count": "pending"}
            ]
            result = list(store.fixtures_active.aggregate(pipeline))
            return result[0]["pending"] if result else 0
        except Exception as e:
            print(f"❌ Error getting pending goals: {e}")
            return 0

    def get_active_twitter_workflows_count(self) -> int:
        """Get count of currently running Twitter workflows from active fixtures."""
        try:
            store = self._get_mongo_store()
            # Count goals that are being searched (have _twitter_count but not complete)
            pipeline = [
                {"$unwind": "$events"},
                {"$match": {
                    "events.type": "Goal",
                    "events._twitter_count": {"$exists": True, "$gt": 0},
                    "events._twitter_complete": {"$ne": True}
                }},
                {"$count": "active"}
            ]
            result = list(store.fixtures_active.aggregate(pipeline))
            return result[0]["active"] if result else 0
        except Exception as e:
            print(f"❌ Error getting active workflows: {e}")
            return 0
    
    def get_running_containers(self, service_prefix: str) -> list[str]:
        """Get list of running container names matching prefix."""
        cmd = ["docker", "ps", "--filter", f"name={PROJECT_NAME}-{service_prefix}",
               "--format", "{{.Names}}"]
        try:
            result = subprocess.run(cmd, capture_output=True, text=True, timeout=10)
            if result.returncode == 0:
                return [n.strip() for n in result.stdout.strip().split('\n') if n.strip()]
            return []
        except Exception as e:
            print(f"❌ Error listing containers: {e}")
            return []
    
    def get_running_twitter_count(self) -> int:
        """Get count of running Twitter instances."""
        containers = self.get_running_containers("twitter")
        return len(containers)
    
    def get_running_worker_count(self) -> int:
        """Get count of running worker instances."""
        containers = self.get_running_containers("worker")
        return len(containers)
    
    def is_twitter_instance_busy(self, instance_num: int) -> bool:
        """Check if a Twitter instance is currently processing a search."""
        import requests
        url = f"http://{PROJECT_NAME}-twitter-{instance_num}:8888/status"
        try:
            resp = requests.get(url, timeout=2)
            if resp.status_code == 200:
                data = resp.json()
                return data.get("busy", False)
        except:
            pass
        return False  # If we can't reach it, assume not busy
    
    def find_idle_twitter_instance(self, start_from: int) -> int:
        """Find the highest-numbered idle Twitter instance >= start_from.
        
        Returns instance number or 0 if none found idle.
        """
        # Check from highest to start_from
        for i in range(start_from, self.twitter_min, -1):
            if not self.is_twitter_instance_busy(i):
                return i
        return 0  # None found idle
    
    def start_service(self, service_name: str) -> bool:
        """Start a service using docker compose up."""
        print(f"🚀 Starting {service_name}...")
        success, output = self._run_compose("up", "-d", service_name)
        if success:
            print(f"✅ Started {service_name}")
        else:
            print(f"❌ Failed to start {service_name}: {output}")
        return success
    
    def stop_service(self, service_name: str) -> bool:
        """Stop a service using docker compose stop."""
        print(f"🛑 Stopping {service_name}...")
        success, output = self._run_compose("stop", service_name)
        if success:
            print(f"✅ Stopped {service_name}")
        else:
            print(f"❌ Failed to stop {service_name}: {output}")
        return success

    def scale_twitter(self, target_count: int) -> bool:
        """Scale Twitter instances to target count."""
        target_count = max(self.twitter_min, min(self.twitter_max, target_count))
        current_count = self.get_running_twitter_count()
        
        if current_count == target_count:
            return True
        
        # Check cooldown
        if time.time() - self.last_twitter_scale < self.scale_cooldown:
            print(f"⏳ Twitter scaling in cooldown ({self.scale_cooldown}s), skipping")
            return False
        
        print(f"📊 Scaling Twitter: {current_count} → {target_count}")
        
        if target_count > current_count:
            # Scale up - start next instances
            for i in range(current_count + 1, target_count + 1):
                self.start_service(f"twitter-{i}")
        else:
            # Scale down - stop highest numbered IDLE instances first
            stopped = 0
            for i in range(current_count, target_count, -1):
                if self.is_twitter_instance_busy(i):
                    print(f"⏳ twitter-{i} is busy, skipping scale-down")
                    continue
                self.stop_service(f"twitter-{i}")
                stopped += 1
            if stopped == 0:
                print(f"⚠️ All instances busy, cannot scale down yet")
                return False
        
        self.last_twitter_scale = time.time()
        return True

    def scale_workers(self, target_count: int) -> bool:
        """Scale worker instances to target count."""
        target_count = max(self.worker_min, min(self.worker_max, target_count))
        current_count = self.get_running_worker_count()
        
        if current_count == target_count:
            return True
        
        # Check cooldown
        if time.time() - self.last_worker_scale < self.scale_cooldown:
            print(f"⏳ Worker scaling in cooldown ({self.scale_cooldown}s), skipping")
            return False
        
        print(f"📊 Scaling Workers: {current_count} → {target_count}")
        
        if target_count > current_count:
            # Scale up
            for i in range(current_count + 1, target_count + 1):
                self.start_service(f"worker-{i}")
        else:
            # Scale down
            for i in range(current_count, target_count, -1):
                self.stop_service(f"worker-{i}")
        
        self.last_worker_scale = time.time()
        return True

    def calculate_target_twitter_instances(self) -> int:
        """Calculate target number of Twitter instances based on load.
        
        Logic: 1 browser per 3 active goals (each browser does ~8 searches/min)
        - Scale up when: active_goals > instances * 3
        - Scale down when: active_goals < instances * 2 (with some buffer)
        """
        pending_goals = self.get_pending_goals_count()
        active_searches = self.get_active_twitter_workflows_count()
        current = self.get_running_twitter_count()
        
        # Use whichever is higher - pending or actively searching
        load = max(pending_goals, active_searches)
        
        print(f"📊 Twitter metrics: pending={pending_goals}, active={active_searches}, load={load}, instances={current}")
        
        # Target: 1 instance per 3 concurrent goals
        # Scale up threshold: load > current * 3
        # Scale down threshold: load < current * 2
        if load > current * 3:
            # Need more browsers
            target = (load + 2) // 3  # Round up
            return min(self.twitter_max, max(target, current + 1))
        elif load < current * 2 and current > self.twitter_min:
            # Can reduce browsers
            return current - 1
        
        return current

    def calculate_target_worker_instances(self) -> int:
        """Calculate target number of worker instances based on load.
        
        Logic: 1 worker per 3 active goals (similar to Twitter)
        - Scale up when: active_goals > workers * 3
        - Scale down when: active_goals < workers * 2
        """
        pending_goals = self.get_pending_goals_count()
        active_searches = self.get_active_twitter_workflows_count()
        current = self.get_running_worker_count()
        
        # Use whichever is higher
        load = max(pending_goals, active_searches)
        
        print(f"📊 Worker metrics: pending={pending_goals}, active={active_searches}, load={load}, workers={current}")
        
        # Same logic as Twitter
        if load > current * 3:
            target = (load + 2) // 3
            return min(self.worker_max, max(target, current + 1))
        elif load < current * 2 and current > self.worker_min:
            return current - 1
        
        return current

    async def run_scaling_loop(self):
        """Main scaling loop - checks metrics and scales every interval seconds."""
        print(f"🚀 Scaler service started")
        print(f"   Twitter: min={self.twitter_min}, max={self.twitter_max}")
        print(f"   Workers: min={self.worker_min}, max={self.worker_max}")
        print(f"   Check interval: {self.check_interval}s")
        print(f"   Cooldown: {self.scale_cooldown}s")
        
        # Ensure minimum instances are running on startup
        print(f"\n🔧 Ensuring minimum instances are running...")
        current_twitter = self.get_running_twitter_count()
        current_workers = self.get_running_worker_count()
        
        if current_twitter < self.twitter_min:
            for i in range(1, self.twitter_min + 1):
                self.start_service(f"twitter-{i}")
        
        if current_workers < self.worker_min:
            for i in range(1, self.worker_min + 1):
                self.start_service(f"worker-{i}")
        
        print(f"\n📊 Starting monitoring loop...")
        
        while True:
            try:
                print(f"\n{'='*60}")
                print(f"⏰ {datetime.now().strftime('%H:%M:%S')} - Checking scaling metrics")
                
                # Scale Twitter instances
                target_twitter = self.calculate_target_twitter_instances()
                self.scale_twitter(target_twitter)
                
                # Scale workers
                target_workers = self.calculate_target_worker_instances()
                self.scale_workers(target_workers)
                
            except Exception as e:
                print(f"❌ Scaler error: {e}")
                import traceback
                traceback.print_exc()
            
            await asyncio.sleep(self.check_interval)


def get_healthy_twitter_urls() -> list[str]:
    """
    Get list of healthy Twitter instance URLs.
    Used by workers to discover available Twitter browsers.
    """
    import requests
    
    all_urls = [
        f"http://{PROJECT_NAME}-twitter-{i}:8888"
        for i in range(1, 9)  # twitter-1 through twitter-8
    ]
    
    healthy = []
    for url in all_urls:
        try:
            resp = requests.get(f"{url}/health", timeout=2)
            if resp.status_code == 200:
                healthy.append(url)
        except:
            pass
    
    return healthy if healthy else all_urls[:2]  # Fallback to first 2


async def main():
    """Run the scaler service."""
    scaler = ScalerService()
    await scaler.run_scaling_loop()


if __name__ == "__main__":
    asyncio.run(main())
