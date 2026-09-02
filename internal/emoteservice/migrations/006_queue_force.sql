-- A re-judgement request has to be able to say so. The worker reuses a stored
-- verdict whenever the model and the classifier version both still match, which
-- is what keeps a retry from paying for the vision call twice — but it also made
-- every resweep that deliberately did not bump the version a silent no-op: the
-- rows were re-queued, drained without error, and never re-judged.
--
-- force says this row was queued to be judged again, not merely to be caught up.
-- It is set by /v1/reclassify and by an explicit /v1/enqueue, survives a failed
-- attempt so the retry still forces, and is cleared when the emote is done.
ALTER TABLE emote_queue ADD COLUMN IF NOT EXISTS force boolean NOT NULL DEFAULT false;
