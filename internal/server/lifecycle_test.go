package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

func TestRunListenFailure(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Run(ctx, newApp(t, defaultConfig(t), nil, nil), ln.Addr().String(), time.Second); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("listen failure did not propagate: %v", err)
	}
}

func TestRunCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, newApp(t, defaultConfig(t), nil, nil), "127.0.0.1:0", time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	app := newApp(t, defaultConfig(t), nil, nil)
	ready := make(chan string, 1)
	app.Hooks().OnListen(func(data fiber.ListenData) error { ready <- data.Host + ":" + data.Port; return nil })
	done := make(chan error, 1)
	go func() { done <- Run(ctx, app, "127.0.0.1:0", time.Second) }()
	address := <-ready
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHealthcheck(t *testing.T) {
	for _, status := range []int{200, 503, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if status == 302 {
					w.Header().Set("Location", "/health")
				}
				w.WriteHeader(status)
			}))
			defer srv.Close()
			u, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			err = Healthcheck(context.Background(), u.Port())
			if (err == nil) != (status == 200) {
				t.Fatalf("status=%d error=%v", status, err)
			}
		})
	}
	if err := Healthcheck(context.Background(), "not a port"); err == nil {
		t.Fatal("invalid URL accepted")
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strings.Split(ln.Addr().String(), ":")[1]
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Healthcheck(context.Background(), port); err == nil {
		t.Fatal("unreachable server accepted")
	}
}
