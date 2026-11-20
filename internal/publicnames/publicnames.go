package publicnames

import (
	"crypto/rand"
	"regexp"
	"strings"
)

var format = regexp.MustCompile(`^[A-Za-z0-9_]{3,32}$`)

var reserved = map[string]bool{
	"admin": true, "administrator": true, "all": true, "api": true, "chat": true, "deleted": true, "dm": true,
	"docs": true, "emoji": true, "help": true, "join": true, "login": true, "me": true, "messenger": true,
	"moderator": true, "official": true, "profile": true, "qr": true, "register": true, "root": true,
	"settings": true, "staff": true, "stickers": true, "support": true, "system": true,
}

func Valid(name string) bool { return format.MatchString(name) }

func Reserved(name string) bool { return reserved[strings.ToLower(name)] }

const tokenAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

const TokenLength = 22

var tokenFormat = regexp.MustCompile(`^[A-Za-z0-9]{22}$`)

func ValidToken(token string) bool { return tokenFormat.MatchString(token) }

func NewToken() string {
	out := make([]byte, TokenLength)
	buf := make([]byte, 1)
	for i := 0; i < TokenLength; {
		if _, err := rand.Read(buf); err != nil {
			panic(err)
		}
		if int(buf[0]) >= 256-256%len(tokenAlphabet) {
			continue
		}
		out[i] = tokenAlphabet[int(buf[0])%len(tokenAlphabet)]
		i++
	}
	return string(out)
}
