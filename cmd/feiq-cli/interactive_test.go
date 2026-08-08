package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"feiq-cli/internal/history"
)

func TestParseInteractiveCommand(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		line    string
		kind    string
		target  string
		payload string
	}{
		{"hello \" \\\\ world", "msg-current", "", "hello \" \\\\ world"},
		{"/msg hello \" \\\\ world", "msg-current", "", "hello \" \\\\ world"},
		{"/to 郑安其", "select", "", "郑安其"},
		{"/file \"/tmp/a file.txt\"", "file", "", "/tmp/a file.txt"},
		{"/dir ./folder", "dir", "", "./folder"},
		{"/file ~/Desktop/report.txt", "file", "", filepath.Join(home, "Desktop/report.txt")},
		{"/dir \"~/Desktop/My Folder\"", "dir", "", filepath.Join(home, "Desktop/My Folder")},
		{"/image", "image", "", ""},
		{"/compose", "compose", "", ""},
		{"/users alice", "users", "", "alice"},
		{"/history", "history", "", ""},
		{"/history alice", "history", "", "alice"},
		{"/send msg 192.168.1.2 hello world", "msg", "192.168.1.2", "hello world"},
		{"/send msg 192.168.1.2 \" \\\\ \"", "msg", "192.168.1.2", "\" \\\\ \""},
		{"/send file 192.168.1.2 \"/tmp/a file.txt\"", "file", "192.168.1.2", "/tmp/a file.txt"},
		{"/send dir 192.168.1.2 ./folder", "dir", "192.168.1.2", "./folder"},
		{"/send dir 192.168.1.2 ~/Desktop/folder", "dir", "192.168.1.2", filepath.Join(home, "Desktop/folder")},
		{"/send image 192.168.1.2", "image", "192.168.1.2", ""},
		{"/search user", "users", "", ""},
		{"/search user alice", "users", "", "alice"},
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
	for _, line := range []string{"/send", "/send image 127.0.0.1 x", "/to", "/msg", "/file", "/dir", "/image x", "/compose x", "/search", "/search group"} {
		if _, err := parseInteractiveCommand(line); err == nil {
			t.Fatalf("%q should fail", line)
		}
	}
}

func TestContactBookResolve(t *testing.T) {
	book := contactBook{local: []history.User{
		{IP: "192.168.1.2", Name: "Alice"},
		{IP: "192.168.1.3", Name: "Alice-Office"},
	}}
	selected, err := book.resolve("192.168.1.2")
	if err != nil || selected.Name != "Alice" {
		t.Fatalf("resolve exact IP: selected=%#v err=%v", selected, err)
	}
	selected, err = book.resolve("office")
	if err != nil || selected.IP != "192.168.1.3" {
		t.Fatalf("resolve fuzzy name: selected=%#v err=%v", selected, err)
	}
	if _, err := book.resolve("Alice"); err != nil {
		t.Fatalf("exact name should win over fuzzy matches: %v", err)
	}
	if _, err := book.resolve("192.168.1.99"); err != nil {
		t.Fatalf("raw IP should be accepted: %v", err)
	}
}

func TestInteractiveContactCompletions(t *testing.T) {
	find := func(query string) []contact {
		if query != "ali" && query != "" {
			t.Fatalf("unexpected query: %q", query)
		}
		return []contact{{IP: "192.168.1.2", Name: "Alice", Online: true}}
	}
	got := interactiveCompletions("/to ali", find)
	if len(got) != 1 || got[0].Value != "/to 192.168.1.2" || !strings.Contains(got[0].Display, "Alice") {
		t.Fatalf("unexpected completions: %#v", got)
	}
	got = interactiveCompletions("/to ", find)
	if len(got) != 1 || got[0].Value != "/to 192.168.1.2" {
		t.Fatalf("empty contact query did not return contacts: %#v", got)
	}
}
