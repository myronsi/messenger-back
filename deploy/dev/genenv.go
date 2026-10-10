//go:build ignore

// genenv creates .env from .env.example for the development stack: it fills the empty secrets with
// random values and expands ${NAME} references (so the host-side connection URLs carry the generated
// passwords). It never overwrites an existing .env.
//
//	go run deploy/dev/genenv.go
//	go run deploy/dev/genenv.go deploy/go/env.example /opt/messenger-go/.env   # the Go stack (staging)
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
)

// generators maps a variable to a function that produces its value.
var generators = map[string]func() string{
	"JWT_SECRET":        hexBytes(32),
	"RECOVERY_PEPPER":   hexBytes(32),
	"ENCRYPTION_KEY":    base64Bytes(32),
	"POSTGRES_PASSWORD": hexBytes(16),
	"REDIS_PASSWORD":    hexBytes(16),
	"ELASTIC_PASSWORD":  hexBytes(16),
	"KIBANA_PASSWORD":   hexBytes(16),
	"S3_ACCESS_KEY":     hexBytes(8),
	"S3_SECRET_KEY":     hexBytes(20),
}

var ref = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		fmt.Fprintln(os.Stderr, "random:", err)
		os.Exit(1)
	}
	return b
}

// Hex keeps the passwords safe inside connection URLs.
func hexBytes(n int) func() string {
	return func() string { return hex.EncodeToString(randomBytes(n)) }
}

func base64Bytes(n int) func() string {
	return func() string { return base64.StdEncoding.EncodeToString(randomBytes(n)) }
}

func leaveAlone(name string) {
	fmt.Printf("%s already exists, leaving it alone. To generate new secrets, wipe the data first\n"+
		"(docker compose -f compose.dev.yaml down --volumes), then delete it: the databases keep the credentials\n"+
		"they were first created with.\n", name)
}

func main() {
	src, dst := ".env.example", ".env"
	if len(os.Args) == 3 {
		src, dst = os.Args[1], os.Args[2]
	} else if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: go run deploy/dev/genenv.go [example env]")
		os.Exit(2)
	}
	if _, err := os.Stat(dst); err == nil {
		leaveAlone(dst)
		return
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	values := map[string]string{}
	for i, line := range lines {
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") || strings.ContainsAny(key, " \t") {
			continue
		}
		if gen, found := generators[key]; found && value == "" {
			value = gen()
			lines[i] = key + "=" + value
		}
		values[key] = value
	}
	// Expand references after every secret exists; references to unknown names stay as they are.
	for i, line := range lines {
		if line == "# COMPOSE_FILE=compose.dev.yaml" {
			lines[i] = "COMPOSE_FILE=compose.dev.yaml"
			continue
		}
		if !strings.HasPrefix(line, "#") {
			lines[i] = ref.ReplaceAllStringFunc(line, func(m string) string {
				if v, ok := values[ref.FindStringSubmatch(m)[1]]; ok {
					return v
				}
				return m
			})
		}
	}
	// O_EXCL keeps an .env created after the check above intact.
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		leaveAlone(dst)
		return
	}
	if err == nil {
		_, err = f.WriteString(strings.Join(lines, "\n"))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		if f != nil {
			// Do not leave a truncated .env behind: the next run would treat it as valid.
			_ = os.Remove(dst)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Created %s with generated secrets.\n", dst)
	if src == ".env.example" {
		fmt.Println("Next: make up")
	}
}
