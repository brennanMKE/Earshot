package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServeHTTPDrainsActiveRequest(t *testing.T) {
	entered := make(chan struct{})
	finish := make(chan struct{})
	server, base, stop, done := startTestServer(t, time.Second, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
			t.Error("request canceled before the grace period ended")
		case <-finish:
			_, _ = io.WriteString(w, "finished")
		}
	}))
	response := startTestRequest(base)
	await(t, entered)
	stop()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", server, 20*time.Millisecond)
		if err != nil {
			break
		}
		conn.Close()
		time.Sleep(time.Millisecond)
	}
	if conn, err := net.DialTimeout("tcp", server, 20*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("listener still accepting connections during shutdown")
	}
	close(finish)
	select {
	case result := <-response:
		if result.err != nil || result.body != "finished" {
			t.Fatalf("request did not finish: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not return")
	}
	if err := shutdownResult(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestServeHTTPCancelsWhenGraceExpires(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	_, base, stop, done := startTestServer(t, 20*time.Millisecond, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	response := startTestRequest(base)
	await(t, entered)
	stop()
	await(t, canceled)
	if err := shutdownResult(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected grace timeout, got %v", err)
	}
	select {
	case result := <-response:
		if result.err == nil {
			t.Fatal("response finished after grace timeout")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not return")
	}
}

func startTestServer(t *testing.T, grace time.Duration, handler http.Handler) (string, string, context.CancelFunc, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	server := &http.Server{Handler: handler}
	t.Cleanup(func() {
		stop()
		_ = server.Close()
		_ = listener.Close()
	})
	done := make(chan error, 1)
	go func() { done <- serveHTTP(ctx, server, listener, grace) }()
	return listener.Addr().String(), "http://" + listener.Addr().String(), stop, done
}

type testResponse struct {
	body string
	err  error
}

func startTestRequest(url string) <-chan testResponse {
	done := make(chan testResponse, 1)
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Get(url)
		if err != nil {
			done <- testResponse{err: err}
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		done <- testResponse{body: string(body), err: err}
	}()
	return done
}

func await(t *testing.T, event <-chan struct{}) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for handler")
	}
}

func shutdownResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not return")
		return nil
	}
}
