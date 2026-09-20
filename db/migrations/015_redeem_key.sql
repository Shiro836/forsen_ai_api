-- A redemption is a different thing from the chat message it was made with,
-- and Twitch names it only in its own event, never in chat. redeem_key is what
-- the two do share (viewer, reward, text), so the event can find its message.
ALTER TABLE msg_queue ADD COLUMN IF NOT EXISTS redeem_key text;

CREATE INDEX CONCURRENTLY IF NOT EXISTS msg_queue_redeem_key_idx
ON msg_queue (user_id, redeem_key)
WHERE redeem_key IS NOT NULL;
