package main

import "testing"

func TestLoopbackListener(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8080": true,
		"[::1]:8080":     true,
		"localhost:8080": true,
		":8080":          false,
		"0.0.0.0:8080":   false,
		"10.0.0.5:8080":  false,
		"broken":         false,
	} {
		if got := loopbackListener(addr); got != want {
			t.Errorf("%q: got %v, want %v", addr, got, want)
		}
	}
}
