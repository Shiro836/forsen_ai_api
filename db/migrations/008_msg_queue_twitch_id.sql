ALTER TABLE msg_queue ADD COLUMN IF NOT EXISTS unique_id text;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS msg_queue_unique_id_idx 
ON msg_queue (unique_id) 
WHERE unique_id IS NOT NULL;
