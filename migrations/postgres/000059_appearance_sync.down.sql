DROP TABLE playlist_tracks;
DROP TABLE playlists;
DROP TABLE chat_wallpapers;
DROP TABLE user_client_settings;

DELETE FROM attachments WHERE purpose = 'chat_wallpaper';
ALTER TABLE attachments DROP CONSTRAINT attachments_purpose_check;
ALTER TABLE attachments ADD CONSTRAINT attachments_purpose_check
    CHECK (purpose IN ('message', 'avatar', 'sticker', 'profile_music', 'profile_background', 'emoji'));
