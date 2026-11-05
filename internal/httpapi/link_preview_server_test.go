package httpapi

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLinkPreviewOverHTTP(t *testing.T) {
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/post":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><head><title>Fallback</title>
				<meta property="og:title" content="A post"><meta property="og:description" content="What it is about">
				<meta property="og:site_name" content="Blog"><meta property="og:image" content="/cover.png">
				<meta property="og:image:width" content="4"><meta property="og:image:height" content="3"></head></html>`))
		case "/cover.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(picture.Bytes())
		case "/file.zip":
			w.Header().Set("Content-Type", "application/zip")
		}
	}))
	defer site.Close()
	e := newChatEnv(t)
	alice := e.register(t, "alice")
	get := func(path, token string) reply {
		return e.do(t, request{method: http.MethodGet, path: path, token: token})
	}

	r := get("/link-preview?url="+url.QueryEscape(site.URL+"/post"), alice.access)
	if r.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", r.Code, r.Body)
	}
	p := r.json()
	if p["title"] != "A post" || p["description"] != "What it is about" || p["site_name"] != "Blog" || p["url"] != site.URL+"/post" {
		t.Fatalf("preview: %v", p)
	}
	img := p["image"].(map[string]any)
	imageURL := img["url"].(string)
	if !strings.HasPrefix(imageURL, apiBase+"/link-preview/image?url=") || img["width"] != float64(4) || img["height"] != float64(3) {
		t.Fatalf("image: %v", img)
	}
	path := strings.TrimPrefix(imageURL, apiBase)
	pic := get(path, alice.access)
	if pic.Code != http.StatusOK || pic.Header().Get("Content-Type") != "image/png" || pic.Header().Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(pic.Body.Bytes(), picture.Bytes()) {
		t.Fatalf("proxied image: %d %v", pic.Code, pic.Header())
	}
	if r := get(path[:len(path)-2]+"xx", alice.access); r.Code != http.StatusForbidden {
		t.Fatalf("tampered signature: %d", r.Code)
	}
	if r := get(path, ""); r.Code != http.StatusUnauthorized {
		t.Fatalf("image without login: %d", r.Code)
	}
	if r := get("/link-preview?url="+url.QueryEscape(site.URL+"/post"), ""); r.Code != http.StatusUnauthorized {
		t.Fatalf("preview without login: %d", r.Code)
	}
	if r := get("/link-preview?url="+url.QueryEscape("javascript:alert(1)"), alice.access); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("javascript url: %d", r.Code)
	}
	if r := get("/link-preview?url="+url.QueryEscape(site.URL+"/file.zip"), alice.access); r.Code != http.StatusNotFound {
		t.Fatalf("not a page: %d", r.Code)
	}
}
