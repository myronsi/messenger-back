package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

// The contract check: every response the HTTP tests receive is validated against api/openapi.yaml. A status the
// operation does not list, a missing or extra body, or a body that breaks its schema fails the test.

type contractRoute struct {
	re     *regexp.Regexp
	path   string // the template, e.g. /chats/{chat_id}
	params int    // fewer parameters win when two templates match
}

type contract struct {
	doc      map[string]any
	routes   []contractRoute
	compiler *jsonschema.Compiler
	mu       sync.Mutex
	compiled map[string]*jsonschema.Schema
}

var (
	contractOnce sync.Once
	contractDoc  *contract
	contractErr  error
)

func loadContract() (*contract, error) {
	contractOnce.Do(func() {
		raw, err := os.ReadFile("../../api/openapi.yaml")
		if err != nil {
			contractErr = err
			return
		}
		var y any
		if err := yaml.Unmarshal(raw, &y); err != nil {
			contractErr = err
			return
		}
		// Through JSON so every map has string keys and numbers are what the validator expects.
		j, err := json.Marshal(normalizeYAML(y))
		if err != nil {
			contractErr = err
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(j))
		if err != nil {
			contractErr = err
			return
		}
		c := jsonschema.NewCompiler()
		c.DefaultDraft(jsonschema.Draft2020)
		c.AssertFormat()
		if err := c.AddResource("openapi.json", doc); err != nil {
			contractErr = err
			return
		}
		var m map[string]any
		_ = json.Unmarshal(j, &m)
		ct := &contract{doc: m, compiler: c, compiled: map[string]*jsonschema.Schema{}}
		for p := range m["paths"].(map[string]any) {
			ct.routes = append(ct.routes, contractRoute{re: regexp.MustCompile(convertParams(p)), path: p, params: strings.Count(p, "{")})
		}
		sort.Slice(ct.routes, func(i, k int) bool { return ct.routes[i].params < ct.routes[k].params })
		contractDoc = ct
	})
	return contractDoc, contractErr
}

// convertParams turns /chats/{chat_id}/pin into ^/chats/[^/]+/pin$.
func convertParams(p string) string {
	var b strings.Builder
	b.WriteString("^")
	for _, seg := range strings.Split(p, "/") {
		if seg == "" {
			continue
		}
		b.WriteString("/")
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			b.WriteString(`[^/]+`)
		} else {
			b.WriteString(regexp.QuoteMeta(seg))
		}
	}
	b.WriteString("$")
	return b.String()
}

func normalizeYAML(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = normalizeYAML(e)
		}
		return x
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[fmt.Sprint(k)] = normalizeYAML(e)
		}
		return m
	case []any:
		for i, e := range x {
			x[i] = normalizeYAML(e)
		}
	}
	return v
}

// pointer escapes a JSON pointer token.
func pointer(s string) string { return strings.NewReplacer("~", "~0", "/", "~1").Replace(s) }

// resolve follows a local $ref ("#/components/...") in the document.
func (c *contract) resolve(v any) (map[string]any, string) {
	m, _ := v.(map[string]any)
	ref, ok := m["$ref"].(string)
	if !ok {
		return m, ""
	}
	var cur any = c.doc
	for _, tok := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		tok = strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
		cur = cur.(map[string]any)[tok]
	}
	out, _ := cur.(map[string]any)
	return out, strings.TrimPrefix(ref, "#")
}

func (c *contract) schema(loc string) (*jsonschema.Schema, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.compiled[loc]; ok {
		return s, nil
	}
	s, err := c.compiler.Compile("openapi.json#" + loc)
	if err != nil {
		return nil, err
	}
	c.compiled[loc] = s
	return s, nil
}

// operationFor finds the operation of a request path below the API base path.
func (c *contract) operationFor(method, path string) (string, map[string]any) {
	for _, r := range c.routes {
		if r.re.MatchString(path) {
			ops := c.doc["paths"].(map[string]any)[r.path].(map[string]any)
			if op, ok := ops[strings.ToLower(method)].(map[string]any); ok {
				return r.path, op
			}
		}
	}
	return "", nil
}

// checkContract validates one response; it is called for every request the tests make through authEnv.do.
func checkContract(t *testing.T, method, rawPath string, status int, contentType string, body []byte) {
	t.Helper()
	if err := contractViolation(method, rawPath, status, contentType, body); err != nil {
		t.Errorf("contract: %v", err)
	}
}

// contractViolation describes how a response breaks the contract, or is nil.
func contractViolation(method, rawPath string, status int, contentType string, body []byte) error {
	c, err := loadContract()
	if err != nil {
		return err
	}
	path, _, _ := strings.Cut(rawPath, "?")
	path = strings.TrimPrefix(path, apiBase)
	tmpl, op := c.operationFor(method, path)
	if op == nil {
		return nil // not part of the contract (the WebSocket upgrade, ops endpoints)
	}
	responses := op["responses"].(map[string]any)
	resp, ok := responses[strconv.Itoa(status)]
	loc := "/paths/" + pointer(tmpl) + "/" + strings.ToLower(method) + "/responses/" + strconv.Itoa(status)
	if !ok {
		// 500 and 503 are not listed per operation: they are the server failing, not the contract.
		if status >= 500 {
			return nil
		}
		return fmt.Errorf("%s %s answered %d, which the operation does not list", method, tmpl, status)
	}
	r, ref := c.resolve(resp)
	if ref != "" {
		loc = ref
	}
	content, _ := r["content"].(map[string]any)
	if len(content) == 0 {
		if len(bytes.TrimSpace(body)) > 0 {
			return fmt.Errorf("%s %s %d has a body, the contract none", method, tmpl, status)
		}
		return nil
	}
	mt, _, _ := mime.ParseMediaType(contentType)
	media, ok := content[mt].(map[string]any)
	if !ok {
		// Binary downloads (images, audio, default avatars) are described by their media ranges.
		for k := range content {
			if (strings.HasSuffix(k, "/*") && strings.HasPrefix(mt, strings.TrimSuffix(k, "*"))) || k == "*/*" || k == "application/octet-stream" {
				return nil
			}
		}
		return fmt.Errorf("%s %s %d answered %q, the contract has %v", method, tmpl, status, mt, keys(content))
	}
	if _, ok := media["schema"]; !ok || !strings.Contains(mt, "json") {
		return nil
	}
	s, err := c.schema(loc + "/content/" + pointer(mt) + "/schema")
	if err != nil {
		return fmt.Errorf("compile %s: %w", loc, err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s %s %d: body is not JSON: %w", method, tmpl, status, err)
	}
	if err := s.Validate(inst); err != nil {
		return fmt.Errorf("%s %s %d: %w (body: %s)", method, tmpl, status, err, truncateBody(body))
	}
	return nil
}

// The check itself must catch what it is there for.
func TestContractCheckRejectsViolations(t *testing.T) {
	cases := []struct {
		name, method, path string
		status             int
		body               string
		ok                 bool
	}{
		{"valid meta", "GET", apiBase + "/meta", 200, `{"backend_version":"1","commit":"x","api_version":"2.0.0","min_client_api_version":"2.0.0"}`, true},
		{"missing field", "GET", apiBase + "/meta", 200, `{"backend_version":"1"}`, false},
		{"unlisted status", "GET", apiBase + "/meta", 404, `{}`, false},
		{"wrong type", "GET", apiBase + "/chats/1", 200, `{"id":1}`, false},
		{"bad id pattern", "GET", apiBase + "/chats", 200, `{"items":[],"next_cursor":5}`, false},
		{"body on 204", "DELETE", apiBase + "/chats/1", 204, `{"x":1}`, false},
		{"server failure", "GET", apiBase + "/chats", 503, `{}`, true},
		{"outside the contract", "GET", "/healthz", 200, `{}`, true},
	}
	for _, c := range cases {
		err := contractViolation(c.method, c.path, c.status, "application/json", []byte(c.body))
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func truncateBody(b []byte) string {
	if len(b) > 600 {
		return string(b[:600]) + "…"
	}
	return string(b)
}
