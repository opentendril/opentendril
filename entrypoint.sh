#!/bin/bash
set -e

echo "🌱 Initializing Tendril..."
mkdir -p /app/data/dynamic_skills /app/logs

touch /app/data/.initialized
echo "✅ Initialization complete. Starting Tendril..."

# Configure git for the mounted workspace
# safe.directory prevents ownership mismatch errors on mounted volumes
git config --global --add safe.directory /workspace
git config --global user.name "Tendril"
git config --global user.email "tendril@jurnx.com"

# Use uvicorn with --reload for live development via volume mount
exec uvicorn src.main:app \
    --host 0.0.0.0 \
    --port 8080 \
    --reload \
    --reload-dir /app/src \
    --log-level info
