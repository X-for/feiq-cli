package console

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestCommonPrefix(t *testing.T) {
	tests := []struct {
		values []string
		want   string
	}{
		{[]string{"/send msg ", "/send file ", "/send dir "}, "/send "},
		{[]string{"/history "}, "/history "},
		{nil, ""},
	}
	for _, test := range tests {
		if got := commonPrefix(test.values); got != test.want {
			t.Fatalf("commonPrefix(%q) = %q, want %q", test.values, got, test.want)
		}
	}
}

func TestMatchingCommands(t *testing.T) {
	terminal := &Terminal{commands: []string{"/send msg ", "/send file ", "/history "}}
	matches := terminal.matchingCompletionsLocked("/send f")
	if len(matches) != 1 || matches[0].Value != "/send file " {
		t.Fatalf("unexpected matches: %#v", matches)
	}
}

func TestHistorySelectionRestoresExactLinesAndDraft(t *testing.T) {
	terminal := &Terminal{out: io.Discard}
	terminal.rememberLineLocked("/file \"/tmp/a file\"")
	terminal.rememberLineLocked("消息 \\\\ \"原样\"")
	terminal.historyAt = len(terminal.history)
	terminal.line = []rune("draft")

	terminal.selectHistoryLocked(-1)
	if got := string(terminal.line); got != "消息 \\\\ \"原样\"" || terminal.cursor != len(terminal.line) {
		t.Fatalf("unexpected latest history: %q cursor=%d", got, terminal.cursor)
	}
	terminal.selectHistoryLocked(-1)
	if got := string(terminal.line); got != "/file \"/tmp/a file\"" {
		t.Fatalf("unexpected older history: %q", got)
	}
	terminal.selectHistoryLocked(1)
	terminal.selectHistoryLocked(1)
	if got := string(terminal.line); got != "draft" {
		t.Fatalf("draft was not restored: %q", got)
	}
}

func TestDisplayWidth(t *testing.T) {
	if got := displayWidth([]rune("ab中文🫡")); got != 8 {
		t.Fatalf("display width = %d, want 8", got)
	}
}

func TestCompletionHintStaysOnCurrentRow(t *testing.T) {
	var output bytes.Buffer
	terminal := &Terminal{
		out:      &output,
		prompt:   "feiq> ",
		line:     []rune("/"),
		commands: []string{"/to ", "/file ", "/help"},
	}
	terminal.redrawLocked(true)
	if strings.Contains(output.String(), "\n") || strings.Contains(output.String(), "\r\n") {
		t.Fatalf("completion hint added a new row: %q", output.String())
	}
	if !strings.Contains(output.String(), "/to") {
		t.Fatalf("completion hint missing candidates: %q", output.String())
	}
}

func TestStructuredCompletion(t *testing.T) {
	terminal := &Terminal{
		out: io.Discard,
		completer: func(string) []Completion {
			return []Completion{{Value: "/to 192.168.1.2", Display: "Alice (192.168.1.2)"}}
		},
		line: []rune("/to ali"),
	}
	terminal.completeLocked()
	if got := string(terminal.line); got != "/to 192.168.1.2" {
		t.Fatalf("unexpected completion: %q", got)
	}
}

func TestTruncateWidth(t *testing.T) {
	got := truncateWidth("abcdef中文", 6)
	if displayWidth([]rune(got)) > 6 || !strings.HasSuffix(got, "…") {
		t.Fatalf("unexpected truncation: %q", got)
	}
}
