package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func withShortCommandTransport(t *testing.T, check func(*http.Request, string)) {
	t.Helper()
	previous := shortCommandHTTPClient
	shortCommandHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		check(request, string(body))
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Status:     "202 Accepted",
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"operation_id":"op-test"}`)),
		}, nil
	})}
	t.Cleanup(func() { shortCommandHTTPClient = previous })
}

func TestShortMessageUsesRunningAPI(t *testing.T) {
	withShortCommandTransport(t, func(request *http.Request, body string) {
		if request.URL.String() != "http://127.0.0.1:2426/api/messages" {
			t.Fatalf("URL = %s", request.URL)
		}
		if body != `{"text":"hello from shell","to":"192.168.110.150"}` {
			t.Fatalf("body = %s", body)
		}
	})
	if err := shortMessage([]string{"192.168.110.150", "hello", "from", "shell"}); err != nil {
		t.Fatal(err)
	}
}

func TestShortPathUsesAbsolutePathAndKind(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sample file.txt")
	if err := os.WriteFile(file, []byte("safe test"), 0o600); err != nil {
		t.Fatal(err)
	}
	withShortCommandTransport(t, func(request *http.Request, body string) {
		if request.URL.Path != "/api/send-path" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if !strings.Contains(body, `"kind":"file"`) || !strings.Contains(body, `"path":"`+file+`"`) {
			t.Fatalf("body = %s", body)
		}
	})
	if err := shortPath([]string{"192.168.110.150", file}, false); err != nil {
		t.Fatal(err)
	}
}

func TestShortDirectoryUsesDirectoryKind(t *testing.T) {
	directory := t.TempDir()
	withShortCommandTransport(t, func(_ *http.Request, body string) {
		if !strings.Contains(body, `"kind":"dir"`) || !strings.Contains(body, `"path":"`+directory+`"`) {
			t.Fatalf("body = %s", body)
		}
	})
	if err := shortPath([]string{"192.168.110.150", directory}, true); err != nil {
		t.Fatal(err)
	}
}

func TestShortCommandReportsAPIError(t *testing.T) {
	previous := shortCommandHTTPClient
	shortCommandHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Status:     "403 Forbidden",
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":"path is outside configured web roots"}`)),
		}, nil
	})}
	t.Cleanup(func() { shortCommandHTTPClient = previous })
	err := shortMessage([]string{"192.168.110.150", "hello"})
	if err == nil || !strings.Contains(err.Error(), "outside configured web roots") {
		t.Fatalf("err = %v", err)
	}
}
