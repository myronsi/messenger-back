package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/realtime"
)

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

func TestShutdownKeepsBackgroundWorkUntilRequestsFinish(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	gw, err := realtime.New(realtime.Options{Log: log})
	if err != nil {
		t.Fatal(err)
	}
	st := &realtimeStack{hub: realtime.NewHub(log, nil), gateway: gw}
	st.ctx, st.cancel = context.WithCancel(context.Background())
	released := make(chan struct{})
	st.done.Go(func() {
		<-st.ctx.Done()
		close(released)
	})

	started, finish := make(chan struct{}), make(chan struct{})
	stoppedEarly := make(chan bool, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-finish
		stoppedEarly <- st.ctx.Err() != nil
	}))
	srv.Start()
	defer srv.Close()
	go func() {
		if resp, err := http.Get(srv.URL); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	<-started

	result := make(chan error, 1)
	go func() {
		result <- shutdown(log, config.HTTP{ShutdownTimeout: 5 * time.Second}, srv.Config, func() {}, st, nil)
	}()
	select {
	case <-released:
		t.Fatal("background work stopped while a request was still running")
	case <-time.After(200 * time.Millisecond):
	}
	close(finish)
	if <-stoppedEarly {
		t.Fatal("the request saw the background work stopped")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
	default:
		t.Fatal("shutdown returned before the background work stopped")
	}
}
