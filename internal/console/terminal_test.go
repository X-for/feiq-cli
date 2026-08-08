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

func TestCompletionHintUsesOneReservedRow(t *testing.T) {
	var output bytes.Buffer
	terminal := &Terminal{
		out:      &output,
		prompt:   "feiq> ",
		line:     []rune("/"),
		commands: []string{"/to ", "/file ", "/help"},
	}
	terminal.reserveHintRowLocked()
	terminal.redrawLocked(true)
	terminal.redrawLocked(true)
	if got := strings.Count(output.String(), "\r\n"); got != 1 {
		t.Fatalf("completion hint reserved %d rows, want 1: %q", got, output.String())
	}
	if !strings.Contains(output.String(), "/to") {
		t.Fatalf("completion hint missing candidates: %q", output.String())
	}
}

func TestCompletionHintDoesNotDependOnInputLength(t *testing.T) {
	terminal := &Terminal{
		prompt: "feiq[Alice]> ",
		line:   []rune("/file /a/very/long/path/that/used/to/hide/the/hint/"),
		completer: func(string) []Completion {
			return []Completion{{Display: "one.jpg"}, {Display: "two.jpg"}}
		},
	}
	if got := terminal.completionHintLocked(); !strings.Contains(got, "one.jpg") {
		t.Fatalf("long input hid completion hint: %q", got)
	}
}

func TestRedrawWithoutHintRestoresCursorFromLineStart(t *testing.T) {
	var output bytes.Buffer
	terminal := &Terminal{out: &output, prompt: "feiq> ", line: []rune("abc"), cursor: 1}
	terminal.redrawLocked(false)
	if !strings.HasSuffix(output.String(), "\r\x1b[7C") {
		t.Fatalf("cursor was not restored from line start: %q", output.String())
	}
}

func TestFormatCompletionHintShowsRemainingCount(t *testing.T) {
	got := formatCompletionHint([]string{"first-long-name.jpg", "second.jpg", "third.jpg"}, 24)
	if displayWidth([]rune(got)) > 24 || !strings.Contains(got, "+2") {
		t.Fatalf("unexpected compact hint: %q", got)
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

func TestTabCyclesCompletionsWithoutLongerCommonPrefix(t *testing.T) {
	terminal := &Terminal{
		out:  io.Discard,
		line: []rune("/file "),
		completer: func(string) []Completion {
			return []Completion{
				{Value: "/file alpha.jpg", Display: "alpha.jpg"},
				{Value: "/file beta.jpg", Display: "beta.jpg"},
			}
		},
	}
	terminal.completeLocked()
	if got := string(terminal.line); got != "/file alpha.jpg" {
		t.Fatalf("first completion = %q", got)
	}
	terminal.completeLocked()
	if got := string(terminal.line); got != "/file beta.jpg" {
		t.Fatalf("second completion = %q", got)
	}
	terminal.detachHistoryLocked()
	if len(terminal.completionMatches) != 0 {
		t.Fatal("editing did not reset completion cycle")
	}
}

func TestTruncateWidth(t *testing.T) {
	got := truncateWidth("abcdef中文", 6)
	if displayWidth([]rune(got)) > 6 || !strings.HasSuffix(got, "…") {
		t.Fatalf("unexpected truncation: %q", got)
	}
}
