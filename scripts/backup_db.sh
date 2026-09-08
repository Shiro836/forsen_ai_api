#!/bin/bash
set -e

# Default config path
CFG_PATH="cfg/cfg.yaml"

if [ ! -f "$CFG_PATH" ]; then
    echo "Config file not found at $CFG_PATH"
    exit 1
fi

# Several blocks carry a conn_str (emote_service has its own postgres); only
# the top-level db: block is the app database.
CONN_STR=$(awk '
    /^db:/ { in_db = 1; next }
    /^[^ \t#]/ { in_db = 0 }
    in_db && /^[ \t]+conn_str:/ {
        sub(/^[ \t]+conn_str:[ \t]*/, ""); gsub(/"/, ""); print; exit
    }
' "$CFG_PATH")

if [ -z "$CONN_STR" ]; then
    echo "Could not find db.conn_str in $CFG_PATH"
    exit 1
fi

# Strip pgxpool-only query params (pool_max_conns etc.) that pg_dump rejects
CONN_STR=$(echo "$CONN_STR" | sed -E 's/([?&])pool_[a-z_]+=[^&]+&?/\1/g; s/[?&]$//')

BACKUP_FILE="backups/backup_$(date +%Y%m%d_%H%M%S).sql"

echo "Backing up database to $BACKUP_FILE..."
pg_dump "$CONN_STR" > "$BACKUP_FILE"

echo "Backup complete: $BACKUP_FILE"
