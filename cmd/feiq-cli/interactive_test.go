package main

import "testing"

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

func TestParseInteractiveCommandRejectsInvalidInput(t *testing.T) {
	for _, line := range []string{"hello", "/send", "/send image 127.0.0.1 x"} {
		if _, err := parseInteractiveCommand(line); err == nil {
			t.Fatalf("%q should fail", line)
		}
	}
}
