package realtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every example of the contract (api/websocket/examples/client) is accepted.
func TestDecodeAcceptsTheContractExamples(t *testing.T) {
	files, err := filepath.Glob("../../api/websocket/examples/client/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no examples: %v", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		ev, err := decodeClientEvent(raw)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		if want := strings.TrimSuffix(filepath.Base(f), ".json"); ev.Type != want {
			t.Errorf("%s decoded as %s", filepath.Base(f), ev.Type)
		}
	}
}

func TestDecodeRejectsWhatTheSchemasReject(t *testing.T) {
	long := strings.Repeat("ä", 4097)
	cases := map[string]string{
		"not json":                  `{`,
		"unknown envelope field":    `{"type":"read","client_temp_id":"a","chat_id":"1","data":{"message_id":"2"},"x":1}`,
		"unknown data field":        `{"type":"read","client_temp_id":"a","chat_id":"1","data":{"message_id":"2","y":1}}`,
		"numeric chat id":           `{"type":"read","client_temp_id":"a","chat_id":1,"data":{"message_id":"2"}}`,
		"leading zero id":           `{"type":"read","client_temp_id":"a","chat_id":"01","data":{"message_id":"2"}}`,
		"negative id":               `{"type":"read","client_temp_id":"a","chat_id":"-1","data":{"message_id":"2"}}`,
		"missing client_temp_id":    `{"type":"read","chat_id":"1","data":{"message_id":"2"}}`,
		"long client_temp_id":       `{"type":"read","client_temp_id":"` + strings.Repeat("x", 65) + `","chat_id":"1","data":{"message_id":"2"}}`,
		"typing with temp id":       `{"type":"typing","client_temp_id":"a","chat_id":"1","data":{"is_typing":true}}`,
		"typing without flag":       `{"type":"typing","chat_id":"1","data":{}}`,
		"text without content":      `{"type":"message","client_temp_id":"a","chat_id":"1","data":{"type":"text"}}`,
		"text with attachment":      `{"type":"message","client_temp_id":"a","chat_id":"1","data":{"type":"text","content":"x","attachment_id":"8b4d0b5e-35a1-4f34-9b2a-0d3c1c9a1d11"}}`,
		"file without attachment":   `{"type":"message","client_temp_id":"a","chat_id":"1","data":{"type":"file"}}`,
		"file with a url":           `{"type":"message","client_temp_id":"a","chat_id":"1","data":{"type":"file","attachment_id":"https://evil.example/x"}}`,
		"system message":            `{"type":"message","client_temp_id":"a","chat_id":"1","data":{"type":"system","content":"x"}}`,
		"too long":                  `{"type":"message","client_temp_id":"a","chat_id":"1","data":{"type":"text","content":"` + long + `"}}`,
		"bad scope":                 `{"type":"delete","client_temp_id":"a","chat_id":"1","data":{"message_id":"2","scope":"all"}}`,
		"empty emoji":               `{"type":"reaction_add","client_temp_id":"a","chat_id":"1","data":{"message_id":"2","emoji":""}}`,
		"unknown type":              `{"type":"shout","client_temp_id":"a","chat_id":"1","data":{}}`,
		"data is not an object":     `{"type":"read","client_temp_id":"a","chat_id":"1","data":[]}`,
		"trailing data":             `{"type":"read","client_temp_id":"a","chat_id":"1","data":{"message_id":"2"}} {}`,
		"invalid utf-8":             "{\"type\":\"read\",\"client_temp_id\":\"\xff\",\"chat_id\":\"1\",\"data\":{\"message_id\":\"2\"}}",
		"reply_to not an id":        `{"type":"message","client_temp_id":"a","chat_id":"1","data":{"type":"text","content":"x","reply_to":5}}`,
		"edit with empty content":   `{"type":"edit","client_temp_id":"a","chat_id":"1","data":{"message_id":"2","content":""}}`,
		"chat id too large for i64": `{"type":"read","client_temp_id":"a","chat_id":"99999999999999999999","data":{"message_id":"2"}}`,
	}
	for name, frame := range cases {
		_, err := decodeClientEvent([]byte(frame))
		var pe *protocolError
		if !errors.As(err, &pe) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	// A rejected event still names its client_temp_id, so the client can match the error.
	_, err := decodeClientEvent([]byte(`{"type":"delete","client_temp_id":"mine","chat_id":"7","data":{"message_id":"2","scope":"all"}}`))
	var pe *protocolError
	if !errors.As(err, &pe) || pe.ClientTempID != "mine" || pe.ChatID != 7 || pe.Code != "validation_failed" {
		t.Fatalf("error details: %+v", pe)
	}
}
