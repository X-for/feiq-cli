package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"feiq-cli/internal/history"
	"feiq-cli/ipmsg"
)

type fakeWebSession struct {
	mu        sync.Mutex
	peers     []ipmsg.Peer
	discovery int
	messages  []string
	paths     []string
	messageCh chan struct{}
	pathCh    chan struct{}
	pathDone  chan struct{}
}

func (session *fakeWebSession) Discover() error {
	session.mu.Lock()
	session.discovery++
	session.mu.Unlock()
	return nil
}

func (session *fakeWebSession) SearchPeers(query string) []ipmsg.Peer {
	query = strings.ToLower(query)
	var result []ipmsg.Peer
	for _, peer := range session.peers {
		if query == "" || strings.Contains(strings.ToLower(peer.IP+peer.Name+peer.Host), query) {
			result = append(result, peer)
		}
	}
	return result
}

func (session *fakeWebSession) SendMessage(_ context.Context, target, message string) (bool, error) {
	session.mu.Lock()
	session.messages = append(session.messages, target+"\x00"+message)
	session.mu.Unlock()
	if session.messageCh != nil {
		close(session.messageCh)
	}
	return true, nil
}

func (session *fakeWebSession) SendPath(_ context.Context, target, path string) error {
	session.mu.Lock()
	session.paths = append(session.paths, target+"\x00"+path)
	session.mu.Unlock()
	if session.pathCh != nil {
		close(session.pathCh)
	}
	if session.pathDone != nil {
		<-session.pathDone
	}
	return nil
}

func newTestWebApp(t *testing.T, session *fakeWebSession) *webApp {
	t.Helper()
	store, err := history.Open(filepath.Join(t.TempDir(), "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return &webApp{
		ctx:          context.Background(),
		session:      session,
		history:      store,
		hub:          newWebHub(),
		outputDir:    t.TempDir(),
		messageWait:  time.Second,
		transferWait: time.Second,
	}
}

func TestAPIStatusAndSecurityHeaders(t *testing.T) {
	app := newTestWebApp(t, &fakeWebSession{})
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	response := httptest.NewRecorder()
	app.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready":true`) {
		t.Fatalf("unexpected API response: status=%d body=%q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
		t.Fatalf("missing CSP: %q", got)
	}
}

func TestWebContactsMergeOnlineAndHistory(t *testing.T) {
	session := &fakeWebSession{peers: []ipmsg.Peer{{IP: "192.168.1.2", Name: "在线昵称", Host: "desktop", LastSeen: time.Now()}}}
	app := newTestWebApp(t, session)
	if err := app.history.Append(history.Entry{PeerIP: "192.168.1.2", PeerName: "旧昵称", Direction: "in", Kind: "msg", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/contacts", nil)
	response := httptest.NewRecorder()
	app.routes().ServeHTTP(response, request)
	var contacts []webContact
	if err := json.Unmarshal(response.Body.Bytes(), &contacts); err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 1 || !contacts[0].Online || contacts[0].Name != "在线昵称" || contacts[0].Count != 1 {
		t.Fatalf("unexpected contacts: %#v", contacts)
	}
}

func TestWebMessageAPIKeepsMultilineAndSymbols(t *testing.T) {
	session := &fakeWebSession{messageCh: make(chan struct{})}
	app := newTestWebApp(t, session)
	want := "第一行\n第二行 \\ \""
	body, err := json.Marshal(map[string]string{"to": "192.168.1.2", "text": want})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewReader(body))
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Origin", "http://127.0.0.1:8080")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.routes().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case <-session.messageCh:
	case <-time.After(time.Second):
		t.Fatal("message send did not start")
	}
	session.mu.Lock()
	got := session.messages[0]
	session.mu.Unlock()
	if got != "192.168.1.2\x00"+want {
		t.Fatalf("message changed: %q", got)
	}
	entries, err := app.history.History("192.168.1.2", 10)
	if err != nil || len(entries) != 1 || entries[0].Content != want {
		t.Fatalf("unexpected history: entries=%#v err=%v", entries, err)
	}
}

func TestWebDirectoryUploadPreservesTreeAndCleansUp(t *testing.T) {
	session := &fakeWebSession{pathCh: make(chan struct{}), pathDone: make(chan struct{})}
	app := newTestWebApp(t, session)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("to", "192.168.1.2")
	_ = writer.WriteField("kind", "dir")
	_ = writer.WriteField("paths", "album/a.txt")
	part, err := writer.CreateFormFile("files", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, "content")
	_ = writer.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	app.routes().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case <-session.pathCh:
	case <-time.After(time.Second):
		t.Fatal("directory send did not start")
	}
	session.mu.Lock()
	sent := strings.SplitN(session.paths[0], "\x00", 2)[1]
	session.mu.Unlock()
	if content, err := os.ReadFile(filepath.Join(sent, "a.txt")); err != nil || string(content) != "content" {
		t.Fatalf("directory tree was not preserved: content=%q err=%v", content, err)
	}
	close(session.pathDone)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Dir(sent)); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("temporary upload directory was not removed")
}

func TestWebRejectsCrossOriginMutation(t *testing.T) {
	app := newTestWebApp(t, &fakeWebSession{})
	request := httptest.NewRequest(http.MethodPost, "/api/discover", nil)
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Origin", "https://example.com")
	response := httptest.NewRecorder()
	app.routes().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin request status=%d", response.Code)
	}
}

func TestSafeUploadPath(t *testing.T) {
	root := t.TempDir()
	for _, unsafe := range []string{"../secret", "/tmp/secret", "folder/../../secret"} {
		if _, err := safeUploadPath(root, unsafe); err == nil {
			t.Fatalf("%q should be rejected", unsafe)
		}
	}
	got, err := safeUploadPath(root, "folder/file.txt")
	if err != nil || got != filepath.Join(root, "folder/file.txt") {
		t.Fatalf("safe path=%q err=%v", got, err)
	}
}

func TestValidateWebListenRequiresExplicitRemoteAccess(t *testing.T) {
	if err := validateWebListen("127.0.0.1:8080", false); err != nil {
		t.Fatalf("localhost should be accepted: %v", err)
	}
	if err := validateWebListen("0.0.0.0:8080", false); err == nil {
		t.Fatal("remote listener should require --allow-remote")
	}
	if err := validateWebListen("0.0.0.0:8080", true); err != nil {
		t.Fatalf("explicit remote listener should be accepted: %v", err)
	}
}

func TestConfiguredOriginGetsCORSAccess(t *testing.T) {
	app := newTestWebApp(t, &fakeWebSession{})
	app.allowOrigin = "http://127.0.0.1:5173"
	request := httptest.NewRequest(http.MethodOptions, "/api/messages", nil)
	request.Header.Set("Origin", app.allowOrigin)
	response := httptest.NewRecorder()
	app.routes().ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != app.allowOrigin {
		t.Fatalf("CORS preflight failed: status=%d headers=%v", response.Code, response.Header())
	}
}

func TestValidateAllowedOrigin(t *testing.T) {
	for _, valid := range []string{"", "http://127.0.0.1:5173", "https://web.example"} {
		if err := validateAllowedOrigin(valid); err != nil {
			t.Fatalf("valid origin %q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"127.0.0.1:5173", "file:///tmp/ui", "http://host/path", "http://user@host"} {
		if err := validateAllowedOrigin(invalid); err == nil {
			t.Fatalf("invalid origin %q was accepted", invalid)
		}
	}
}
