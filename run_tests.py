#!/usr/bin/env python3
"""Test runner for Found Footy"""
import subprocess
import sys
import os

def run_tests():
    """Run all tests with coverage"""
    print("🧪 Running Found Footy Test Suite")
    print("=" * 50)
    
    # Set environment variables for testing
    os.environ['MONGODB_URL'] = 'mongodb://test:test@localhost:27017/test'
    os.environ['S3_ENDPOINT_URL'] = 'http://localhost:9000'
    
    # Run pytest with coverage
    cmd = [
        "python", "-m", "pytest",
        "tests/",
        "-v",                    # Verbose output
        "--tb=short",           # Short traceback format
        "--cov=found_footy",    # Coverage for our package
        "--cov-report=term-missing",  # Show missing lines
        "--cov-report=html:htmlcov",  # Generate HTML coverage report
        "--durations=10"        # Show 10 slowest tests
    ]
    
    try:
        result = subprocess.run(cmd, check=False)
        
        if result.returncode == 0:
            print("\n✅ All tests passed!")
            print("📊 Coverage report generated in htmlcov/")
        else:
            print(f"\n❌ Tests failed with exit code {result.returncode}")
            
        return result.returncode
        
    except FileNotFoundError:
        print("❌ pytest not found. Install with: pip install pytest pytest-cov")
        return 1

if __name__ == "__main__":
    exit_code = run_tests()
    sys.exit(exit_code)