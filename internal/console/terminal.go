package console

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
)

var ErrInterrupt = errors.New("terminal interrupted")

type Color string

const (
	ColorRed     Color = "31"
	ColorGreen   Color = "32"
	ColorYellow  Color = "33"
	ColorBlue    Color = "34"
	ColorMagenta Color = "35"
	ColorCyan    Color = "36"
	ColorGray    Color = "90"
)

// Terminal provides a small interactive line editor. Printf can be called by
// background goroutines: it clears the active input row, writes the event,
// then redraws the prompt and unfinished input.
type Terminal struct {
	in          *os.File
	out         io.Writer
	prompt      string
	reader      *bufio.Reader
	interactive bool
	oldState    *term.State

	mu        sync.Mutex
	line      []rune
	cursor    int
	reading   bool
	closed    bool
	commands  []string
	completer func(string) []string
	colors    bool
	targets   []string
	targetAt  int
}

func (t *Terminal) SetCommands(commands []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.commands = append([]string(nil), commands...)
}

func (t *Terminal) SetCompleter(completer func(string) []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.completer = completer
}

func (t *Terminal) SetColorMode(mode string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch mode {
	case "auto":
		t.colors = t.interactive && os.Getenv("NO_COLOR") == ""
	case "always":
		t.colors = true
	case "never":
		t.colors = false
	default:
		return fmt.Errorf("color must be auto, always or never")
	}
	return nil
}

func (t *Terminal) SetTargets(targets []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.targets = uniqueTargets(targets)
	t.targetAt = -1
}

func (t *Terminal) RememberTarget(target string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.targets = uniqueTargets(append([]string{target}, t.targets...))
	t.targetAt = -1
}

func New(in *os.File, out io.Writer, prompt string) (*Terminal, error) {
	t := &Terminal{
		in:       in,
		out:      out,
		prompt:   prompt,
		reader:   bufio.NewReader(in),
		targetAt: -1,
	}
	fd := int(in.Fd())
	if term.IsTerminal(fd) {
		oldState, err := term.MakeRaw(fd)
		if err != nil {
			return nil, err
		}
		t.interactive = true
		t.colors = os.Getenv("NO_COLOR") == ""
		t.oldState = oldState
	}
	return t, nil
}

func (t *Terminal) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	if t.interactive {
		_, _ = io.WriteString(t.out, "\r\x1b[2K")
		return term.Restore(int(t.in.Fd()), t.oldState)
	}
	return nil
}

func (t *Terminal) ReadLine() (string, error) {
	if !t.interactive {
		_, _ = io.WriteString(t.out, t.prompt)
		line, err := t.reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return "", err
		}
		return trimLineEnding(line), nil
	}

	t.mu.Lock()
	t.reading = true
	t.line = t.line[:0]
	t.cursor = 0
	t.redrawLocked()
	t.mu.Unlock()

	var encoded []byte
	for {
		var one [1]byte
		if _, err := t.in.Read(one[:]); err != nil {
			t.finishRead()
			return "", err
		}
		ch := one[0]
		switch ch {
		case 3: // Ctrl-C in raw mode.
			t.finishRead()
			return "", ErrInterrupt
		case 4: // Ctrl-D.
			t.mu.Lock()
			empty := len(t.line) == 0
			t.mu.Unlock()
			if empty {
				t.finishRead()
				return "", io.EOF
			}
		case '\r', '\n':
			t.mu.Lock()
			line := string(t.line)
			t.line = t.line[:0]
			t.reading = false
			_, _ = io.WriteString(t.out, "\r\n")
			t.mu.Unlock()
			return line, nil
		case 8, 127:
			t.mu.Lock()
			t.targetAt = -1
			if t.cursor > 0 {
				copy(t.line[t.cursor-1:], t.line[t.cursor:])
				t.line = t.line[:len(t.line)-1]
				t.cursor--
				t.redrawLocked()
			}
			t.mu.Unlock()
		case 9: // Tab completes the current command prefix.
			t.mu.Lock()
			t.completeLocked()
			t.mu.Unlock()
		case 21: // Ctrl-U clears the current input.
			t.mu.Lock()
			t.line = t.line[:0]
			t.cursor = 0
			t.targetAt = -1
			t.redrawLocked()
			t.mu.Unlock()
		case 27:
			var sequence [2]byte
			if _, err := io.ReadFull(t.in, sequence[:]); err == nil && sequence[0] == '[' {
				t.mu.Lock()
				switch sequence[1] {
				case 'A':
					t.selectTargetLocked(1)
				case 'B':
					t.selectTargetLocked(-1)
				case 'C':
					if t.cursor < len(t.line) {
						t.cursor++
					}
					t.redrawLocked()
				case 'D':
					if t.cursor > 0 {
						t.cursor--
					}
					t.redrawLocked()
				}
				t.mu.Unlock()
			}
		default:
			encoded = append(encoded, ch)
			if !utf8.FullRune(encoded) {
				continue
			}
			r, size := utf8.DecodeRune(encoded)
			if r == utf8.RuneError && size == 1 {
				encoded = encoded[:0]
				continue
			}
			encoded = encoded[size:]
			t.mu.Lock()
			t.targetAt = -1
			t.line = append(t.line, 0)
			copy(t.line[t.cursor+1:], t.line[t.cursor:])
			t.line[t.cursor] = r
			t.cursor++
			if string(t.line) == "/" {
				t.showCompletionsLocked(t.matchingCommandsLocked("/"))
			} else {
				t.redrawLocked()
			}
			t.mu.Unlock()
		}
	}
}

func (t *Terminal) Printf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.interactive {
		message = strings.ReplaceAll(message, "\n", "\r\n")
		_, _ = io.WriteString(t.out, "\r\x1b[2K")
		_, _ = io.WriteString(t.out, message)
		_, _ = io.WriteString(t.out, "\r\n")
		if t.reading {
			t.redrawLocked()
		}
		return
	}
	_, _ = fmt.Fprintln(t.out, message)
}

func (t *Terminal) PrintfColor(color Color, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	t.mu.Lock()
	enabled := t.colors
	t.mu.Unlock()
	if enabled {
		message = "\x1b[" + string(color) + "m" + message + "\x1b[0m"
	}
	t.Printf("%s", message)
}

func (t *Terminal) finishRead() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reading = false
	t.line = t.line[:0]
	t.cursor = 0
	_, _ = io.WriteString(t.out, "\r\n")
}

func (t *Terminal) redrawLocked() {
	_, _ = io.WriteString(t.out, "\r\x1b[2K")
	_, _ = io.WriteString(t.out, t.prompt)
	_, _ = io.WriteString(t.out, string(t.line))
	if columns := displayWidth(t.line[t.cursor:]); columns > 0 {
		_, _ = fmt.Fprintf(t.out, "\x1b[%dD", columns)
	}
}

func (t *Terminal) completeLocked() {
	prefix := string(t.line)
	matches := t.matchingCompletionsLocked(prefix)
	if len(matches) == 0 {
		t.redrawLocked()
		return
	}
	completion := commonPrefix(matches)
	if len(matches) == 1 {
		t.line = []rune(matches[0])
		t.cursor = len(t.line)
		t.redrawLocked()
		return
	}
	if len(completion) > len(prefix) {
		t.line = []rune(completion)
		t.cursor = len(t.line)
	}
	t.showCompletionsLocked(matches)
}

func (t *Terminal) matchingCompletionsLocked(prefix string) []string {
	if t.completer != nil {
		return t.completer(prefix)
	}
	return t.matchingCommandsLocked(prefix)
}

func (t *Terminal) selectTargetLocked(direction int) {
	if len(t.targets) == 0 {
		t.redrawLocked()
		return
	}
	if direction > 0 {
		if t.targetAt < len(t.targets)-1 {
			t.targetAt++
		}
	} else if t.targetAt >= 0 {
		t.targetAt--
	}
	if t.targetAt < 0 {
		t.line = t.line[:0]
	} else {
		t.line = []rune("/send msg " + t.targets[t.targetAt] + " ")
	}
	t.cursor = len(t.line)
	t.redrawLocked()
}

func uniqueTargets(targets []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(targets))
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target != "" && !seen[target] {
			seen[target] = true
			result = append(result, target)
		}
	}
	return result
}

func displayWidth(value []rune) int {
	width := 0
	for _, r := range value {
		switch {
		case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
		case r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a || r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff || r >= 0xfe10 && r <= 0xfe6f || r >= 0xff00 && r <= 0xff60 || r >= 0xffe0 && r <= 0xffe6 || r >= 0x1f300):
			width += 2
		default:
			width++
		}
	}
	return width
}

func (t *Terminal) matchingCommandsLocked(prefix string) []string {
	var matches []string
	for _, command := range t.commands {
		if strings.HasPrefix(command, prefix) {
			matches = append(matches, command)
		}
	}
	return matches
}

func (t *Terminal) showCompletionsLocked(commands []string) {
	if len(commands) == 0 {
		t.redrawLocked()
		return
	}
	display := make([]string, len(commands))
	for index, command := range commands {
		display[index] = strings.TrimSpace(command)
	}
	_, _ = io.WriteString(t.out, "\r\x1b[2K")
	_, _ = io.WriteString(t.out, "可用命令: "+strings.Join(display, "  ")+"\r\n")
	t.redrawLocked()
}

func commonPrefix(values []string) string {
	if len(values) == 0 {
		return ""
	}
	prefix := values[0]
	for _, value := range values[1:] {
		for !strings.HasPrefix(value, prefix) {
			if prefix == "" {
				return ""
			}
			prefix = prefix[:len(prefix)-1]
		}
	}
	return prefix
}

func trimLineEnding(line string) string {
	for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
		line = line[:len(line)-1]
	}
	return line
}
