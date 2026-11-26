package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	MaxPlaylists      = 100
	MaxPlaylistTracks = 1000
)

const PurposeChatWallpaper = "chat_wallpaper"

type ClientSettings struct {
	Appearance      json.RawMessage
	RecentEmoji     []string
	RecentReactions []string
	Version         int64
	UpdatedAt       *time.Time
}

type ClientSettingsPatch struct {
	Appearance      json.RawMessage
	RecentEmoji     *[]string
	RecentReactions *[]string
}

type Wallpaper struct {
	UserID       int64
	ChatID       *int64
	Kind         string
	Preset       *string
	Colors       []string
	Pattern      *string
	AttachmentID *uuid.UUID
	Blur         *int16
	Dim          *int16
	SetBy        int64
	UpdatedAt    time.Time
}

type PlaylistSummary struct {
	ID         int64
	UserID     int64
	Name       string
	TrackCount int
	Version    int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type PlaylistTrack struct {
	AttachmentID    uuid.UUID
	ChatID          *int64
	MessageID       *int64
	Title           string
	Artist          *string
	DurationSeconds *float64
}

type Playlist struct {
	PlaylistSummary
	Tracks []PlaylistTrack
}

type PersonalRepository interface {
	ClientSettings(ctx context.Context, userID int64) (ClientSettings, error)
	UpdateClientSettings(ctx context.Context, userID int64, p ClientSettingsPatch) (ClientSettings, error)
	Wallpapers(ctx context.Context, userID int64) ([]Wallpaper, error)
	SetWallpapers(ctx context.Context, ws []Wallpaper) ([]Wallpaper, error)
	DeleteWallpaper(ctx context.Context, userID int64, chatID *int64) (bool, error)
	Playlists(ctx context.Context, userID int64) ([]PlaylistSummary, error)
	Playlist(ctx context.Context, userID, id int64) (Playlist, error)
	CreatePlaylist(ctx context.Context, userID int64, name string, tracks []PlaylistTrack) (Playlist, error)
	RenamePlaylist(ctx context.Context, userID, id int64, name string) (PlaylistSummary, error)
	DeletePlaylist(ctx context.Context, userID, id int64) error
	ReplacePlaylistTracks(ctx context.Context, userID, id, version int64, tracks []PlaylistTrack) (Playlist, error)
}

func (s *Store) Personal() PersonalRepository { return personalRepo{s} }

type personalRepo struct{ s *Store }

func (r personalRepo) ClientSettings(ctx context.Context, userID int64) (ClientSettings, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	out, err := scanClientSettings(r.s.pool.QueryRow(ctx, `SELECT appearance, recent_emoji, recent_reactions, version, updated_at
FROM user_client_settings WHERE user_id = $1`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientSettings{RecentEmoji: []string{}, RecentReactions: []string{}}, nil
	}
	return out, mapError(err)
}

func (r personalRepo) UpdateClientSettings(ctx context.Context, userID int64, p ClientSettingsPatch) (ClientSettings, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var appearance any
	if p.Appearance != nil {
		appearance = []byte(p.Appearance)
	}
	var emoji, reactions any
	if p.RecentEmoji != nil {
		emoji = *p.RecentEmoji
	}
	if p.RecentReactions != nil {
		reactions = *p.RecentReactions
	}
	out, err := scanClientSettings(r.s.pool.QueryRow(ctx, `INSERT INTO user_client_settings AS s (user_id, appearance, recent_emoji, recent_reactions, version, updated_at)
VALUES ($1, $2::jsonb, COALESCE($3::text[], '{}'), COALESCE($4::text[], '{}'), 1, now())
ON CONFLICT (user_id) DO UPDATE SET
    appearance = COALESCE($2::jsonb, s.appearance),
    recent_emoji = COALESCE($3::text[], s.recent_emoji),
    recent_reactions = COALESCE($4::text[], s.recent_reactions),
    version = s.version + 1,
    updated_at = now()
RETURNING appearance, recent_emoji, recent_reactions, version, updated_at`, userID, appearance, emoji, reactions))
	return out, mapError(err)
}

func scanClientSettings(row pgx.Row) (ClientSettings, error) {
	var out ClientSettings
	var appearance []byte
	var at time.Time
	if err := row.Scan(&appearance, &out.RecentEmoji, &out.RecentReactions, &out.Version, &at); err != nil {
		return ClientSettings{}, err
	}
	if appearance != nil {
		out.Appearance = appearance
	}
	out.UpdatedAt = &at
	return out, nil
}

const wallpaperColumns = `user_id, chat_id, kind, preset, colors, pattern, attachment_id, blur, dim, set_by, updated_at`

func scanWallpaper(row pgx.Row) (Wallpaper, error) {
	var w Wallpaper
	err := row.Scan(&w.UserID, &w.ChatID, &w.Kind, &w.Preset, &w.Colors, &w.Pattern, &w.AttachmentID, &w.Blur, &w.Dim, &w.SetBy, &w.UpdatedAt)
	return w, err
}

func (r personalRepo) Wallpapers(ctx context.Context, userID int64) ([]Wallpaper, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.pool.Query(ctx, `SELECT `+wallpaperColumns+` FROM chat_wallpapers WHERE user_id = $1
ORDER BY chat_id NULLS FIRST`, userID)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	out := []Wallpaper{}
	for rows.Next() {
		w, err := scanWallpaper(rows)
		if err != nil {
			return nil, mapError(err)
		}
		out = append(out, w)
	}
	return out, mapError(rows.Err())
}

func (r personalRepo) SetWallpapers(ctx context.Context, ws []Wallpaper) ([]Wallpaper, error) {
	var out []Wallpaper
	err := r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		out = make([]Wallpaper, 0, len(ws))
		for _, w := range ws {
			stored, err := scanWallpaper(tx.QueryRow(ctx, `INSERT INTO chat_wallpapers AS w (user_id, chat_id, kind, preset, colors, pattern, attachment_id, blur, dim, set_by, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
ON CONFLICT (user_id, chat_id) DO UPDATE SET
    kind = EXCLUDED.kind, preset = EXCLUDED.preset, colors = EXCLUDED.colors, pattern = EXCLUDED.pattern,
    attachment_id = EXCLUDED.attachment_id, blur = EXCLUDED.blur, dim = EXCLUDED.dim, set_by = EXCLUDED.set_by,
    updated_at = now()
RETURNING `+wallpaperColumns, w.UserID, w.ChatID, w.Kind, w.Preset, w.Colors, w.Pattern, w.AttachmentID, w.Blur, w.Dim, w.SetBy))
			if err != nil {
				return err
			}
			out = append(out, stored)
		}
		return nil
	})
	return out, err
}

func (r personalRepo) DeleteWallpaper(ctx context.Context, userID int64, chatID *int64) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM chat_wallpapers WHERE user_id = $1 AND chat_id IS NOT DISTINCT FROM $2`, userID, chatID)
	if err != nil {
		return false, mapError(err)
	}
	return tag.RowsAffected() > 0, nil
}

const playlistColumns = `p.id, p.user_id, p.name, (SELECT count(*) FROM playlist_tracks t WHERE t.playlist_id = p.id), p.version, p.created_at, p.updated_at`

func scanPlaylist(row pgx.Row) (PlaylistSummary, error) {
	var p PlaylistSummary
	err := row.Scan(&p.ID, &p.UserID, &p.Name, &p.TrackCount, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (r personalRepo) Playlists(ctx context.Context, userID int64) ([]PlaylistSummary, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.pool.Query(ctx, `SELECT `+playlistColumns+` FROM playlists p WHERE p.user_id = $1
ORDER BY p.created_at DESC, p.id DESC`, userID)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	out := []PlaylistSummary{}
	for rows.Next() {
		p, err := scanPlaylist(rows)
		if err != nil {
			return nil, mapError(err)
		}
		out = append(out, p)
	}
	return out, mapError(rows.Err())
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func loadPlaylist(ctx context.Context, q querier, userID, id int64, lock bool) (Playlist, error) {
	sql := `SELECT ` + playlistColumns + ` FROM playlists p WHERE p.id = $1 AND p.user_id = $2`
	if lock {
		sql += ` FOR UPDATE`
	}
	summary, err := scanPlaylist(q.QueryRow(ctx, sql, id, userID))
	if err != nil {
		return Playlist{}, err
	}
	tracks, err := loadTracks(ctx, q, id)
	if err != nil {
		return Playlist{}, err
	}
	return Playlist{PlaylistSummary: summary, Tracks: tracks}, nil
}

func loadTracks(ctx context.Context, q querier, id int64) ([]PlaylistTrack, error) {
	rows, err := q.Query(ctx, `SELECT attachment_id, chat_id, message_id, title, artist, duration_seconds
FROM playlist_tracks WHERE playlist_id = $1 ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlaylistTrack{}
	for rows.Next() {
		var t PlaylistTrack
		if err := rows.Scan(&t.AttachmentID, &t.ChatID, &t.MessageID, &t.Title, &t.Artist, &t.DurationSeconds); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r personalRepo) Playlist(ctx context.Context, userID, id int64) (Playlist, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	p, err := loadPlaylist(ctx, r.s.pool, userID, id, false)
	return p, mapError(err)
}

func writeTracks(ctx context.Context, tx pgx.Tx, id int64, tracks []PlaylistTrack) error {
	if _, err := tx.Exec(ctx, `DELETE FROM playlist_tracks WHERE playlist_id = $1`, id); err != nil {
		return err
	}
	for i, t := range tracks {
		if _, err := tx.Exec(ctx, `INSERT INTO playlist_tracks (playlist_id, position, attachment_id, chat_id, message_id, title, artist, duration_seconds)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, id, i, t.AttachmentID, t.ChatID, t.MessageID, t.Title, t.Artist, t.DurationSeconds); err != nil {
			return err
		}
	}
	return nil
}

func (r personalRepo) CreatePlaylist(ctx context.Context, userID int64, name string, tracks []PlaylistTrack) (Playlist, error) {
	var out Playlist
	err := r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM playlists WHERE user_id = $1`, userID).Scan(&count); err != nil {
			return err
		}
		if count >= MaxPlaylists {
			return ErrLimit
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO playlists (user_id, name) VALUES ($1, $2) RETURNING id`, userID, name).Scan(&id); err != nil {
			return err
		}
		if err := writeTracks(ctx, tx, id, tracks); err != nil {
			return err
		}
		var err error
		out, err = loadPlaylist(ctx, tx, userID, id, false)
		return err
	})
	return out, err
}

func (r personalRepo) RenamePlaylist(ctx context.Context, userID, id int64, name string) (PlaylistSummary, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	p, err := scanPlaylist(r.s.pool.QueryRow(ctx, `UPDATE playlists p SET name = $3, version = p.version + 1, updated_at = now()
WHERE p.id = $1 AND p.user_id = $2 RETURNING `+playlistColumns, id, userID, name))
	return p, mapError(err)
}

func (r personalRepo) DeletePlaylist(ctx context.Context, userID, id int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM playlists WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r personalRepo) ReplacePlaylistTracks(ctx context.Context, userID, id, version int64, tracks []PlaylistTrack) (Playlist, error) {
	var out Playlist
	err := r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var current int64
		if err := tx.QueryRow(ctx, `SELECT version FROM playlists WHERE id = $1 AND user_id = $2 FOR UPDATE`, id, userID).Scan(&current); err != nil {
			return err
		}
		if current != version {
			return ErrConflict
		}
		if err := writeTracks(ctx, tx, id, tracks); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE playlists SET version = version + 1, updated_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		var err error
		out, err = loadPlaylist(ctx, tx, userID, id, false)
		return err
	})
	return out, err
}
