ALTER TABLE messages ADD removed_by_moderation boolean;
ALTER TABLE messages ADD moderation_reason_code text;
ALTER TABLE messages ADD moderation_reason_text text;
