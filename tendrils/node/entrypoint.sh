#!/bin/bash
set -e

# Load nvm
export NVM_DIR="/usr/local/nvm"
[ -s "$NVM_DIR/nvm.sh" ] && \. "$NVM_DIR/nvm.sh"

# Store the path to the default stable Node installed for the executor
EXECUTOR_NODE=$(nvm which default)

# Check for .nvmrc or .node-version to set the environment PATH for child processes
if [ -f "/app/.nvmrc" ]; then
    NODE_TARGET=$(cat /app/.nvmrc | tr -d '\r\n')
    echo "Found .nvmrc specifying node version: $NODE_TARGET" >&2
    nvm install "$NODE_TARGET" >&2
    nvm use "$NODE_TARGET" >&2
elif [ -f "/app/.node-version" ]; then
    NODE_TARGET=$(cat /app/.node-version | tr -d '\r\n')
    echo "Found .node-version specifying node version: $NODE_TARGET" >&2
    nvm install "$NODE_TARGET" >&2
    nvm use "$NODE_TARGET" >&2
fi

# Execute the main executor using the stable Executor Node, 
# while the shell's PATH prioritizes the user's Node target for spawned processes!
exec "$EXECUTOR_NODE" /opt/opentendril-node/dist/main.js
