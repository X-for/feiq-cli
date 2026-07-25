//go:build darwin

package clipboard

import (
	"fmt"
	"os/exec"
	"strings"
)

func SavePNG(path string) error {
	script := `on run argv
set outputPath to item 1 of argv
try
  set imageData to the clipboard as «class PNGf»
on error
  error "剪贴板中没有可读取的 PNG 图片"
end try
set fileRef to open for access POSIX file outputPath with write permission
try
  set eof fileRef to 0
  write imageData to fileRef
  close access fileRef
on error errorMessage
  try
    close access fileRef
  end try
  error errorMessage
end try
end run`
	output, err := exec.Command("osascript", "-e", script, path).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("读取剪贴板图片: %s", message)
	}
	return nil
}
