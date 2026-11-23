package search

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gocql/gocql"
	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

type kindFiles map[uuid.UUID]postgres.Attachment

func (f kindFiles) Get(_ context.Context, id uuid.UUID) (postgres.Attachment, error) {
	a, ok := f[id]
	if !ok {
		return postgres.Attachment{}, postgres.ErrNotFound
	}
	return a, nil
}

func filterOf(t *testing.T, body map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(body["query"])
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestKindFiltersAttachmentsAndLinks(t *testing.T) {
	visual := filterOf(t, request(1, "harbour", []string{"1"}, Query{Kind: "visual"}))
	if !strings.Contains(visual, `{"terms":{"media_kinds":["image","video"]}}`) {
		t.Fatalf("visual filter: %s", visual)
	}
	link := filterOf(t, request(1, "", []string{"1"}, Query{Kind: KindLink}))
	if !strings.Contains(link, `{"term":{"has_link":true}}`) || !strings.Contains(link, `"match_all"`) {
		t.Fatalf("links without words: %s", link)
	}
	plain := filterOf(t, request(1, "harbour", []string{"1"}, Query{}))
	if strings.Contains(plain, "media_kinds") || strings.Contains(plain, "has_link") || strings.Contains(plain, "match_all") {
		t.Fatalf("no kind: %s", plain)
	}
}

func TestWordsAreOptionalOnlyWithAKind(t *testing.T) {
	s, _, _, visible := pageFixture(3, func(int) string { return "" })
	ctx := context.Background()
	if _, _, err := s.Search(ctx, 7, Query{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no words and no kind: %v", err)
	}
	if _, _, err := s.Search(ctx, 7, Query{Kind: "photo"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown kind: %v", err)
	}
	hits, _, err := s.Search(ctx, 7, Query{Kind: "file"})
	if err != nil || len(hits) != len(visible) {
		t.Fatalf("a kind without words: %d hits, %v", len(hits), err)
	}
}

func TestDocumentsCarryAttachmentKindsAndLinks(t *testing.T) {
	photo, clip, doc := uuid.New(), uuid.New(), uuid.New()
	in := NewIndexer(NewIndex(nil, "", 0), nil, kindFiles{
		photo: {ID: photo, Kind: "image", Filename: "harbour.jpg"},
		clip:  {ID: clip, Kind: "video", Filename: "boat.mp4"},
		doc:   {ID: doc, Kind: "file", Filename: "plan.pdf"},
	}, nil)
	text := "see https://example.com/trip"
	album := in.doc(context.Background(), scylla.Message{ID: 5, ChatID: 1, Content: &text, AlbumIDs: []gocql.UUID{gocql.UUID(photo), gocql.UUID(clip), gocql.UUID(photo)}})
	if strings.Join(album.Kinds, ",") != "image,video" || !album.HasLink {
		t.Fatalf("album with a link: %+v", album)
	}
	plain := "no link here"
	file := gocql.UUID(doc)
	single := in.doc(context.Background(), scylla.Message{ID: 6, ChatID: 1, Content: &plain, AttachmentID: &file})
	if strings.Join(single.Kinds, ",") != "file" || single.HasLink || single.FileName != "plan.pdf" {
		t.Fatalf("file without a link: %+v", single)
	}
}
