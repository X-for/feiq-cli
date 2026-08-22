package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	return newTestWebAppAtRoot(t, session, t.TempDir())
}

func newTestWebAppAtRoot(t *testing.T, session *fakeWebSession, root string) *webApp {
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
		paths:        newPathAccess([]string{root}),
		messageWait:  time.Second,
		transferWait: time.Second,
	}
}

func TestWebPathsListsDefaultRootAndChildDirectory(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "documents")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "message.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := newTestWebAppAtRoot(t, &fakeWebSession{}, root)

	response := httptest.NewRecorder()
	app.routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/paths", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("default root status=%d body=%s", response.Code, response.Body.String())
	}
	var rootListing pathListing
	if err := json.Unmarshal(response.Body.Bytes(), &rootListing); err != nil {
		t.Fatal(err)
	}
	if rootListing.Path != root || rootListing.Root != root || len(rootListing.Roots) != 1 || rootListing.Roots[0] != root {
		t.Fatalf("unexpected root listing: %#v", rootListing)
	}
	if len(rootListing.Entries) != 1 || rootListing.Entries[0].Path != child || rootListing.Entries[0].Kind != "dir" {
		t.Fatalf("unexpected root entries: %#v", rootListing.Entries)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/paths?path="+url.QueryEscape(child), nil)
	response = httptest.NewRecorder()
	app.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("child status=%d body=%s", response.Code, response.Body.String())
	}
	var childListing pathListing
	if err := json.Unmarshal(response.Body.Bytes(), &childListing); err != nil {
		t.Fatal(err)
	}
	if childListing.Path != child || childListing.Parent != root || len(childListing.Entries) != 1 || childListing.Entries[0].Name != "message.txt" {
		t.Fatalf("unexpected child listing: %#v", childListing)
	}
}

func TestWebPathsRejectOutsideRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	app := newTestWebAppAtRoot(t, &fakeWebSession{}, root)
	request := httptest.NewRequest(http.MethodGet, "/api/paths?path="+url.QueryEscape(outside), nil)
	response := httptest.NewRecorder()
	app.routes().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebSendPathRejectsOutsideRootAndAllowsInside(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	allowed := filepath.Join(root, "allowed.txt")
	blocked := filepath.Join(outside, "blocked.txt")
	for _, path := range []string{allowed, blocked} {
		if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	session := &fakeWebSession{pathCh: make(chan struct{})}
	app := newTestWebAppAtRoot(t, session, root)

	body, _ := json.Marshal(map[string]string{"to": "192.168.1.2", "path": blocked, "kind": "file"})
	response := httptest.NewRecorder()
	app.routes().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/send-path", bytes.NewReader(body)))
	if response.Code != http.StatusForbidden {
		t.Fatalf("outside status=%d body=%s", response.Code, response.Body.String())
	}

	body, _ = json.Marshal(map[string]string{"to": "192.168.1.2", "path": allowed, "kind": "file"})
	response = httptest.NewRecorder()
	app.routes().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/send-path", bytes.NewReader(body)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("inside status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case <-session.pathCh:
	case <-time.After(time.Second):
		t.Fatal("allowed path send did not start")
	}
	session.mu.Lock()
	sent := session.paths[0]
	session.mu.Unlock()
	if sent != "192.168.1.2\x00"+allowed {
		t.Fatalf("unexpected sent path: %q", sent)
	}
}

func TestWebSendPathUsesActualPathKind(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	session := &fakeWebSession{pathCh: make(chan struct{})}
	app := newTestWebAppAtRoot(t, session, root)

	// A manually entered path may carry a stale browser-side kind. The server
	// can inspect the path itself, so it must not reject an otherwise valid path.
	body, _ := json.Marshal(map[string]string{"to": "192.168.110.150", "path": directory, "kind": "file"})
	response := httptest.NewRecorder()
	app.routes().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/send-path", bytes.NewReader(body)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case <-session.pathCh:
	case <-time.After(time.Second):
		t.Fatal("directory send did not start")
	}
}

func TestWebUploadRemoved(t *testing.T) {
	app := newTestWebApp(t, &fakeWebSession{})
	response := httptest.NewRecorder()
	app.routes().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/upload", nil))
	if response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
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

func TestWebRoutesServeFrontendAndKeepAPI(t *testing.T) {
	app := newTestWebApp(t, &fakeWebSession{})
	frontend := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "embedded frontend")
	})

	pageResponse := httptest.NewRecorder()
	app.routes(frontend).ServeHTTP(pageResponse, httptest.NewRequest(http.MethodGet, "/", nil))
	if pageResponse.Code != http.StatusOK || pageResponse.Body.String() != "embedded frontend" {
		t.Fatalf("unexpected frontend response: status=%d body=%q", pageResponse.Code, pageResponse.Body.String())
	}

	apiResponse := httptest.NewRecorder()
	app.routes(frontend).ServeHTTP(apiResponse, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if apiResponse.Code != http.StatusOK || !strings.Contains(apiResponse.Body.String(), `"ready":true`) {
		t.Fatalf("unexpected API response: status=%d body=%q", apiResponse.Code, apiResponse.Body.String())
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
	request.Host = "127.0.0.1:2426"
	request.Header.Set("Origin", "http://127.0.0.1:2426")
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

func TestWebRejectsCrossOriginMutation(t *testing.T) {
	app := newTestWebApp(t, &fakeWebSession{})
	request := httptest.NewRequest(http.MethodPost, "/api/discover", nil)
	request.Host = "127.0.0.1:2426"
	request.Header.Set("Origin", "https://example.com")
	response := httptest.NewRecorder()
	app.routes().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin request status=%d", response.Code)
	}
}

func TestValidateWebListenRequiresExplicitRemoteAccess(t *testing.T) {
	if err := validateWebListen("127.0.0.1:2426", false); err != nil {
		t.Fatalf("localhost should be accepted: %v", err)
	}
	if err := validateWebListen("0.0.0.0:2426", false); err == nil {
		t.Fatal("remote listener should require --allow-remote")
	}
	if err := validateWebListen("0.0.0.0:2426", true); err != nil {
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
