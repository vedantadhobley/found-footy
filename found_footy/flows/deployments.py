import argparse
import sys
from found_footy.flows.fixtures_flow import fixtures_flow
from found_footy.flows.youtube_flow import youtube_flow

def create_deployments():
    print("🚀 Creating deployments...")
    
    # Scheduled deployment - uses fixtures-pool
    fixtures_scheduled = fixtures_flow.to_deployment(
        name="fixtures-daily",
        work_pool_name="fixtures-pool",
        schedule={"cron": "5 0 * * *"},
        paused=True,
        description="Daily fixtures processing at 00:05 UTC - Enable from UI when ready"
    )
    
    # Manual deployment - uses fixtures-pool
    fixtures_manual = fixtures_flow.to_deployment(
        name="fixtures-manual",
        work_pool_name="fixtures-pool",
        description="Manual fixtures processing for any date - Trigger from UI"
    )
    
    # YouTube deployment - uses youtube-pool
    youtube_deployment = youtube_flow.to_deployment(
        name="youtube-highlights",
        work_pool_name="youtube-pool",
        description="YouTube highlights processing - Triggered by fixture completion events"
    )
    
    try:
        fixtures_scheduled.apply()
        fixtures_manual.apply() 
        youtube_deployment.apply()
        print("✅ Deployments created successfully!")
        print("📅 'fixtures-daily' - PAUSED daily run (enable from UI)")
        print("🧪 'fixtures-manual' - manual trigger anytime")
        print("🎬 'youtube-highlights' - auto-triggered by events")
    except Exception as e:
        print(f"❌ Error creating deployments: {e}")
        raise

def run_immediate():
    """Run the fixtures flow immediately for today's date"""
    print("🏃 Running fixtures flow immediately for today...")
    print("🔍 About to call fixtures_flow()...")
    try:
        result = fixtures_flow()
        print(f"✅ Immediate run completed successfully: {result}")
    except Exception as e:
        print(f"❌ Immediate run failed: {e}")
        import traceback
        traceback.print_exc()

if __name__ == "__main__":
    print(f"🐛 DEBUG: Command line args: {sys.argv}")
    
    parser = argparse.ArgumentParser()
    parser.add_argument("--apply", action="store_true", help="Apply all deployments")
    parser.add_argument("--run-now", action="store_true", help="Also run immediately")
    args = parser.parse_args()
    
    print(f"🐛 DEBUG: Parsed args - apply: {args.apply}, run_now: {args.run_now}")
    
    if args.apply:
        print("📋 Creating deployments...")
        create_deployments()
        
        if args.run_now:
            print("🚨 DEBUG: run_now flag is True, calling run_immediate()")
            run_immediate()
        else:
            print("⚠️ DEBUG: run_now flag is False, skipping immediate run")
        
        print("✅ Setup complete!")
        print("🌐 Access Prefect UI at http://localhost:4200")
        print("🎛️ Enable 'fixtures-daily' schedule from the UI when ready")
    else:
        print("❌ DEBUG: apply flag is False")
        print("Use --apply to create deployments")
        print("Use --apply --run-now to also run immediately")