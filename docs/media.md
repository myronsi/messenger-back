# Media: uploads, downloads and avatars

`internal/media` stores files and decides who may get them back; `internal/httpapi/media_server.go` serves the
endpoints. Bytes live in object storage (`STORAGE_BACKEND=s3`, any S3-compatible service) or, for local runs, on disk
(`STORAGE_BACKEND=disk`, `STORAGE_DIR`). Metadata and the access rules are in PostgreSQL.

## Upload: `POST /api/v2/attachments`

`multipart/form-data` with `file`, `purpose` (`message` or `avatar`) and optionally `kind` (`voice` for a recorded
message), `duration_ms` and `waveform`. The answer is the `Attachment`; its `id` (a UUID) goes into a message
(`attachment_id`) or `PUT /me/avatar`.

1. The body is limited to the largest upload limit (not `HTTP_MAX_BODY_BYTES`), spooled to a temporary file, and
   checked against the limit of its kind: images 20 MiB, audio and voice 25 MiB, video and files 100 MiB,
   avatars 10 MiB. Uploads are rate limited (60 per minute, bursts of 20, per user).
2. **The type comes from the content**, never from the file name or the client's content type
   (`net/http.DetectContentType` plus checks for Ogg, WebM, MP4 audio and AAC). Avatars must be images; `voice` and
   `audio` must be audio containers. Everything that is not an image, audio or video is a plain file and is stored as
   `application/octet-stream` (PDF, zip and gzip keep their type). HTML, SVG and scripts sniff as text, so they are
   never stored as anything a browser would render.
3. **Images** (JPEG, PNG, GIF, WebP) are checked against limits before they are decoded (16384 px per side,
   40 MP; for a GIF the blocks are walked first: at most 300 frames and 160 MP over all frames), decoded in one of
   `MEDIA_IMAGE_WORKERS` slots (default 2, since one image can take a few hundred MB; uploads wait for a slot within
   their deadline, then get `503` with `Retry-After`) and **encoded again**: EXIF (location, camera), comments and anything malformed are gone, and the EXIF orientation of
   a JPEG is applied to the pixels first. WebP becomes PNG (with transparency) or JPEG. A 320 px JPEG thumbnail is
   stored next to it; width and height are recorded.
4. **Voice and audio** are measured with `ffprobe` (duration) and `ffmpeg` (64-bar waveform for voice messages),
   each with a 15 s timeout of its own and allowed to read only the file (`-protocol_whitelist`). The decoded PCM is
   streamed into the waveform, never held, and cut at one hour whatever the container claims. A file the tools
   cannot read as audio is refused (`415`); a measurement that times out is `503`. The image ships static builds of
   both. Without them (local runs without ffmpeg) the client's `duration_ms` and `waveform` are used, clamped.
5. Storage keys are random (`attachments/{uuid}`, `avatars/{uuid}`, `….thumb.jpg`), never derived from user
   input. The file name is kept only as a display name (base name, printable, at most 255 bytes).

## Who may download: `GET /api/v2/attachments/{id}/content`

An upload is not in any chat. Sending a message with it links it (`attachment_links`: attachment, chat, message);
forwarding links it into further chats. The server allows a download to:

- the uploader;
- a member of a chat in which a message using it is **visible to them** (the message exists, is not deleted for
  everyone and not hidden for them);
- for a group avatar, the members of the group (user avatars go through the avatar endpoints below);
- for a v1 attachment (which belongs to one chat), the members of that chat.

Everyone else gets `404`, so the existence of a file is not revealed.

The answer streams the object, with byte ranges (`206`, needed to seek and for Safari to play audio and video at
all) and an `ETag`, with `X-Content-Type-Options: nosniff`, `Content-Security-Policy: default-src
'none'; sandbox` and `Content-Disposition: inline` for images, audio and video, `attachment` (and
`application/octet-stream`) for everything else. `?variant=thumbnail` returns the thumbnail. With
`MEDIA_SIGNED_URLS=true` the API answers with a `302` to a five-minute signed URL of the bucket instead
(`Cache-Control: no-store`, so no browser follows a cached redirect to an expired URL); the bucket then needs CORS
for the app's origin, and `S3_PUBLIC_ENDPOINT` is the address clients reach. The signature can only force
`Content-Type` and `Content-Disposition`: `nosniff` and the sandbox CSP are not on those responses, so serve the
bucket from a domain of its own without cookies, or add the headers in a CDN or proxy in front of it.

Uploads and downloads (`POST /attachments`, `…/content`, the avatar images) get `HTTP_TRANSFER_TIMEOUT` (10 min)
instead of the request, read and write timeouts, so large files on slow connections finish.

## Avatars

- `PUT /me/avatar` with an upload of purpose `avatar` by the same user sets it and adds it to the history
  (`user_avatar_history`).
- `GET /users/{id}/avatar` applies the user's `avatar_visibility` (with exceptions and shared chats) and falls back
  to a neutral default image when the avatar is hidden or missing. `?version={id}` returns an earlier avatar.
- `GET /users/{id}/avatars` is the history, newest first, empty when the avatar is not visible to the caller.
- Group avatars are set with `POST /groups/{id}/avatar` (#53) and are attachments the group's members may download.

## Cleanup

The worker deletes uploads that nothing uses (no message link, not an avatar, not in an avatar history) once they are
a day old (`media.UnusedAfter`), first the row and then the objects, every hour. The row is locked first and the
references are checked in a statement of their own, so a message that links the upload at that moment either
keeps it (the collector waits for it and sees the link) or fails to send (the collector was first). Deleting
a chat removes its links, so its files become unused and are collected the same way.

## Settings

| Variable | Default | |
|---|---|---|
| `STORAGE_BACKEND` | `disk` | `disk` or `s3` |
| `STORAGE_DIR` | `data/media` | disk backend |
| `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `S3_BUCKET`, `S3_REGION` | –, –, –, `messenger-media`, `us-east-1` | s3 backend; `/readyz` checks the bucket |
| `MEDIA_SIGNED_URLS`, `S3_PUBLIC_ENDPOINT` | `false`, – | redirects to signed URLs |
| `FFPROBE_PATH`, `FFMPEG_PATH` | on `PATH` | voice measurement |
| `MEDIA_IMAGE_WORKERS` | `2` | images decoded at once |
| `HTTP_TRANSFER_TIMEOUT` | `10m` | deadline of uploads and downloads |
