package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

func serveHTTP(ctx context.Context, server *http.Server, listener net.Listener, grace time.Duration) error {
	requests, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	server.BaseContext = func(net.Listener) context.Context { return requests }
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		logger.Info("shutdown started")
	}

	deadline, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := server.Shutdown(deadline); err != nil {
		cancelRequests()
		_ = server.Close()
		<-done
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	logger.Info("shutdown complete")
	return nil
}
