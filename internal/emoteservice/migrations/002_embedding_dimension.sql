-- jina-clip-v2 is the chosen embedding model: 1024 dimensions, and it beat
-- siglip-so400m on every measured AUC cell. Pinning the width is what makes an
-- ANN index possible at all -- hnsw rejects a dimensionless column.
ALTER TABLE emote_moderation ALTER COLUMN image_embedding TYPE vector(1024);
ALTER TABLE emote_moderation ALTER COLUMN desc_embedding TYPE vector(1024);

CREATE INDEX IF NOT EXISTS emote_moderation_image_embedding_idx
    ON emote_moderation USING hnsw (image_embedding vector_cosine_ops);

CREATE INDEX IF NOT EXISTS emote_moderation_desc_embedding_idx
    ON emote_moderation USING hnsw (desc_embedding vector_cosine_ops);
