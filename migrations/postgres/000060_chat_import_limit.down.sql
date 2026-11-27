ALTER TABLE chat_imports DROP CONSTRAINT chat_imports_message_count_check;
ALTER TABLE chat_imports ADD CONSTRAINT chat_imports_message_count_check CHECK (message_count BETWEEN 1 AND 100000) NOT VALID;
