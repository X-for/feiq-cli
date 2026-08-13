package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"feiq-cli/internal/history"
	"feiq-cli/ipmsg"
	webassets "feiq-cli/web"
)

type webSession interface {
	Discover() error
	SearchPeers(query string) []ipmsg.Peer
	SendMessage(ctx context.Context, target, message string) (bool, error)
	SendPath(ctx context.Context, target, path string) error
}

type webEvent struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	Time        time.Time `json:"time"`
	Direction   string    `json:"direction,omitempty"`
	PeerIP      string    `json:"peer_ip,omitempty"`
	PeerName    string    `json:"peer_name,omitempty"`
	Kind        string    `json:"kind,omitempty"`
	Content     string    `json:"content,omitempty"`
	SavedPath   string    `json:"saved_path,omitempty"`
	OperationID string    `json:"operation_id,omitempty"`
	Status      string    `json:"status,omitempty"`
	Error       string    `json:"error,omitempty"`
}

type webHub struct {
	mu      sync.Mutex
	nextID  uint64
	clients map[chan webEvent]struct{}
}

func newWebHub() *webHub {
	return &webHub{clients: make(map[chan webEvent]struct{})}
}

func (hub *webHub) publish(event webEvent) {
	hub.mu.Lock()
	hub.nextID++
	if event.ID == "" {
		event.ID = strconv.FormatUint(hub.nextID, 10)
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	for client := range hub.clients {
		select {
		case client <- event:
		default:
		}
	}
	hub.mu.Unlock()
}

func (hub *webHub) subscribe() (<-chan webEvent, func()) {
	client := make(chan webEvent, 64)
	hub.mu.Lock()
	hub.clients[client] = struct{}{}
	hub.mu.Unlock()
	return client, func() {
		hub.mu.Lock()
		delete(hub.clients, client)
		close(client)
		hub.mu.Unlock()
	}
}

type webApp struct {
	ctx          context.Context
	session      webSession
	history      *history.Store
	hub          *webHub
	outputDir    string
	paths        *pathAccess
	messageWait  time.Duration
	transferWait time.Duration
	allowOrigin  string
}

func apiMode(args []string) error {
	return httpMode(args, nil)
}

func webMode(args []string) error {
	return httpMode(args, webassets.Handler())
}

func httpMode(args []string, frontend http.Handler) error {
	config, configPath, err := loadAppConfig(args)
	if err != nil {
		return err
	}
	command := "api"
	if frontend != nil {
		command = "web"
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	var common commonFlags
	common.add(flags, config)
	addConfigFlag(flags, configPath)
	listen := flags.String("listen", "127.0.0.1:2426", "HTTP API listen address")
	allowRemote := flags.Bool("allow-remote", false, "allow the HTTP service to listen beyond localhost (no authentication)")
	allowOrigin := flags.String("allow-origin", "", "optional browser origin allowed to call the API, for example http://127.0.0.1:5173")
	output := flags.String("output", configString(config.Output, "./downloads"), "directory for received files and directories")
	historyPath := flags.String("history-file", configString(config.HistoryFile, history.DefaultPath()), "local JSONL chat history file")
	messageWait := flags.Duration("message-wait", configDuration(config.MessageWait, 5*time.Second), "message acknowledgement timeout")
	transferWait := flags.Duration("transfer-wait", configDuration(config.TransferWait, 5*time.Minute), "attachment offer timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validateWebListen(*listen, *allowRemote); err != nil {
		return err
	}
	if err := validateAllowedOrigin(*allowOrigin); err != nil {
		return err
	}
	roots, err := configuredWebRoots(config)
	if err != nil {
		return fmt.Errorf("configure Web path roots: %w", err)
	}
	store, err := history.Open(*historyPath)
	if err != nil {
		return err
	}
	absOutput, err := filepath.Abs(*output)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hub := newWebHub()
	app := &webApp{
		ctx:          ctx,
		history:      store,
		hub:          hub,
		outputDir:    absOutput,
		paths:        newPathAccess(roots),
		messageWait:  *messageWait,
		transferWait: *transferWait,
		allowOrigin:  strings.TrimSuffix(*allowOrigin, "/"),
	}
	session, err := common.node().StartSession(ctx, absOutput, func(event ipmsg.ReceiveEvent) {
		app.receive(event)
	}, func(err error) {
		hub.publish(webEvent{Type: "error", Error: err.Error()})
	})
	if err != nil {
		return err
	}
	defer session.Close()
	app.session = session

	server := &http.Server{
		Addr:              *listen,
		Handler:           app.routes(frontend),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("listen HTTP service on %s: %w", *listen, err)
	}
	label := "API"
	if frontend != nil {
		label = "Web UI"
	}
	fmt.Printf("feiq-cli %s: http://%s\n", label, browserAddress(listener.Addr()))
	fmt.Printf("IPMsg: %s:%d; attachments: %s\n", common.bind, common.port, absOutput)

	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func validateAllowedOrigin(origin string) error {
	if origin == "" {
		return nil
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("--allow-origin must be an HTTP origin such as http://127.0.0.1:5173")
	}
	return nil
}

func validateWebListen(address string, allowRemote bool) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid --listen address: %w", err)
	}
	if port == "" {
		return fmt.Errorf("--listen port is required")
	}
	if host == "" {
		return fmt.Errorf("--listen host is required; use 127.0.0.1 for local access")
	}
	parsed := net.ParseIP(host)
	local := strings.EqualFold(host, "localhost") || parsed != nil && parsed.IsLoopback()
	if !local && !allowRemote {
		return fmt.Errorf("refusing non-local HTTP listener %s without --allow-remote; the service has no authentication", address)
	}
	return nil
}

func browserAddress(address net.Addr) string {
	host, port, err := net.SplitHostPort(address.String())
	if err != nil {
		return address.String()
	}
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func (app *webApp) routes(frontend ...http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", app.handleStatus)
	mux.HandleFunc("GET /api/contacts", app.handleContacts)
	mux.HandleFunc("POST /api/discover", app.sameOrigin(app.handleDiscover))
	mux.HandleFunc("GET /api/history", app.handleHistory)
	mux.HandleFunc("GET /api/events", app.handleEvents)
	mux.HandleFunc("POST /api/messages", app.sameOrigin(app.handleMessage))
	mux.HandleFunc("GET /api/paths", app.handlePaths)
	mux.HandleFunc("POST /api/send-path", app.sameOrigin(app.handleSendPath))
	mux.HandleFunc("GET /api/download", app.handleDownload)
	if len(frontend) > 0 && frontend[0] != nil {
		mux.Handle("GET /", frontend[0])
	}
	return securityHeaders(app.cors(mux))
}

func (app *webApp) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		origin := strings.TrimSuffix(request.Header.Get("Origin"), "/")
		if app.allowOrigin != "" && origin == app.allowOrigin {
			writer.Header().Set("Access-Control-Allow-Origin", app.allowOrigin)
			writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			writer.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			writer.Header().Set("Vary", "Origin")
		}
		if request.Method == http.MethodOptions {
			if origin != "" && origin != app.allowOrigin {
				writeAPIError(writer, http.StatusForbidden, "cross-origin request rejected")
				return
			}
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; connect-src 'self'")
		next.ServeHTTP(writer, request)
	})
}

func (app *webApp) sameOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		origin := strings.TrimSuffix(request.Header.Get("Origin"), "/")
		sameOrigin := origin == "http://"+request.Host || origin == "https://"+request.Host
		if origin != "" && !sameOrigin && origin != app.allowOrigin {
			writeAPIError(writer, http.StatusForbidden, "cross-origin request rejected")
			return
		}
		next(writer, request)
	}
}

func (app *webApp) handleStatus(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"ready":      true,
		"output_dir": app.outputDir,
		"history":    app.history.Path(),
	})
}

type webContact struct {
	IP       string    `json:"ip"`
	Name     string    `json:"name"`
	Host     string    `json:"host,omitempty"`
	Online   bool      `json:"online"`
	LastSeen time.Time `json:"last_seen,omitempty"`
	Count    int       `json:"count,omitempty"`
}

func (app *webApp) handleContacts(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query().Get("q")
	local, err := app.history.SearchUsers(query)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	contacts := make(map[string]webContact)
	for _, user := range local {
		contacts[user.IP] = webContact{IP: user.IP, Name: user.Name, LastSeen: user.LastSeen, Count: user.Count}
	}
	for _, peer := range app.session.SearchPeers(query) {
		item := contacts[peer.IP]
		item.IP, item.Name, item.Host, item.Online, item.LastSeen = peer.IP, peer.Name, peer.Host, true, peer.LastSeen
		contacts[peer.IP] = item
	}
	result := make([]webContact, 0, len(contacts))
	for _, item := range contacts {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Online != result[j].Online {
			return result[i].Online
		}
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].IP < result[j].IP
	})
	writeJSON(writer, http.StatusOK, result)
}

func (app *webApp) handleDiscover(writer http.ResponseWriter, _ *http.Request) {
	if err := app.session.Discover(); err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]bool{"started": true})
}

func (app *webApp) handleHistory(writer http.ResponseWriter, request *http.Request) {
	peer := strings.TrimSpace(request.URL.Query().Get("peer"))
	if peer == "" {
		writeAPIError(writer, http.StatusBadRequest, "peer is required")
		return
	}
	entries, err := app.history.History(peer, 100)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, entries)
}

func (app *webApp) handleEvents(writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeAPIError(writer, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	events, unsubscribe := app.hub.subscribe()
	defer unsubscribe()
	_, _ = io.WriteString(writer, ": connected\n\n")
	flusher.Flush()
	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case event := <-events:
			encoded, _ := json.Marshal(event)
			fmt.Fprintf(writer, "id: %s\ndata: %s\n\n", event.ID, encoded)
			flusher.Flush()
		case <-keepAlive.C:
			_, _ = io.WriteString(writer, ": keep-alive\n\n")
			flusher.Flush()
		case <-request.Context().Done():
			return
		}
	}
}

func (app *webApp) handleMessage(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err.Error())
		return
	}
	body.To = strings.TrimSpace(body.To)
	if body.To == "" || strings.TrimSpace(body.Text) == "" {
		writeAPIError(writer, http.StatusBadRequest, "to and text are required")
		return
	}
	operationID := randomID()
	entry := history.Entry{Direction: "out", PeerIP: body.To, Kind: "msg", Content: body.Text}
	if err := app.history.Append(entry); err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	app.hub.publish(webEvent{Type: "message", Direction: "out", PeerIP: body.To, Kind: "msg", Content: body.Text, OperationID: operationID, Status: "sending"})
	go func() {
		ctx, cancel := context.WithTimeout(app.ctx, app.messageWait)
		defer cancel()
		acked, err := app.session.SendMessage(ctx, body.To, body.Text)
		status := "sent"
		if acked {
			status = "delivered"
		}
		event := webEvent{Type: "operation", PeerIP: body.To, OperationID: operationID, Status: status}
		if err != nil {
			event.Status, event.Error = "failed", err.Error()
		}
		app.hub.publish(event)
	}()
	writeJSON(writer, http.StatusAccepted, map[string]string{"operation_id": operationID})
}

func (app *webApp) handleSendPath(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		To   string `json:"to"`
		Path string `json:"path"`
		Kind string `json:"kind"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err.Error())
		return
	}
	body.To = strings.TrimSpace(body.To)
	body.Path = strings.TrimSpace(body.Path)
	if body.To == "" || body.Path == "" {
		writeAPIError(writer, http.StatusBadRequest, "to and path are required")
		return
	}
	resolvedPath, _, err := app.paths.Resolve(body.Path)
	if err != nil {
		writePathAPIError(writer, err)
		return
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		writePathAPIError(writer, err)
		return
	}
	kind := "file"
	if info.IsDir() {
		kind = "dir"
	} else if !info.Mode().IsRegular() {
		writeAPIError(writer, http.StatusBadRequest, "path must be a regular file or directory")
		return
	}
	if body.Kind != "" && body.Kind != kind {
		writeAPIError(writer, http.StatusBadRequest, "selected path does not match kind")
		return
	}
	operationID, err := app.startPathTransfer(body.To, resolvedPath, kind, "")
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]string{"operation_id": operationID})
}

func (app *webApp) handlePaths(writer http.ResponseWriter, request *http.Request) {
	if app.paths == nil || len(app.paths.roots) == 0 {
		writeAPIError(writer, http.StatusInternalServerError, "Web path access is not configured")
		return
	}
	requestedPath := strings.TrimSpace(request.URL.Query().Get("path"))
	if requestedPath == "" {
		requestedPath = app.paths.roots[0]
	}
	resolvedPath, _, err := app.paths.Resolve(requestedPath)
	if err != nil {
		writePathAPIError(writer, err)
		return
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		writePathAPIError(writer, err)
		return
	}
	if !info.IsDir() {
		writeAPIError(writer, http.StatusBadRequest, "path is not a directory")
		return
	}
	listing, err := app.paths.List(resolvedPath)
	if err != nil {
		writePathAPIError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, listing)
}

func writePathAPIError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errPathNotAllowed):
		writeAPIError(writer, http.StatusForbidden, err.Error())
	case errors.Is(err, os.ErrNotExist):
		writeAPIError(writer, http.StatusNotFound, err.Error())
	case errors.Is(err, syscall.ENOTDIR):
		writeAPIError(writer, http.StatusBadRequest, err.Error())
	default:
		writeAPIError(writer, http.StatusInternalServerError, err.Error())
	}
}

func (app *webApp) startPathTransfer(target, path, kind, cleanupRoot string) (string, error) {
	operationID := randomID()
	content := filepath.Base(path)
	if err := app.history.Append(history.Entry{Direction: "out", PeerIP: target, Kind: kind, Content: content}); err != nil {
		return "", err
	}
	app.hub.publish(webEvent{Type: "message", Direction: "out", PeerIP: target, Kind: kind, Content: content, OperationID: operationID, Status: "offered"})
	go func() {
		if cleanupRoot != "" {
			defer os.RemoveAll(cleanupRoot)
		}
		ctx, cancel := context.WithTimeout(app.ctx, app.transferWait)
		defer cancel()
		err := app.session.SendPath(ctx, target, path)
		event := webEvent{Type: "operation", PeerIP: target, OperationID: operationID, Status: "completed"}
		if err != nil {
			event.Status, event.Error = "failed", err.Error()
		}
		app.hub.publish(event)
	}()
	return operationID, nil
}

func (app *webApp) receive(event ipmsg.ReceiveEvent) {
	if event.Text != "" {
		entry := history.Entry{Direction: "in", PeerIP: event.From, PeerName: event.User, Kind: "msg", Content: event.Text}
		_ = app.history.Append(entry)
		app.hub.publish(webEvent{Type: "message", Direction: "in", PeerIP: event.From, PeerName: event.User, Kind: "msg", Content: event.Text})
	}
	for index, attachment := range event.Attachments {
		kind := "file"
		if attachment.Attr&0xff == ipmsg.FileDirectory {
			kind = "dir"
		}
		savedPath := ""
		if index < len(event.SavedPaths) {
			savedPath = event.SavedPaths[index]
		}
		entry := history.Entry{Direction: "in", PeerIP: event.From, PeerName: event.User, Kind: kind, Content: attachment.Name, SavedPath: savedPath}
		_ = app.history.Append(entry)
		app.hub.publish(webEvent{Type: "message", Direction: "in", PeerIP: event.From, PeerName: event.User, Kind: kind, Content: attachment.Name, SavedPath: savedPath})
	}
}

func (app *webApp) handleDownload(writer http.ResponseWriter, request *http.Request) {
	requested := request.URL.Query().Get("path")
	absolute, err := filepath.Abs(requested)
	if err != nil || !pathWithin(app.outputDir, absolute) {
		writeAPIError(writer, http.StatusForbidden, "path is outside the receive directory")
		return
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		writeAPIError(writer, http.StatusNotFound, "file is unavailable")
		return
	}
	http.ServeFile(writer, request, absolute)
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func decodeJSON(request *http.Request, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request body must contain one JSON object")
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeAPIError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}

func randomID() string {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(value[:])
}
