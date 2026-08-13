package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var shortCommandHTTPClient = &http.Client{Timeout: 10 * time.Second}

func shortMessage(args []string) error {
	fs := flag.NewFlagSet("msg", flag.ContinueOnError)
	apiURL := fs.String("api", "http://127.0.0.1:2426", "running feiq-cli Web/API address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return fmt.Errorf("usage: feiq-cli msg [--api URL] <IP> <message>")
	}
	return postShortCommand(*apiURL, "/api/messages", map[string]string{
		"to": fs.Arg(0), "text": strings.Join(fs.Args()[1:], " "),
	})
}

func shortPath(args []string, wantDir bool) error {
	command, kind := "file", "file"
	if wantDir {
		command, kind = "dir", "dir"
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	apiURL := fs.String("api", "http://127.0.0.1:2426", "running feiq-cli Web/API address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: feiq-cli %s [--api URL] <IP> <path>", command)
	}
	path, err := filepath.Abs(expandHomePath(fs.Arg(1)))
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if wantDir != info.IsDir() || !wantDir && !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a %s", path, map[bool]string{true: "directory", false: "regular file"}[wantDir])
	}
	return postShortCommand(*apiURL, "/api/send-path", map[string]string{
		"to": fs.Arg(0), "path": path, "kind": kind,
	})
}

func postShortCommand(baseURL, endpoint string, payload any) error {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("invalid API address %q", baseURL)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + endpoint
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, parsed.String(), bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := shortCommandHTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("connect to running feiq-cli Web/API at %s: %w", baseURL, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	var result struct {
		OperationID string `json:"operation_id"`
		Error       string `json:"error"`
	}
	_ = json.Unmarshal(body, &result)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if result.Error == "" {
			result.Error = strings.TrimSpace(string(body))
		}
		return fmt.Errorf("Web/API rejected request (%s): %s", response.Status, result.Error)
	}
	if result.OperationID == "" {
		return fmt.Errorf("Web/API returned no operation ID")
	}
	fmt.Println("accepted by running feiq-cli Web/API; operation:", result.OperationID)
	return nil
}
