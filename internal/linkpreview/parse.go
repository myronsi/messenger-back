package linkpreview

import (
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	maxTitle       = 300
	maxDescription = 500
	maxSiteName    = 100
)

type page struct {
	title, description, siteName, image string
	width, height                       int
	docTitle, metaDescription, twitter  string
}

func parse(r io.Reader, base *url.URL) Preview {
	var p page
	z := html.NewTokenizer(r)
	inTitle := false
loop:
	for {
		switch z.Next() {
		case html.ErrorToken:
			break loop
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			switch atom.Lookup(name) {
			case atom.Body:
				break loop
			case atom.Title:
				inTitle = true
			case atom.Meta:
				if hasAttr {
					p.meta(z)
				}
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch atom.Lookup(name) {
			case atom.Head:
				break loop
			case atom.Title:
				inTitle = false
			}
		case html.TextToken:
			if inTitle && p.docTitle == "" {
				p.docTitle = string(z.Text())
			}
		}
	}
	out := Preview{
		Title:       clip(first(p.title, p.docTitle), maxTitle),
		Description: clip(first(p.description, p.metaDescription), maxDescription),
		SiteName:    clip(p.siteName, maxSiteName),
	}
	if image := resolve(base, first(p.image, p.twitter)); image != "" {
		out.Image = &Image{URL: image, Width: p.width, Height: p.height}
	}
	return out
}

func (p *page) meta(z *html.Tokenizer) {
	var key, content string
	for {
		k, v, more := z.TagAttr()
		switch strings.ToLower(string(k)) {
		case "property", "name":
			if key == "" {
				key = strings.ToLower(strings.TrimSpace(string(v)))
			}
		case "content":
			content = strings.TrimSpace(string(v))
		}
		if !more {
			break
		}
	}
	if content == "" {
		return
	}
	set := func(dst *string) {
		if *dst == "" {
			*dst = content
		}
	}
	switch key {
	case "og:title":
		set(&p.title)
	case "og:description":
		set(&p.description)
	case "og:site_name":
		set(&p.siteName)
	case "og:image", "og:image:url", "og:image:secure_url":
		set(&p.image)
	case "og:image:width":
		p.width = dimension(content)
	case "og:image:height":
		p.height = dimension(content)
	case "twitter:image", "twitter:image:src":
		set(&p.twitter)
	case "description":
		set(&p.metaDescription)
	}
}

func dimension(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 20000 {
		return 0
	}
	return n
}

func first(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func clip(s string, limit int) string {
	s = strings.Join(strings.Fields(strings.ToValidUTF8(s, "")), " ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:limit-1])) + "…"
}

func resolve(base *url.URL, ref string) string {
	if ref == "" {
		return ""
	}
	u, err := base.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	u.Fragment = ""
	return u.String()
}
