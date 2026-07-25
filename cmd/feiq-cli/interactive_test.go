package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestParseInteractiveCommand(t *testing.T) {
	tests := []struct {
		line    string
		kind    string
		target  string
		payload string
	}{
		{"/send msg 192.168.1.2 hello world", "msg", "192.168.1.2", "hello world"},
		{"  /send   msg  192.168.1.2   spaced message  ", "msg", "192.168.1.2", "spaced message"},
		{"/send file 192.168.1.2 \"/tmp/a file.txt\"", "file", "192.168.1.2", "/tmp/a file.txt"},
		{"/send dir 192.168.1.2 ./folder", "dir", "192.168.1.2", "./folder"},
		{"/send image 192.168.1.2", "image", "192.168.1.2", ""},
		{"/history 192.168.1.2", "history", "192.168.1.2", ""},
		{"/search user", "search-user", "", ""},
		{"/search user alice", "search-user", "", "alice"},
		{"exit", "quit", "", ""},
		{"quit", "quit", "", ""},
		{"/exit", "quit", "", ""},
		{"/quit", "quit", "", ""},
	}
	for _, test := range tests {
		got, err := parseInteractiveCommand(test.line)
		if err != nil {
			t.Fatalf("%q: %v", test.line, err)
		}
		if got.kind != test.kind || got.target != test.target || got.payload != test.payload {
			t.Fatalf("%q: got %#v", test.line, got)
		}
	}
}

func TestCompleteLocalPaths(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "report file.txt")
	directory := filepath.Join(root, "reports")
	if err := os.WriteFile(file, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	files := completeLocalPaths("/send file 127.0.0.1 ", filepath.Join(root, "rep"), false)
	wantFile := "/send file 127.0.0.1 " + strconv.Quote(file)
	if len(files) != 2 || !strings.Contains(strings.Join(files, "\n"), wantFile) {
		t.Fatalf("unexpected file completions: %#v", files)
	}
	directories := completeLocalPaths("/send dir 127.0.0.1 ", filepath.Join(root, "rep"), true)
	if len(directories) != 1 || !strings.Contains(directories[0], "reports") {
		t.Fatalf("unexpected directory completions: %#v", directories)
	}
}

func TestParseInteractiveCommandRejectsInvalidInput(t *testing.T) {
	for _, line := range []string{"hello", "/send", "/send image 127.0.0.1 x", "/history", "/history 1.2.3.4 extra", "/search", "/search group"} {
		if _, err := parseInteractiveCommand(line); err == nil {
			t.Fatalf("%q should fail", line)
		}
	}
}
