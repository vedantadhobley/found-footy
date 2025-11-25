#!/bin/bash
# Quick test fixture setup script
# Runs inside the dagster-daemon container

echo "🔧 Setting up test fixture..."
docker exec found-footy-dagster-daemon python /workspace/scripts/setup_test_fixture.py "$@"
