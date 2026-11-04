package search

import (
	"testing"

	"github.com/gocql/gocql"

	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func TestExcerptFollowsTheCurrentCaption(t *testing.T) {
	file := gocql.TimeUUID()
	caption := "new caption"
	m := scylla.Message{Content: &caption, AttachmentID: &file}
	mark := func(s string) string { return MarkStart + s + MarkEnd }
	for name, tc := range map[string]struct {
		highlight map[string][]string
		message   scylla.Message
		want      bool
	}{
		"current caption":          {map[string][]string{"content": {mark("new") + " caption"}}, m, true},
		"edited caption":           {map[string][]string{"content": {"old " + mark("caption")}}, m, false},
		"file name":                {map[string][]string{"file_name": {mark("report") + ".pdf"}}, m, true},
		"file name of a text":      {map[string][]string{"file_name": {mark("report") + ".pdf"}}, scylla.Message{Content: &caption}, false},
		"caption removed":          {map[string][]string{"content": {mark("caption")}}, scylla.Message{AttachmentID: &file}, false},
		"stemmed caption excerpt":  {map[string][]string{"content.en": {mark("captions")}}, m, false},
		"no highlight":             {map[string][]string{}, m, false},
		"stale caption, file name": {map[string][]string{"content": {"gone"}, "file_name": {mark("report")}}, m, true},
	} {
		if got := current(tc.highlight, tc.message); (got != nil) != tc.want {
			t.Errorf("%s: got %v, want kept %v", name, got, tc.want)
		}
	}
}
