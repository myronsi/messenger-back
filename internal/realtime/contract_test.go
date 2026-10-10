package realtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

// Every event the test clients receive is validated against api/websocket/server-events.schema.json (which
// refers to the schemas of api/openapi.yaml).

var (
	eventSchemaOnce sync.Once
	eventSchema     *jsonschema.Schema
	eventSchemaErr  error
)

// specLoader loads the schema files, YAML ones (openapi.yaml) through JSON.
type specLoader struct{}

func (specLoader) Load(u string) (any, error) {
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, err
	}
	path := parsed.Path
	if len(path) > 2 && path[0] == '/' && path[2] == ':' { // file:///C:/... on Windows
		path = path[1:]
	}
	raw, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, ".yaml") {
		var y any
		if err := yaml.Unmarshal(raw, &y); err != nil {
			return nil, err
		}
		if raw, err = json.Marshal(stringKeys(y)); err != nil {
			return nil, err
		}
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(raw))
}

func stringKeys(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = stringKeys(e)
		}
		return x
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[fmt.Sprint(k)] = stringKeys(e)
		}
		return m
	case []any:
		for i, e := range x {
			x[i] = stringKeys(e)
		}
	}
	return v
}

func serverEventSchema() (*jsonschema.Schema, error) {
	eventSchemaOnce.Do(func() {
		abs, err := filepath.Abs("../../api/websocket/server-events.schema.json")
		if err != nil {
			eventSchemaErr = err
			return
		}
		c := jsonschema.NewCompiler()
		c.UseLoader(specLoader{})
		c.AssertFormat()
		eventSchema, eventSchemaErr = c.Compile("file:///" + strings.TrimPrefix(filepath.ToSlash(abs), "/"))
	})
	return eventSchema, eventSchemaErr
}

// eventViolation describes how a server event breaks the contract, or is nil.
func eventViolation(raw []byte) error {
	s, err := serverEventSchema()
	if err != nil {
		return fmt.Errorf("event schema: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if err := s.Validate(inst); err != nil {
		return fmt.Errorf("%s: %w", truncate(raw), err)
	}
	return nil
}

func truncate(b []byte) string {
	if len(b) > 400 {
		return string(b[:400]) + "…"
	}
	return string(b)
}

func TestEventContractRejectsViolations(t *testing.T) {
	ok := `{"type":"typing","event_id":"1","chat_id":"5","data":{"user_id":"7","is_typing":true}}`
	if err := eventViolation([]byte(ok)); err != nil {
		t.Fatalf("valid typing event: %v", err)
	}
	for _, bad := range []string{
		`{"type":"typing","event_id":1,"chat_id":"5","data":{"user_id":"7","is_typing":true}}`,
		`{"type":"no_such_event","event_id":"1","chat_id":null,"data":{}}`,
		`{"type":"message","event_id":"1","chat_id":"5","data":{}}`,
	} {
		if eventViolation([]byte(bad)) == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
