"""
Test that all activities used in workflows are registered in the worker.

This prevents the disaster where an activity is defined but not registered,
causing workflows to fail with "Activity function not registered" errors.
"""
import ast
import os
from pathlib import Path


def get_registered_activities() -> set[str]:
    """Parse worker.py to get list of registered activity names."""
    worker_path = Path(__file__).parent.parent / "src" / "worker.py"
    
    with open(worker_path) as f:
        content = f.read()
    
    # Find the activities=[ ... ] list in the Worker() call
    # Parse the module.function references
    registered = set()
    
    # Simple regex-based extraction (AST parsing is complex for this)
    import re
    
    # Find all module.function_name patterns in the activities list
    # Match patterns like: monitor.check_twitter_workflow_running
    activity_pattern = re.compile(r'(\w+)\.(\w+)')
    
    # Find the activities=[ section
    in_activities = False
    bracket_count = 0
    
    for line in content.split('\n'):
        if 'activities=[' in line:
            in_activities = True
            bracket_count = line.count('[') - line.count(']')
            continue
        
        if in_activities:
            bracket_count += line.count('[') - line.count(']')
            
            # Extract activity references
            matches = activity_pattern.findall(line)
            for module, func in matches:
                if module in ('ingest', 'monitor', 'rag', 'twitter', 'download', 'upload'):
                    registered.add(f"{module}.{func}")
            
            if bracket_count <= 0:
                break
    
    return registered


def get_activities_used_in_workflows() -> dict[str, set[str]]:
    """Parse workflow files to find all execute_activity calls."""
    workflows_dir = Path(__file__).parent.parent / "src" / "workflows"
    
    used_activities = {}
    
    for workflow_file in workflows_dir.glob("*.py"):
        if workflow_file.name == "__init__.py":
            continue
        
        with open(workflow_file) as f:
            content = f.read()
        
        # Find all execute_activity calls
        # Pattern: execute_activity(module.function_name, ...) or 
        #          execute_activity(\n    module.function_name, ...)
        import re
        
        # Match execute_activity calls with the activity reference
        pattern = re.compile(r'execute_activity\s*\(\s*(\w+)\.(\w+)')
        
        activities = set()
        for match in pattern.finditer(content):
            module, func = match.groups()
            if module.endswith('_activities'):
                # Convert monitor_activities -> monitor
                module = module.replace('_activities', '')
            activities.add(f"{module}.{func}")
        
        if activities:
            used_activities[workflow_file.name] = activities
    
    return used_activities


def test_all_activities_registered():
    """
    CRITICAL TEST: Ensure all activities used in workflows are registered.
    
    This catches the bug where an activity is added to a workflow but
    not registered in worker.py, causing runtime failures.
    """
    registered = get_registered_activities()
    used_by_workflow = get_activities_used_in_workflows()
    
    all_used = set()
    for activities in used_by_workflow.values():
        all_used.update(activities)
    
    missing = all_used - registered
    
    if missing:
        # Build helpful error message
        error_lines = ["The following activities are used in workflows but NOT registered in worker.py:"]
        for activity in sorted(missing):
            # Find which workflows use it
            workflows = [wf for wf, acts in used_by_workflow.items() if activity in acts]
            error_lines.append(f"  - {activity} (used in: {', '.join(workflows)})")
        error_lines.append("")
        error_lines.append("Add them to the 'activities=[...]' list in src/worker.py")
        
        raise AssertionError("\n".join(error_lines))
    
    print(f"✅ All {len(all_used)} activities are registered")
    print(f"   Registered: {len(registered)}")
    print(f"   Used in workflows: {len(all_used)}")


def test_no_unused_activities():
    """
    INFO TEST: Warn about activities registered but not used.
    
    This is informational - unused activities aren't an error,
    but might indicate dead code.
    """
    registered = get_registered_activities()
    used_by_workflow = get_activities_used_in_workflows()
    
    all_used = set()
    for activities in used_by_workflow.values():
        all_used.update(activities)
    
    unused = registered - all_used
    
    if unused:
        print(f"⚠️ {len(unused)} registered activities not directly used in workflows:")
        for activity in sorted(unused):
            print(f"   - {activity}")
        print("   (These might be used in other activities or be legitimate)")


if __name__ == "__main__":
    test_all_activities_registered()
    print()
    test_no_unused_activities()
