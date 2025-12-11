#!/bin/bash
# MongoDB Replica Set Initialization
# - Creates keyfile for replica set authentication
# - Starts MongoDB and initializes replica set automatically

set -e

KEYFILE_PATH="/data/keyfile/mongodb-keyfile"
RS_INITIALIZED_FLAG="/data/db/.rs_initialized"

# Create keyfile directory if it doesn't exist
mkdir -p /data/keyfile

# Generate keyfile if it doesn't exist
if [ ! -f "$KEYFILE_PATH" ]; then
    echo "Generating MongoDB replica set keyfile..."
    openssl rand -base64 756 > "$KEYFILE_PATH"
fi

# Set correct permissions and ownership
chmod 400 "$KEYFILE_PATH"
chown mongodb:mongodb "$KEYFILE_PATH"

echo "Keyfile initialized successfully"

# Function to initialize replica set
init_replica_set() {
    # Get the container hostname from environment or use default
    local MONGO_HOST="${MONGO_REPLICA_HOST:-localhost}"
    
    echo "Waiting for MongoDB to be ready..."
    until mongosh --quiet --eval "db.adminCommand('ping')" > /dev/null 2>&1; do
        sleep 1
    done
    
    echo "MongoDB is ready. Checking replica set status..."
    
    # Check if replica set is already initialized
    RS_STATUS=$(mongosh --quiet --eval "try { rs.status().ok } catch(e) { 0 }" 2>/dev/null || echo "0")
    
    if [ "$RS_STATUS" = "1" ]; then
        echo "Replica set already initialized"
    else
        echo "Initializing replica set with host: $MONGO_HOST:27017"
        mongosh --quiet --eval "rs.initiate({_id: 'rs0', members: [{_id: 0, host: '$MONGO_HOST:27017'}]})" || true
        
        # Wait for replica set to be ready
        echo "Waiting for replica set to become ready..."
        sleep 3
        
        # Verify initialization
        RS_STATUS=$(mongosh --quiet --eval "try { rs.status().ok } catch(e) { 0 }" 2>/dev/null || echo "0")
        if [ "$RS_STATUS" = "1" ]; then
            echo "✅ Replica set initialized successfully!"
        else
            echo "⚠️ Replica set initialization may need manual intervention"
        fi
    fi
}

# Start replica set initialization in background after MongoDB starts
(sleep 5 && init_replica_set) &

# Execute the main MongoDB command
exec "$@"
