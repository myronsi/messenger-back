# Synced settings, chat backgrounds and playlists

Contract 2.1.0-alpha.50, migration `000059_appearance_sync`.

## Synced settings

`user_client_settings` holds one row per user: `appearance` (accent, font size, density, corners), the 24 most recent
emoji and the 8 most recent reactions. `PATCH /me/client-settings` replaces only the parts it sends (last write wins)
and raises `version`. Every change goes to all of the user's sessions as `client_settings_updated`.

Theme and language are not stored here; they stay on each device. A device can also keep any appearance field for
itself ("only on this device"); that choice never reaches the server.

## Chat backgrounds

`chat_wallpapers` holds one row per user and chat; `chat_id` `NULL` is the default for all chats (unique with
`NULLS NOT DISTINCT`). Kinds:

| Kind | Fields |
|---|---|
| `preset` | `preset`, a client built-in (`plain`, `tint`, `gradient`, `dots`, `grid`) |
| `color` | `colors` (1 or 2), optional `pattern` (`dots`, `grid`) |
| `media` | `attachment_id` (purpose `chat_wallpaper`, uploaded by the caller), `blur` 0-20, `dim` 0-60 (absent: the client dims automatically, 20 % dark, 0 % light) |

"For both" (`for_both: true`) works only in a direct chat with another person and when neither blocked the other. It
writes a separate row for each person with `set_by` = the actor, posts the system line `chat_wallpaper_set` and sends
`wallpaper_updated` to both. Afterwards each person changes or resets only their own row.

Who may load a `chat_wallpaper` file: its uploader, and anyone whose row uses it (`UsedAsWallpaperBy`). A file nobody
uses any more is removed by the unreferenced-attachment cleanup.

## Playlists

`playlists` and `playlist_tracks` (ordered by `position`). At most 100 playlists per user and 1000 tracks per
playlist; an attachment appears once per playlist. `PUT .../tracks` replaces the whole ordered list and needs the
current `version` (`409 conflict` otherwise), so two devices cannot silently overwrite each other.

A track stores the attachment id, the chat and message it came from and its title, artist and duration. On every read
the server checks the attachment with the normal media access rules; a track the user can no longer open has
`available: false` and no `attachment`. Changes go to the user's sessions as `playlists_updated`.

Recently played and the play queue stay on each device.
