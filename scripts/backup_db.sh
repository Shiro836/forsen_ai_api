#!/bin/bash
set -e

# Default config path
CFG_PATH="cfg/cfg.yaml"

if [ ! -f "$CFG_PATH" ]; then
    echo "Config file not found at $CFG_PATH"
    exit 1
fi

# Extract connection string
# This is a simple regex extraction, assuming the format in cfg.yaml
CONN_STR=$(grep "conn_str:" "$CFG_PATH" | sed -E 's/.*conn_str: "([^"]+)".*/\1/')

if [ -z "$CONN_STR" ]; then
    # Try without quotes if the first attempt failed
    CONN_STR=$(grep "conn_str:" "$CFG_PATH" | sed -E 's/.*conn_str: ([^ ]+).*/\1/')
fi

if [ -z "$CONN_STR" ]; then
    echo "Could not find conn_str in $CFG_PATH"
    exit 1
fi

BACKUP_FILE="backups/backup_$(date +%Y%m%d_%H%M%S).sql"

echo "Backing up database to $BACKUP_FILE..."
pg_dump "$CONN_STR" > "$BACKUP_FILE"

echo "Backup complete: $BACKUP_FILE"
