package ipmsg

import (
	"bytes"
	"os/exec"
)

// macOS ships iconv, and Linux distributions normally do too. Keeping the
// conversion behind these functions leaves the Go protocol layer dependency
// free. If iconv is unavailable, UTF-8 is used as a safe fallback.
func encodeGBK(text string) []byte {
	if out, ok := iconv([]byte(text), "UTF-8", "GBK"); ok {
		return out
	}
	return []byte(text)
}

func decodeGBK(raw []byte) string {
	if out, ok := iconv(raw, "GBK", "UTF-8"); ok {
		return string(out)
	}
	return string(raw)
}

func iconv(input []byte, from, to string) ([]byte, bool) {
	path, err := exec.LookPath("iconv")
	if err != nil {
		return nil, false
	}
	cmd := exec.Command(path, "-f", from, "-t", to)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.Output()
	return out, err == nil
}
