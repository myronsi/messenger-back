package scylla

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestGetManyAndHiddenOf(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	chat, other := newChat(), newChat()
	sender := int64(1)
	all := seed(t, r, chat, sender, 30, time.Now().Add(-60*24*time.Hour), 36*time.Hour)
	for i, v1 := range []int64{201, 202} {
		if err := r.Insert(ctx, Message{ChatID: chat, ID: v1, SenderID: &sender, Type: TypeText, Content: text("v1"), CreatedAt: time.Now().Add(-time.Duration(400+i) * 24 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	elsewhere := seed(t, r, other, sender, 1, time.Now(), time.Second)
	if err := r.Delete(ctx, chat, all[3]); err != nil {
		t.Fatal(err)
	}
	wanted := append(slices.Clone(all), 201, 202, 203, elsewhere[0], all[0])
	got, err := r.GetMany(ctx, chat, wanted)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(all)+2 {
		t.Fatalf("got %d messages, want %d", len(got), len(all)+2)
	}
	for _, id := range append(slices.Clone(all), 201, 202) {
		m, ok := got[id]
		if !ok || m.ChatID != chat || m.ID != id {
			t.Fatalf("message %d: %+v %v", id, m, ok)
		}
	}
	if !got[all[3]].Deleted || got[all[3]].Content != nil || *got[all[5]].Content != "5" || *got[201].Content != "v1" {
		t.Fatalf("contents: %+v %+v %+v", got[all[3]], got[all[5]], got[201])
	}
	if empty, err := r.GetMany(ctx, chat, nil); err != nil || len(empty) != 0 {
		t.Fatalf("no ids: %v %v", empty, err)
	}

	viewer := int64(77)
	for _, id := range []int64{all[2], all[20], 201} {
		if err := r.Hide(ctx, viewer, chat, id, HiddenDeletedForMe); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Hide(ctx, viewer, chat, all[10], HiddenNotDelivered); err != nil {
		t.Fatal(err)
	}
	hidden, err := r.HiddenOf(ctx, viewer, chat, wanted)
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden) != 4 || !hidden[all[2]] || !hidden[all[10]] || !hidden[all[20]] || !hidden[201] {
		t.Fatalf("hidden: %v", hidden)
	}
	if h, err := r.HiddenOf(ctx, viewer+1, chat, wanted); err != nil || len(h) != 0 {
		t.Fatalf("other viewer: %v %v", h, err)
	}
	many := make([]int64, 0, 250)
	for i := range 250 {
		many = append(many, all[0]+int64(i))
	}
	many = append(many, all[20])
	if h, err := r.HiddenOf(ctx, viewer, chat, many); err != nil || !h[all[20]] {
		t.Fatalf("more ids than one IN list: %v %v", h, err)
	}
}
