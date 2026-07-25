package console

import "testing"

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
