package media

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
)

// Kinds of attachments, as in the contract.
const (
	KindImage = "image"
	KindAudio = "audio"
	KindVoice = "voice"
	KindVideo = "video"
	KindFile  = "file"
)

// Purposes of an upload.
const (
	PurposeMessage = "message"
	PurposeAvatar  = "avatar"
)

// ErrUnsupported is returned for content that is not allowed for the purpose or kind.
var ErrUnsupported = errors.New("media: unsupported content")

// Detected is what the content turned out to be.
type Detected struct {
	Kind        string
	ContentType string
}

// imageTypes are the images the server decodes and re-encodes; anything else is never treated as an image.
var imageTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}

// audioType recognizes the audio containers browsers record and play.
func audioType(head []byte, sniffed string) string {
	switch {
	case bytes.HasPrefix(head, []byte("OggS")):
		return "audio/ogg"
	case bytes.HasPrefix(head, []byte{0x1A, 0x45, 0xDF, 0xA3}): // EBML: WebM or Matroska
		return "audio/webm"
	case len(head) >= 12 && string(head[4:8]) == "ftyp" && (string(head[8:11]) == "M4A" || string(head[8:12]) == "mp42" || string(head[8:12]) == "isom"):
		return "audio/mp4"
	case sniffed == "audio/mpeg", sniffed == "audio/wave", sniffed == "audio/aiff":
		return sniffed
	case len(head) >= 2 && head[0] == 0xFF && head[1]&0xF6 == 0xF0: // ADTS AAC
		return "audio/aac"
	}
	return ""
}

// Detect decides what an upload is from its first bytes, never from its name or the client's content type.
//   - avatar: only images;
//   - kind voice or audio: only audio containers;
//   - otherwise images and audio and video are recognized, and everything else is a plain file.
func Detect(head []byte, purpose, kind string) (Detected, error) {
	sniffed := http.DetectContentType(head)
	if i := strings.IndexByte(sniffed, ';'); i >= 0 {
		sniffed = sniffed[:i]
	}
	if purpose == PurposeAvatar {
		if !imageTypes[sniffed] {
			return Detected{}, ErrUnsupported
		}
		return Detected{Kind: KindImage, ContentType: sniffed}, nil
	}
	switch kind {
	case KindVoice, KindAudio:
		at := audioType(head, sniffed)
		if at == "" {
			return Detected{}, ErrUnsupported
		}
		return Detected{Kind: kind, ContentType: at}, nil
	case KindImage:
		if !imageTypes[sniffed] {
			return Detected{}, ErrUnsupported
		}
		return Detected{Kind: KindImage, ContentType: sniffed}, nil
	}
	switch {
	case imageTypes[sniffed]:
		return Detected{Kind: KindImage, ContentType: sniffed}, nil
	case sniffed == "video/mp4" || sniffed == "video/webm":
		return Detected{Kind: KindVideo, ContentType: sniffed}, nil
	}
	if at := audioType(head, sniffed); at != "" && at != "audio/webm" {
		return Detected{Kind: KindAudio, ContentType: at}, nil
	}
	return Detected{Kind: KindFile, ContentType: fileType(sniffed)}, nil
}

// fileType keeps a few harmless types and turns everything else into application/octet-stream, so no stored
// type can make a browser render or run a file.
func fileType(sniffed string) string {
	// Text is not on the list: HTML, SVG and scripts sniff as text/plain or text/xml.
	switch sniffed {
	case "application/pdf", "application/zip", "application/x-gzip":
		return sniffed
	}
	return "application/octet-stream"
}

// Inline reports whether a stored type may be shown in the browser (images, audio, video). Everything else is
// downloaded. HTML, SVG and scripts never reach this point as such: Detect stores them as octet-stream.
func Inline(contentType string) bool {
	return imageTypes[contentType] || strings.HasPrefix(contentType, "audio/") || strings.HasPrefix(contentType, "video/")
}
