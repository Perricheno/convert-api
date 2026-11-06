package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestConversionErrorCode(t *testing.T) {
	output := []byte("fail /tmp/private/input.pdf: This PDF has no tables drawn with lines, so there is nothing to put in a spreadsheet.")
	for _, target := range []string{"csv", "xlsx"} {
		if got := conversionErrorCode("pdf", target, output); got != "PDF_TABLES_NOT_FOUND" {
			t.Fatalf("%s: %q", target, got)
		}
	}
	if conversionErrorCode("pdf", "docx", output) != "" || conversionErrorCode("docx", "csv", output) != "" || conversionErrorCode("pdf", "csv", []byte("other failure")) != "" {
		t.Fatal("unrelated errors must not be misclassified")
	}
}

func TestCancelTerminatesConverterChildren(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := fileeCommand(ctx, "sh", "-c", `(sleep 1; echo survived > "$1") & wait`, "sh", marker)
	if err := cmd.Run(); err == nil {
		t.Fatal("expected cancellation to terminate the converter")
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("expected deadline exceeded, got %v", ctx.Err())
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("converter child survived cancellation: stat error %v", err)
	}
}

func TestShutdownWaitsForActiveRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "finished")
	})}
	signals := make(chan os.Signal, 1)
	stopped := make(chan error, 1)
	go func() { stopped <- serveUntilShutdown(server, listener, signals) }()
	response := make(chan error, 1)
	go func() {
		res, err := http.Get("http://" + listener.Addr().String())
		if err != nil {
			response <- err
			return
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err == nil && string(body) != "finished" {
			err = fmt.Errorf("unexpected response %q", body)
		}
		response <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	signals <- syscall.SIGTERM
	select {
	case err := <-stopped:
		t.Fatalf("server stopped before request finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-response:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish")
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
}
