package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintVersion(t *testing.T) {
	oldVersion, oldCommit, oldDate := appVersion, appCommit, appDate
	appVersion, appCommit, appDate = "v1.2.3", "abc123", "2026-07-30T00:00:00Z"
	t.Cleanup(func() {
		appVersion, appCommit, appDate = oldVersion, oldCommit, oldDate
	})

	var output bytes.Buffer
	printVersion(&output)
	for _, expected := range []string{
		"feiq-cli v1.2.3",
		"commit: abc123",
		"built: 2026-07-30T00:00:00Z",
		"platform:",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("version output %q does not contain %q", output.String(), expected)
		}
	}
}
