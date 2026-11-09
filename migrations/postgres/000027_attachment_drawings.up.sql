ALTER TABLE attachment_links ADD COLUMN drawing JSONB CHECK (drawing IS NULL OR jsonb_typeof(drawing) = 'object');
