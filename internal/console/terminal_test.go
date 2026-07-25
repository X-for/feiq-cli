package console

import (
	"io"
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
	matches := terminal.matchingCommandsLocked("/send f")
	if len(matches) != 1 || matches[0] != "/send file " {
		t.Fatalf("unexpected matches: %#v", matches)
	}
}

func TestTargetSelectionAndCursor(t *testing.T) {
	terminal := &Terminal{out: io.Discard, targets: []string{"192.168.1.2", "192.168.1.3"}, targetAt: -1}
	terminal.selectTargetLocked(1)
	if got := string(terminal.line); got != "/send msg 192.168.1.2 " || terminal.cursor != len(terminal.line) {
		t.Fatalf("unexpected selected target: %q cursor=%d", got, terminal.cursor)
	}
	terminal.selectTargetLocked(1)
	if got := string(terminal.line); got != "/send msg 192.168.1.3 " {
		t.Fatalf("unexpected older target: %q", got)
	}
	terminal.selectTargetLocked(-1)
	if got := string(terminal.line); got != "/send msg 192.168.1.2 " {
		t.Fatalf("unexpected newer target: %q", got)
	}
}

func TestDisplayWidth(t *testing.T) {
	if got := displayWidth([]rune("ab中文🫡")); got != 8 {
		t.Fatalf("display width = %d, want 8", got)
	}
}

func TestUniqueTargets(t *testing.T) {
	got := uniqueTargets([]string{" 1.2.3.4 ", "5.6.7.8", "1.2.3.4"})
	if len(got) != 2 || got[0] != "1.2.3.4" || got[1] != "5.6.7.8" {
		t.Fatalf("unexpected targets: %#v", got)
	}
}
