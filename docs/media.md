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
   (`net/http.DetectContentType` plus checks for Ogg, WebM, MP4 audio, FLAC and AAC). HEIC, HEIF and AVIF photos
   (recognized by their `ftyp` brand) are turned into a PNG with `ffmpeg` and then handled like any image; when
   `ffmpeg` cannot read them they stay plain files. Avatars must be images; `voice` and
   `audio` must be audio containers. Everything that is not an image, audio or video is a plain file and is stored as
   `application/octet-stream` (PDF, zip and gzip keep their type). HTML, SVG and scripts sniff as text, so they are
   never stored as anything a browser would render.
3. **Images** (JPEG, PNG, GIF, WebP) are checked against limits before they are decoded (16384 px per side,
   40 MP; for a GIF the blocks are walked first: at most 300 frames and 160 MP over all frames), decoded in one of
   `MEDIA_IMAGE_WORKERS` slots (default 2, since one image can take a few hundred MB; uploads wait for a slot within
   their deadline, then get `503` with `Retry-After`) and **encoded again**: EXIF (location, camera), comments and anything malformed are gone, and the EXIF orientation of
   a JPEG is applied to the pixels first. WebP stays WebP (lossless stays lossless); a PNG without transparency
   becomes a JPEG when that is less than half its size. With `ffmpeg` the server also stores, in the same request:
   a 480 px WebP thumbnail (without `ffmpeg` a 320 px JPEG), for a still image over 1600 px a 1600 px WebP display
   copy (`variants.display`; older uploads get a JPEG `{key}.display.jpg` on first request), avatars at 64, 128 and
   256 px, and a ThumbHash `placeholder` clients draw before the thumbnail arrives. Width and height are recorded.
4. **Voice and audio** are measured with `ffprobe` (duration) and `ffmpeg` (64-bar waveform for voice messages),
   each with a 15 s timeout of its own and allowed to read only the file (`-protocol_whitelist`). The decoded PCM is
   streamed into the waveform, never held, and cut at one hour whatever the container claims. A file the tools
   cannot read as audio is refused (`415`); a measurement that times out is `503`. The image ships static builds of
   both. Without them (local runs without ffmpeg) the client's `duration_ms` and `waveform` are used, clamped.
5. Storage keys are random (`attachments/{uuid}`, `avatars/{uuid}`, `….thumb.jpg`), never derived from user
   input. The file name is kept only as a display name (base name, printable, at most 255 bytes).

## Variants and optimization jobs

Smaller copies are listed in `attachments.variants` (name, key, type, size, size in px) and deleted with the
attachment. Made at upload: `display`, `avatar_64`/`_128`/`_256`, sticker `panel` (still 128 px) and `message`
(320 px), custom emoji `inline` (64 px; emoji themselves are stored as WebP: lossless for stills, animated for GIFs).
Built-in stickers ship their panel frames in `internal/stickers/builtin/<pack>/panel/` (made once with a local
ffmpeg that reads animated WebP; the static production build cannot).

Slow work goes to `media_jobs` and runs in the worker (`MEDIA_TRANSCODE_WORKERS`, default 1, two ffmpeg threads
each; claimed with `FOR UPDATE SKIP LOCKED`, three attempts with growing pauses, jobs running over 30 minutes are
queued again). `attachments.processing_state` shows the state to clients (`processing`):

- `video`: H.264/AAC video with `yuv420p`, a short side of at most 720 px and at most 3 Mbit/s is left alone, or only
  rewritten with `+faststart` when its index (`moov`) is behind the data. Anything else becomes H.264 (CRF 26, at most
  720 px on the short side and 30 fps) with AAC 96 kbit/s and `+faststart`, without metadata (location). The copy is
  `variants.optimized`; `GET …/content` then serves it, `?variant=source` the upload. Videos over 30 minutes are skipped.
- `audio`: lossless files and anything over 192 kbit/s become AAC 160 kbit/s (`.m4a`, `+faststart`, tags kept); MP4
  audio with the index at the end is only rewritten. A copy that is not smaller is dropped.
- `gif`: a GIF in a message also gets a muted MP4 (`variants.animation`, `animation_url`); the GIF stays the content.

When a job ends the worker sends `attachment_updated` to the uploader and the members of chats that use the file.
Uploads with a running job are not collected as unused. Originals are kept.

Older files: `worker media-backfill --kind video|audio|gif|audio-tags|image|stickers|emoji|avatars [--limit N]
[--pause 200ms]` queues jobs or makes the missing variants, in id order; run it once per kind after deploying.

Audio files (not voice messages) also keep their `title`, `artist` and `album` tags (`tags`) and the embedded cover as
thumbnail.

## Streaming: `POST /api/v2/attachments/{id}/playback`

Media elements cannot send a bearer token, so the web app used to download whole videos into a blob before playing
them. `POST …/playback` answers with a URL `…/content?pt=<token>`: an HMAC (derived from `JWT_SECRET`) over the
attachment, the viewer and an expiry five minutes ahead. `GET …/content` accepts `pt` instead of the bearer token
for that attachment only, so the browser streams with range requests and starts at once. The access check runs when
the token is made.

## Resumable uploads: `/api/v2/upload-sessions`

For large files on unstable connections: `POST /upload-sessions` (purpose, kind, file name, size) returns a session
with `chunk_size` (8 MiB); the client sends `PUT /upload-sessions/{id}/chunks?offset=N` in order (each chunk is an
object `uploads/{id}/{n}` of its own, so any API instance can take the next one), asks `GET /upload-sessions/{id}`
where to continue after a broken connection, and `POST …/complete` joins the chunks and runs the normal upload
checks. At most 5 open sessions per user; the worker deletes sessions without progress for 24 hours.

## Link preview images

`GET /link-preview/image` shrinks the fetched image to at most 600 px WebP (when that is smaller) and keeps the
result in Redis for 7 days (results up to 512 KB), so a link shared in many chats is fetched from the other site once.

## Who may download: `GET /api/v2/attachments/{id}/content`

An upload is not in any chat. Sending a message with it links it (`attachment_links`: attachment, chat, message);
forwarding links it into further chats. The server allows a download to:

- the uploader;
- a member of a chat in which a message using it is **visible to them** (the message exists, is not deleted for
  everyone and not hidden for them);
- for a group avatar, the members of the group (user avatars go through the avatar endpoints below);
- for a v1 attachment (which belongs to one chat), the members of that chat;
- for a sticker image (purpose `sticker`), every signed-in user, so a sticker pack can be previewed from a chat.

Everyone else gets `404`, so the existence of a file is not revealed.

The answer streams the object, with byte ranges (`206`, needed to seek and for Safari to play audio and video at
all) and an `ETag`, with `X-Content-Type-Options: nosniff`, `Content-Security-Policy: default-src
'none'; sandbox` and `Content-Disposition: inline` for images, audio and video, `attachment` (and
`application/octet-stream`) for everything else. `?variant=thumbnail` returns the thumbnail and
`?variant=display` the display copy (`display_url`), or the original when the image needs none. Images uploaded display copies existed get theirs on the first such request, made in an image worker slot and stored. With
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

The worker deletes uploads that nothing uses (no message link, not an avatar, not in an avatar history, not a sticker, not
in a rejected send kept for retry) once they are
a day old (`media.UnusedAfter`), first the row and then the objects, every hour. The row is locked first and the
references are checked in a statement of their own, so a message that links the upload at that moment either
keeps it (the collector waits for it and sees the link) or fails to send (the collector was first). Deleting
a chat removes its links, so its files become unused and are collected the same way. Deleting a message for
everyone removes its link; a forwarded copy has a link of its own and keeps the file.

## Stickers

Sticker packs live in `sticker_packs` and `stickers`; each sticker points at an attachment of purpose `sticker`
(stored under `stickers/<uuid>`). A sticker message stores `sticker_id` and the sticker's `attachment_id` in
ScyllaDB but is never linked in `attachment_links`, so stickers do not show up in a chat's media lists and are
never collected.

Built-in packs are embedded in the binary (`internal/stickers/builtin/<key>/`: `pack.json` with `key`, `title`,
`default` and the ordered `stickers` with `key` and `emoji`, plus one `<key>.webp` per sticker of at most 512 px).
The worker seeds them on start and retries every minute until it succeeds: new stickers are uploaded, changed
emoji and order are updated, and stickers removed from the manifest are marked deleted (sent ones keep showing).
A sticker is stored under its key and a hash of its image, so a changed image becomes a new sticker and the old
one is retired; messages that sent the old one keep showing it.
Packs with `default: true` are in every user's panel until the user removes them (`user_sticker_packs`).
`user_recent_stickers` keeps the last 20 stickers each user sent.

Users make their own packs. The client crops, cuts out and draws on the image itself and uploads the finished picture
with `purpose: sticker`: a PNG, JPEG or still WebP of at most 5 MB. The server decodes it (dropping all metadata),
fits it into 512 x 512 px with a transparent border and encodes a WebP with ffmpeg (a PNG when ffmpeg is missing);
the result must be at most 512 KB.

A sticker can also be made from a video (MP4, WebM, MOV) or a GIF of at most 15 MB (`internal/media/sticker_video.go`).
ffmpeg keeps at most 3 s (`start_ms`/`end_ms`, otherwise the first 3 s), crops the square `crop_x`/`crop_y`/
`crop_size` or fits the clip into 512 x 512 with a transparent border, drops the sound and encodes a looping
animated WebP (`libwebp_anim`). It lowers quality and frame rate (30, 24, 20, 15 fps) until the file is at most 1 MB,
otherwise `413`. The `panel` variant is a still first frame and `message` an animated 320 px copy of at most
512 KB. The attachment records the clip length in `duration`, and adding it to a pack sets `is_animated`. The work
runs inside the upload request with an image worker slot, not as a job.

An animated WebP is a sticker source too, but ffmpeg cannot decode animated WebP. The server decodes its frames in Go
(`internal/media/sticker_webp.go`), composites them with their blend and dispose flags, crops and fits each into
512 x 512, samples them at 30 fps inside the same 3 s window and pipes them to ffmpeg as raw RGBA, which encodes them
like a video clip.

Until the image is added to a pack only its uploader can load it, and an image
no pack uses is collected like any other unused upload. A user has at most 50 packs of at most 120 stickers; only
the owner changes a pack, and deleting a pack or a sticker only marks it deleted, so sent stickers keep showing.
Every user pack has a random `slug`, its share link, and pack IDs are not secret, so a user pack is served by
its ID (preview, add, report, sending its stickers) only to its owner and to users who opened its link before
(`user_sticker_packs` remembers them with `installed = false`). `Sticker.pack_slug` carries the link, so whoever
receives a sticker can open its pack. Deleting an account deletes its packs.

Reports (`sticker_pack_reports`, one per user and pack, at most 20 per user a day) hide a pack after 5 different
users reported it: `hidden_at` takes it out of every panel but its owner's, it cannot be added, and stickers cannot
be sent from it. `worker unhide-sticker-pack <pack_id>` restores a pack and clears its reports.

## Settings

| Variable | Default | |
|---|---|---|
| `STORAGE_BACKEND` | `disk` | `disk` or `s3` |
| `STORAGE_DIR` | `data/media` | disk backend |
| `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `S3_BUCKET`, `S3_REGION` | –, –, –, `messenger-media`, `us-east-1` | s3 backend; `/readyz` checks the bucket |
| `MEDIA_SIGNED_URLS`, `S3_PUBLIC_ENDPOINT` | `false`, – | redirects to signed URLs |
| `FFPROBE_PATH`, `FFMPEG_PATH` | on `PATH` | voice measurement |
| `MEDIA_IMAGE_WORKERS` | `2` | images decoded at once |
| `MEDIA_TRANSCODE_WORKERS` | `1` | video, GIF and audio jobs the worker runs at once |
| `HTTP_TRANSFER_TIMEOUT` | `10m` | deadline of uploads and downloads |
