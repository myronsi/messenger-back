DROP INDEX user_avatar_history_attachment_idx;
ALTER TABLE user_avatar_history DROP COLUMN attachment_id;
DELETE FROM user_avatar_history WHERE avatar_url IS NULL;
ALTER TABLE user_avatar_history ALTER COLUMN avatar_url SET NOT NULL;

DROP INDEX chats_avatar_attachment_idx;
ALTER TABLE chats DROP COLUMN avatar_attachment_id;
DROP INDEX users_avatar_attachment_idx;
ALTER TABLE users DROP COLUMN avatar_attachment_id;

DROP TABLE attachment_links;

DROP INDEX attachments_created_idx;
DROP INDEX attachments_thumbnail_key_key;
ALTER TABLE attachments DROP COLUMN thumbnail_key, DROP COLUMN filename, DROP COLUMN kind, DROP COLUMN purpose;
DELETE FROM attachments WHERE chat_id IS NULL;
ALTER TABLE attachments ALTER COLUMN chat_id SET NOT NULL;
