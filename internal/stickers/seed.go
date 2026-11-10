package stickers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

//go:embed builtin
var builtin embed.FS

func Builtin() fs.FS {
	sub, err := fs.Sub(builtin, "builtin")
	if err != nil {
		panic(err)
	}
	return sub
}

const (
	MaxSide          = 512
	MaxBuiltinPacks  = 20
	MaxPackStickers  = 120
	maxBuiltinBytes  = 2 << 20
	contentTypeWebP  = "image/webp"
	storageKeyPrefix = "stickers/"
)

type SeedDeps struct {
	Stickers    postgres.StickerRepository
	Attachments postgres.AttachmentRepository
	Put         func(ctx context.Context, key string, data []byte, contentType string) error
}

type manifest struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Default  bool   `json:"default"`
	Stickers []struct {
		Key   string `json:"key"`
		Emoji string `json:"emoji"`
	} `json:"stickers"`
}

type Image struct {
	Width    int32
	Height   int32
	Animated bool
}

var errNotWebP = errors.New("not a WebP image")

func ProbeWebP(data []byte) (Image, error) {
	if len(data) < 30 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return Image{}, errNotWebP
	}
	u24 := func(b []byte) int32 { return int32(b[0]) | int32(b[1])<<8 | int32(b[2])<<16 }
	switch string(data[12:16]) {
	case "VP8X":
		return Image{Width: u24(data[24:27]) + 1, Height: u24(data[27:30]) + 1, Animated: data[20]&0x02 != 0}, nil
	case "VP8L":
		if data[20] != 0x2f {
			return Image{}, errNotWebP
		}
		w := int32(data[21]) | int32(data[22]&0x3f)<<8
		h := int32(data[22]>>6) | int32(data[23])<<2 | int32(data[24]&0x0f)<<10
		return Image{Width: w + 1, Height: h + 1}, nil
	case "VP8 ":
		if data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
			return Image{}, errNotWebP
		}
		return Image{Width: int32(data[26]) | int32(data[27]&0x3f)<<8, Height: int32(data[28]) | int32(data[29]&0x3f)<<8}, nil
	}
	return Image{}, errNotWebP
}

func readManifests(fsys fs.FS) ([]manifest, error) {
	dirs, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []manifest
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(fsys, path.Join(d.Name(), "pack.json"))
		if err != nil {
			return nil, err
		}
		var m manifest
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&m); err != nil {
			return nil, fmt.Errorf("sticker pack %s: %w", d.Name(), err)
		}
		if m.Key != d.Name() {
			return nil, fmt.Errorf("sticker pack %s: key must be the folder name", d.Name())
		}
		if m.Title == "" || len(m.Stickers) == 0 || len(m.Stickers) > MaxPackStickers {
			return nil, fmt.Errorf("sticker pack %s: needs a title and 1 to %d stickers", d.Name(), MaxPackStickers)
		}
		out = append(out, m)
	}
	if len(out) > MaxBuiltinPacks {
		return nil, fmt.Errorf("at most %d built-in sticker packs", MaxBuiltinPacks)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func Seed(ctx context.Context, d SeedDeps, fsys fs.FS) error {
	packs, err := readManifests(fsys)
	if err != nil {
		return err
	}
	for _, m := range packs {
		if err := seedPack(ctx, d, fsys, m); err != nil {
			return fmt.Errorf("sticker pack %s: %w", m.Key, err)
		}
	}
	return nil
}

func seedPack(ctx context.Context, d SeedDeps, fsys fs.FS, m manifest) error {
	packID, err := d.Stickers.EnsureBuiltinPack(ctx, m.Key, m.Title, m.Default)
	if err != nil {
		return err
	}
	existing, err := d.Stickers.BuiltinStickerIDs(ctx, packID)
	if err != nil {
		return err
	}
	keep := make([]string, 0, len(m.Stickers))
	var position int32
	for i, item := range m.Stickers {
		if item.Key == "" || !messages.ValidEmoji(item.Emoji) {
			return fmt.Errorf("sticker %d needs a key and an emoji", i)
		}
		name := item.Key + ".webp"
		data, err := fs.ReadFile(fsys, path.Join(m.Key, name))
		if err != nil {
			return err
		}
		img, err := ProbeWebP(data)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if img.Width > MaxSide || img.Height > MaxSide || len(data) > maxBuiltinBytes {
			return fmt.Errorf("%s: larger than %d px or %d bytes", name, MaxSide, maxBuiltinBytes)
		}
		sum := sha256.Sum256(data)
		version := item.Key + "@" + hex.EncodeToString(sum[:6])
		s := postgres.BuiltinSticker{Key: version, Emoji: item.Emoji, Position: position, Animated: img.Animated}
		position++
		keep = append(keep, version)
		if id, ok := existing[version]; ok {
			if err := d.Stickers.UpdateBuiltinSticker(ctx, id, s); err != nil {
				return err
			}
			continue
		}
		key := storageKeyPrefix + uuid.NewString()
		if err := d.Put(ctx, key, data, contentTypeWebP); err != nil {
			return err
		}
		w, h := img.Width, img.Height
		a, err := d.Attachments.Create(ctx, postgres.NewAttachment{
			Purpose: postgres.PurposeSticker, Kind: "image", Filename: name, StorageKey: key,
			MimeType: contentTypeWebP, Size: int64(len(data)), Width: &w, Height: &h,
		})
		if err != nil {
			return err
		}
		s.AttachmentID = a.ID
		if err := d.Stickers.AddBuiltinSticker(ctx, packID, s); err != nil {
			return err
		}
	}
	return d.Stickers.RetireBuiltinStickers(ctx, packID, keep)
}
