-- Two classes join the taxonomy: gambling, and fluids for the bodily fluids
-- Twitch's ToS covers (semen keeps its own class, blood stays under violence).
-- Storage is text[], so only the settings rows need touching.
--
-- Both are added to what is already blocked rather than replacing it, so a
-- channel or platform row that had deliberately unblocked something keeps that
-- choice. A class that did not exist when a row was written cannot have been
-- consciously allowed by it, and these two are blocked by default, so the
-- fail-safe is to block them everywhere until someone opts back in.
UPDATE emote_platform_settings
SET blocked_classes = (
        SELECT coalesce(array_agg(c ORDER BY c), '{}')
        FROM (SELECT DISTINCT unnest(blocked_classes || ARRAY['gambling', 'fluids']) AS c) s
    ),
    updated_at = now();

UPDATE emote_streamer_settings
SET blocked_classes = (
        SELECT coalesce(array_agg(c ORDER BY c), '{}')
        FROM (SELECT DISTINCT unnest(blocked_classes || ARRAY['gambling', 'fluids']) AS c) s
    ),
    updated_at = now()
WHERE blocked_classes IS NOT NULL;
