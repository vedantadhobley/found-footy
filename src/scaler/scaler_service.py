"""
Dynamic Scaler Service

Monitors Temporal task queue depth and scales workers/Twitter instances accordingly.

Scaling Logic:
- Workers: Scale based on Temporal task queue backlog (pending workflow/activity tasks)
- Twitter: Scale based on ACTIVE GOALS being searched (from MongoDB)
  - Each goal runs 10 search attempts over ~10 minutes
  - 1 Twitter instance can handle ~2-3 concurrent goals efficiently
  - Scale up when: active_goals > instances * 2
  - Scale down when: active_goals < instances (with cooldown)
- Min: 2, Max: 8 for both workers and Twitter

Uses --scale flag for clean Docker Compose scaling.
"""

import asyncio
import os
import sys
import time
from datetime import datetime, timezone

import requests
from pymongo import MongoClient
from python_on_whales import DockerClient
from temporalio.client import Client

# Force unbuffered output
sys.stdout = os.fdopen(sys.stdout.fileno(), 'w', buffering=1)
sys.stderr = os.fdopen(sys.stderr.fileno(), 'w', buffering=1)

# Configuration
PROJECT_NAME = "found-footy-prod"
COMPOSE_FILE = os.getenv("COMPOSE_FILE", "/workspace/docker-compose.yml")
TEMPORAL_HOST = os.getenv("TEMPORAL_HOST", "found-footy-prod-temporal:7233")
MONGODB_URI = os.getenv("MONGODB_URI", "mongodb://ffuser:ffpass@found-footy-prod-mongo:27017/found_footy?authSource=admin")
TASK_QUEUE = "found-footy"

# Scaling thresholds
SCALE_UP_THRESHOLD = 5      # Workers: Scale up when > 5 active workflows per worker
SCALE_DOWN_THRESHOLD = 2    # Workers: Scale down when < 2 active workflows per worker
TWITTER_GOALS_PER_INSTANCE = 2  # Twitter: 2 goals per instance (searches are fast with OR syntax)
MIN_INSTANCES = 2
MAX_INSTANCES = 8
CHECK_INTERVAL = 30         # seconds
SCALE_COOLDOWN = 60         # seconds between scaling actions


class TemporalMetrics:
    """Fetch metrics from Temporal task queue."""
    
    def __init__(self, client: Client):
        self.client = client
    
    async def get_task_queue_depth(self) -> dict:
        """
        Get pending task counts from Temporal task queue.
        
        Returns dict with workflow_tasks, activity_tasks, and poller counts.
        """
        try:
            from temporalio.api.enums.v1 import TaskQueueType
            from temporalio.api.taskqueue.v1 import TaskQueue
            from temporalio.api.workflowservice.v1 import DescribeTaskQueueRequest
            
            # Query workflow task queue
            workflow_req = DescribeTaskQueueRequest(
                namespace="default",
                task_queue=TaskQueue(name=TASK_QUEUE),
                task_queue_type=TaskQueueType.TASK_QUEUE_TYPE_WORKFLOW,
            )
            workflow_resp = await self.client.workflow_service.describe_task_queue(workflow_req)
            
            workflow_backlog = 0
            if hasattr(workflow_resp, 'task_queue_status') and workflow_resp.task_queue_status:
                workflow_backlog = workflow_resp.task_queue_status.backlog_count_hint
            
            # Query activity task queue
            activity_req = DescribeTaskQueueRequest(
                namespace="default",
                task_queue=TaskQueue(name=TASK_QUEUE),
                task_queue_type=TaskQueueType.TASK_QUEUE_TYPE_ACTIVITY,
            )
            activity_resp = await self.client.workflow_service.describe_task_queue(activity_req)
            
            activity_backlog = 0
            if hasattr(activity_resp, 'task_queue_status') and activity_resp.task_queue_status:
                activity_backlog = activity_resp.task_queue_status.backlog_count_hint
            
            return {
                "workflow_tasks": workflow_backlog,
                "activity_tasks": activity_backlog,
                "total": workflow_backlog + activity_backlog,
                "workflow_pollers": len(workflow_resp.pollers) if workflow_resp.pollers else 0,
                "activity_pollers": len(activity_resp.pollers) if activity_resp.pollers else 0,
            }
        except Exception as e:
            print(f"❌ Error getting task queue metrics: {e}")
            return {"workflow_tasks": 0, "activity_tasks": 0, "total": 0, 
                    "workflow_pollers": 0, "activity_pollers": 0}

    async def get_active_workflow_count(self) -> int:
        """
        Count running workflows in the default namespace.
        
        Uses ListWorkflowExecutions with RUNNING status filter.
        Returns count of active (running) workflows.
        """
        try:
            from temporalio.api.workflowservice.v1 import ListWorkflowExecutionsRequest
            
            # Query for running workflows
            request = ListWorkflowExecutionsRequest(
                namespace="default",
                query="ExecutionStatus = 'Running'",
                page_size=100,  # We just need count, not all details
            )
            
            count = 0
            while True:
                response = await self.client.workflow_service.list_workflow_executions(request)
                count += len(response.executions)
                
                # Check for more pages
                if response.next_page_token:
                    request.next_page_token = response.next_page_token
                else:
                    break
            
            return count
        except Exception as e:
            print(f"❌ Error counting active workflows: {e}")
            return 0


class MongoMetrics:
    """Fetch metrics from MongoDB for scaling decisions."""
    
    def __init__(self, uri: str = MONGODB_URI):
        self.client = MongoClient(uri)
        self.db = self.client.get_database()
    
    def get_active_twitter_goals(self) -> int:
        """
        Get count of goal events actively being searched on Twitter.
        
        Events are embedded in fixtures as an 'events' array.
        Active goal = _monitor_complete=true AND _twitter_complete not true
        These are goals in the Twitter search pipeline (10 attempts over ~10 minutes each).
        """
        try:
            # Events are embedded in fixtures - need aggregation to count them
            pipeline = [
                {"$unwind": "$events"},
                {"$match": {
                    "events._monitor_complete": True,
                    "$or": [
                        {"events._twitter_complete": {"$exists": False}},
                        {"events._twitter_complete": False},
                        {"events._twitter_complete": None}
                    ]
                }},
                {"$count": "active_goals"}
            ]
            # Check both active and live fixtures
            total = 0
            for collection in ["fixtures_active", "fixtures_live"]:
                result = list(self.db[collection].aggregate(pipeline))
                if result:
                    total += result[0].get("active_goals", 0)
            return total
        except Exception as e:
            print(f"❌ Error getting active Twitter goals: {e}")
            return 0
    
    def get_goals_summary(self) -> dict:
        """Get summary of goal events in various states across all fixture collections."""
        try:
            total = 0
            monitor_complete = 0
            twitter_complete = 0
            upload_complete = 0
            
            for collection in ["fixtures_active", "fixtures_live", "fixtures_completed"]:
                pipeline = [
                    {"$unwind": "$events"},
                    {"$group": {
                        "_id": None,
                        "total": {"$sum": 1},
                        "monitor_complete": {
                            "$sum": {"$cond": [{"$eq": ["$events._monitor_complete", True]}, 1, 0]}
                        },
                        "twitter_complete": {
                            "$sum": {"$cond": [{"$eq": ["$events._twitter_complete", True]}, 1, 0]}
                        },
                        "upload_complete": {
                            "$sum": {"$cond": [{"$eq": ["$events._upload_complete", True]}, 1, 0]}
                        },
                    }}
                ]
                result = list(self.db[collection].aggregate(pipeline))
                if result:
                    total += result[0].get("total", 0)
                    monitor_complete += result[0].get("monitor_complete", 0)
                    twitter_complete += result[0].get("twitter_complete", 0)
                    upload_complete += result[0].get("upload_complete", 0)
            
            return {
                "total": total,
                "monitor_complete": monitor_complete,
                "twitter_complete": twitter_complete,
                "upload_complete": upload_complete
            }
        except Exception as e:
            print(f"❌ Error getting goals summary: {e}")
            return {"total": 0, "monitor_complete": 0, "twitter_complete": 0, "upload_complete": 0}


class ScalerService:
    """Manages dynamic scaling of workers and Twitter instances using --scale."""
    
    def __init__(self, docker: DockerClient, temporal_client: Client):
        self.docker = docker
        self.temporal = TemporalMetrics(temporal_client)
        self.mongo = MongoMetrics()
        
        # Scaling config from env
        self.min_instances = int(os.getenv("MIN_INSTANCES", MIN_INSTANCES))
        self.max_instances = int(os.getenv("MAX_INSTANCES", MAX_INSTANCES))
        self.check_interval = int(os.getenv("CHECK_INTERVAL", CHECK_INTERVAL))
        self.scale_cooldown = int(os.getenv("SCALE_COOLDOWN", SCALE_COOLDOWN))
        self.scale_up_threshold = int(os.getenv("SCALE_UP_THRESHOLD", SCALE_UP_THRESHOLD))
        self.scale_down_threshold = int(os.getenv("SCALE_DOWN_THRESHOLD", SCALE_DOWN_THRESHOLD))
        self.twitter_goals_per_instance = int(os.getenv("TWITTER_GOALS_PER_INSTANCE", TWITTER_GOALS_PER_INSTANCE))
        
        # Cooldown tracking
        self.last_worker_scale = 0
        self.last_twitter_scale = 0
    
    def get_running_services(self, service_name: str) -> list[str]:
        """Get list of running containers for a scaled service."""
        try:
            # With --scale, containers are named: found-footy-prod-twitter-1, found-footy-prod-twitter-2, etc.
            containers = self.docker.ps(filters={"name": f"{PROJECT_NAME}-{service_name}-"})
            return [c.name for c in containers if c.state.running]
        except Exception as e:
            print(f"❌ Error listing {service_name} containers: {e}")
            return []
    
    def get_running_count(self, service_name: str) -> int:
        """Get count of running instances for a service."""
        return len(self.get_running_services(service_name))
    
    def scale_service(self, service_name: str, target: int) -> bool:
        """
        Scale a service to target count using docker compose --scale.
        
        This is idempotent - if already at target, it's a no-op.
        """
        current = self.get_running_count(service_name)
        if current == target:
            return True
        
        try:
            print(f"🔄 Scaling {service_name}: {current} → {target}")
            # Use docker compose up with --scale
            self.docker.compose.up(
                [service_name],
                detach=True,
                scales={service_name: target}
            )
            print(f"✅ Scaled {service_name} to {target}")
            return True
        except Exception as e:
            print(f"❌ Failed to scale {service_name}: {e}")
            return False
    
    # Legacy compatibility - these now just call scale_service
    def scale_up(self, service_type: str, target: int) -> bool:
        """Scale up by starting additional instances."""
        # Convert old 'worker-' prefix to 'worker' service name
        service_name = service_type.rstrip('-')
        return self.scale_service(service_name, target)
    
    def scale_down(self, service_type: str, target: int) -> bool:
        """Scale down by stopping excess instances."""
        service_name = service_type.rstrip('-')
        return self.scale_service(service_name, target)
    
    def calculate_target_workers(self, active_workflows: int) -> int:
        """
        Calculate target worker count based on active workflow count.
        
        Logic:
        - workflows_per_worker = active_workflows / current_workers
        - Scale up if workflows_per_worker > SCALE_UP_THRESHOLD (5)
        - Scale down if workflows_per_worker < SCALE_DOWN_THRESHOLD (2)
        
        This is more accurate than queue depth because Temporal drains
        queues very fast, but active workflows represent actual work.
        """
        current = self.get_running_count("worker")
        if current == 0:
            return self.min_instances
        
        workflows_per_worker = active_workflows / current
        
        if workflows_per_worker > self.scale_up_threshold:
            # Need more workers - target enough so workflows_per_worker <= threshold
            target = max(current + 1, (active_workflows + self.scale_up_threshold - 1) // self.scale_up_threshold)
            return min(self.max_instances, target)
        elif workflows_per_worker < self.scale_down_threshold and current > self.min_instances:
            # Can reduce workers
            return current - 1
        
        return current
    
    def calculate_target_twitter(self, metrics: dict) -> int:
        """
        Calculate target Twitter instance count based on ACTIVE GOALS.
        
        Unlike workers (which scale on pending tasks), Twitter scales based on
        goals currently being searched. Each goal runs 10 search attempts over
        ~10 minutes, so we need to know how many goals are "in flight".
        
        Logic:
        - active_goals = goals with _monitor_complete=true AND _twitter_complete not true
        - Each Twitter instance can handle ~2-3 concurrent goals efficiently
        - Scale up if active_goals > instances * TWITTER_GOALS_PER_INSTANCE
        - Scale down if active_goals < instances (with cooldown to avoid flapping)
        
        This is more accurate than Temporal queue depth because Twitter searches
        complete quickly (~2-3s), so the queue is usually empty even when busy.
        """
        current = self.get_running_count("twitter")
        if current == 0:
            return self.min_instances
        
        # Get active goals from MongoDB (this is the real workload indicator)
        active_goals = self.mongo.get_active_twitter_goals()
        
        # Calculate goals per instance
        goals_per_instance = active_goals / current if current > 0 else active_goals
        
        if goals_per_instance > self.twitter_goals_per_instance:
            # Need more Twitter instances - scale to handle load
            target = max(current + 1, (active_goals + self.twitter_goals_per_instance - 1) // self.twitter_goals_per_instance)
            return min(self.max_instances, target)
        elif active_goals < current and current > self.min_instances:
            # More instances than goals - can scale down
            # Target: at least min_instances, but enough to handle current goals
            target = max(self.min_instances, active_goals)
            return target
        
        return current
    
    # Legacy compatibility methods
    def get_running_twitter_count(self) -> int:
        """Get count of running Twitter instances."""
        return self.get_running_count("twitter")
    
    def get_running_worker_count(self) -> int:
        """Get count of running worker instances."""
        return self.get_running_count("worker")
    
    def scale_twitter(self, target_count: int) -> bool:
        """Scale Twitter instances to target count (legacy interface)."""
        return self.scale_service("twitter", target_count)
    
    def scale_workers(self, target_count: int) -> bool:
        """Scale worker instances to target count (legacy interface)."""
        return self.scale_service("worker", target_count)
    async def run_scaling_loop(self):
        """Main scaling loop."""
        print(f"🚀 Scaler service started")
        print(f"   Task queue: {TASK_QUEUE}")
        print(f"   Instances: min={self.min_instances}, max={self.max_instances}")
        print(f"   Worker scale up threshold: {self.scale_up_threshold} workflows/worker")
        print(f"   Worker scale down threshold: {self.scale_down_threshold} workflows/worker")
        print(f"   Twitter goals per instance: {self.twitter_goals_per_instance}")
        print(f"   Check interval: {self.check_interval}s")
        print(f"   Cooldown: {self.scale_cooldown}s")
        
        # Ensure minimum instances on startup
        print(f"\n🔧 Ensuring minimum instances...")
        self.scale_service("worker", self.min_instances)
        self.scale_service("twitter", self.min_instances)
        
        print(f"\n📊 Starting monitoring loop...")
        
        while True:
            try:
                print(f"\n{'='*60}")
                print(f"⏰ {datetime.now().strftime('%H:%M:%S')} - Checking metrics")
                
                # Get Temporal metrics
                metrics = await self.temporal.get_task_queue_depth()
                active_workflows = await self.temporal.get_active_workflow_count()
                
                # Get MongoDB goal metrics
                active_goals = self.mongo.get_active_twitter_goals()
                goals_summary = self.mongo.get_goals_summary()
                
                current_workers = self.get_running_count("worker")
                current_twitter = self.get_running_count("twitter")
                
                print(f"📊 Active Workflows: {active_workflows} ({active_workflows/current_workers:.1f}/worker)")
                print(f"📊 Workers: {current_workers} running, {metrics['workflow_pollers']} polling")
                print(f"📊 Twitter: {current_twitter} running, {active_goals} active goals ({active_goals/current_twitter:.1f}/instance)")
                print(f"📊 Goals: total={goals_summary.get('total', 0)} monitor✓={goals_summary.get('monitor_complete', 0)} twitter✓={goals_summary.get('twitter_complete', 0)} upload✓={goals_summary.get('upload_complete', 0)}")
                
                # Calculate targets
                target_workers = self.calculate_target_workers(active_workflows)
                target_twitter = self.calculate_target_twitter(metrics)
                
                # Scale workers
                now = time.time()
                if target_workers != current_workers:
                    if now - self.last_worker_scale >= self.scale_cooldown:
                        print(f"📈 Workers: {current_workers} → {target_workers} (workflows={active_workflows})")
                        self.scale_service("worker", target_workers)
                        self.last_worker_scale = now
                    else:
                        remaining = int(self.scale_cooldown - (now - self.last_worker_scale))
                        print(f"⏳ Workers scaling in cooldown ({remaining}s remaining)")
                
                # Scale Twitter
                if target_twitter != current_twitter:
                    if now - self.last_twitter_scale >= self.scale_cooldown:
                        print(f"📈 Twitter: {current_twitter} → {target_twitter} (active_goals={active_goals})")
                        self.scale_service("twitter", target_twitter)
                        self.last_twitter_scale = now
                    else:
                        remaining = int(self.scale_cooldown - (now - self.last_twitter_scale))
                        print(f"⏳ Twitter scaling in cooldown ({remaining}s remaining)")
                
            except Exception as e:
                print(f"❌ Scaler error: {e}")
                import traceback
                traceback.print_exc()
            
            await asyncio.sleep(self.check_interval)
    
def get_healthy_twitter_urls() -> list[str]:
    """
    Get list of healthy Twitter instance URLs.
    Used by workers to discover available Twitter browsers.
    
    With --scale, containers are named:
    - found-footy-prod-twitter-1
    - found-footy-prod-twitter-2
    etc.
    """
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
    """Initialize and run the scaler."""
    print("🔌 Connecting to Temporal...")
    temporal_client = await Client.connect(TEMPORAL_HOST)
    print("✅ Connected to Temporal")
    
    print("🐳 Initializing Docker client...")
    docker = DockerClient(compose_files=[COMPOSE_FILE])
    print(f"✅ Docker client ready (compose file: {COMPOSE_FILE})")
    
    scaler = ScalerService(docker, temporal_client)
    
    await scaler.run_scaling_loop()


if __name__ == "__main__":
    asyncio.run(main())
