package main

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLoadAppConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{
		"bind": "192.168.1.25",
		"port": 4242,
		"name": "测试端",
		"output": "~/received",
		"history_file": "~/.feiq-cli/custom.jsonl",
		"web_roots": ["~/", "~/Documents"],
		"message_wait": "8s",
		"transfer_wait": "30m"
	}`
	if err := os.Mkdir(filepath.Join(home, "Documents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	config, gotPath, err := loadAppConfig([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != path {
		t.Fatalf("config path = %q, want %q", gotPath, path)
	}
	if config.Bind == nil || *config.Bind != "192.168.1.25" {
		t.Fatalf("bind = %#v", config.Bind)
	}
	if config.Port == nil || *config.Port != 4242 {
		t.Fatalf("port = %#v", config.Port)
	}
	if config.Name == nil || *config.Name != "测试端" {
		t.Fatalf("name = %#v", config.Name)
	}
	if config.Output == nil || *config.Output != filepath.Join(home, "received") {
		t.Fatalf("output = %#v", config.Output)
	}
	if config.HistoryFile == nil || *config.HistoryFile != filepath.Join(home, ".feiq-cli", "custom.jsonl") {
		t.Fatalf("history file = %#v", config.HistoryFile)
	}
	if want := []string{"~/", "~/Documents"}; !reflect.DeepEqual(config.WebRoots, want) {
		t.Fatalf("web roots = %q, want %q", config.WebRoots, want)
	}
	if got := configDuration(config.MessageWait, time.Second); got != 8*time.Second {
		t.Fatalf("message wait = %s", got)
	}
	if got := configDuration(config.TransferWait, time.Second); got != 30*time.Minute {
		t.Fatalf("transfer wait = %s", got)
	}
}

func TestConfiguredWebRootsDefaultToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := configuredWebRoots(appConfig{})
	if err != nil || !reflect.DeepEqual(got, []string{home}) {
		t.Fatalf("roots = %q, err = %v", got, err)
	}
}

func TestConfiguredWebRootsExpandAndCompact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	child := filepath.Join(home, "Documents")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}

	config := appConfig{WebRoots: []string{"~/", "~/Documents", "~/"}}
	got, err := configuredWebRoots(config)
	if err != nil || !reflect.DeepEqual(got, []string{home}) {
		t.Fatalf("roots = %q, err = %v", got, err)
	}
}

func TestConfiguredWebRootsAllowFilesystemRoot(t *testing.T) {
	got, err := configuredWebRoots(appConfig{WebRoots: []string{"/"}})
	if err != nil || !reflect.DeepEqual(got, []string{string(filepath.Separator)}) {
		t.Fatalf("roots = %q, err = %v", got, err)
	}
}

func TestLoadAppConfigRejectsFileWebRoot(t *testing.T) {
	rootFile := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(rootFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"web_roots":[` + strconv.Quote(rootFile) + `]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := loadAppConfig([]string{"--config", path})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %v, want not a directory", err)
	}
}

func TestCommandLineOverridesConfigDefault(t *testing.T) {
	configOutput := "/from/config"
	config := appConfig{Output: &configOutput}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	output := fs.String("output", configString(config.Output, "./downloads"), "")
	if err := fs.Parse([]string{"--output", "/from/command-line"}); err != nil {
		t.Fatal(err)
	}
	if *output != "/from/command-line" {
		t.Fatalf("output = %q", *output)
	}
}

func TestLoadAppConfigAllowsMissingDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config, path, err := loadAppConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config, appConfig{}) {
		t.Fatalf("config = %#v", config)
	}
	if want := filepath.Join(home, ".feiq-cli", "config.json"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestLoadAppConfigRejectsInvalidFiles(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{"unknown field", `{"outpt":"downloads"}`, "unknown field"},
		{"invalid port", `{"port":70000}`, "port must be between"},
		{"invalid color", `{"color":"sometimes"}`, "color must be"},
		{"invalid duration", `{"transfer_wait":"later"}`, "transfer_wait"},
		{"multiple values", `{} {}`, "multiple JSON values"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := loadAppConfig([]string{"--config=" + path})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestLoadAppConfigRequiresExplicitFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	if _, _, err := loadAppConfig([]string{"--config", path}); err == nil {
		t.Fatal("expected missing explicit config to fail")
	}
}
