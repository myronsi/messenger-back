# Database image storage plan

## Target design

Store image bytes in PostgreSQL instead of `static/` files. Add an `image_blobs` table with an identity primary key, `content_type`, `byte_size`, `sha256`, `data BYTEA`, and `created_at`. Add nullable `avatar_image_id` foreign keys to `users` and `chats`; message attachments should reference an `image_blobs` record through a dedicated attachment table rather than embedding a database identifier in JSON message content.

Serve images through authenticated API routes, for example `GET /images/{id}`, that check the same profile, group, and chat permissions already applied to the referencing resource. Return `Content-Type`, `Content-Length`, `ETag` from the SHA-256 digest, and cache-control headers. Keep image records immutable; avatar history stores the referenced image ID and the capture time.

## Migration sequence

1. Add tables and nullable foreign keys, then deploy read support while preserving existing `/static/` URLs.
2. Backfill existing image files in batches: validate type and size, hash each file, insert it once per digest, and associate it with the user, chat, or attachment. Record source path and failures for retry.
3. Switch upload endpoints to validate and write image bytes in a transaction, returning API image URLs. Update reads to prefer database references with a temporary static-file fallback.
4. After backfill verification and a retention window, remove the fallback and delete only successfully migrated static files. Keep PostgreSQL backups and point-in-time recovery enabled before cleanup.

Use a database size limit (for example 10 MB per image), allowlisted image media types, decoded-pixel limits, and image re-encoding to mitigate malformed-image and decompression-bomb uploads. For substantially larger files or high-volume delivery, retain database metadata and use object storage rather than PostgreSQL `BYTEA`.
