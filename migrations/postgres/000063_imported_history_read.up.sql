UPDATE participants p SET last_read_message_id = done.last_id
FROM (SELECT chat_id, max(last_message_id) AS last_id FROM chat_imports WHERE state = 'done' GROUP BY chat_id) done
WHERE p.chat_id = done.chat_id AND p.last_read_message_id IS NULL;
