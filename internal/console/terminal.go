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

type Completion struct {
	Value   string
	Display string
}

type Completer func(line string) []Completion

// Terminal is a small Unicode-aware line editor. Async output clears and
// redraws the active input row. One row below the input is reserved for
// completion hints and refreshed in place.
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
	completer Completer
	colors    bool

	history   []string
	historyAt int
	draft     []rune
	hintRow   bool

	completionMatches []Completion
	completionAt      int
}

func New(in *os.File, out io.Writer, prompt string) (*Terminal, error) {
	t := &Terminal{
		in:     in,
		out:    out,
		prompt: prompt,
		reader: bufio.NewReader(in),
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

func (t *Terminal) SetPrompt(prompt string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prompt = prompt
	if t.reading {
		t.redrawLocked(true)
	}
}

func (t *Terminal) SetCommands(commands []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.commands = append([]string(nil), commands...)
}

func (t *Terminal) SetCompleter(completer Completer) {
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

func (t *Terminal) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	if t.interactive {
		t.clearInputLocked()
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
	t.historyAt = len(t.history)
	t.draft = t.draft[:0]
	t.resetCompletionLocked()
	t.reserveHintRowLocked()
	t.redrawLocked(true)
	t.mu.Unlock()

	var encoded []byte
	for {
		var one [1]byte
		if _, err := t.in.Read(one[:]); err != nil {
			t.finishRead()
			return "", err
		}
		switch ch := one[0]; ch {
		case 3: // Ctrl-C.
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
			t.rememberLineLocked(line)
			t.redrawLocked(false)
			t.line = t.line[:0]
			t.cursor = 0
			t.reading = false
			_, _ = io.WriteString(t.out, "\r\n")
			t.hintRow = false
			t.mu.Unlock()
			return line, nil
		case 8, 127:
			t.mu.Lock()
			t.detachHistoryLocked()
			if t.cursor > 0 {
				copy(t.line[t.cursor-1:], t.line[t.cursor:])
				t.line = t.line[:len(t.line)-1]
				t.cursor--
			}
			t.redrawLocked(true)
			t.mu.Unlock()
		case 9: // Tab.
			t.mu.Lock()
			t.completeLocked()
			t.mu.Unlock()
		case 21: // Ctrl-U.
			t.mu.Lock()
			t.detachHistoryLocked()
			t.line = t.line[:0]
			t.cursor = 0
			t.redrawLocked(true)
			t.mu.Unlock()
		case 27:
			var sequence [2]byte
			if _, err := io.ReadFull(t.in, sequence[:]); err == nil && sequence[0] == '[' {
				t.mu.Lock()
				switch sequence[1] {
				case 'A':
					t.selectHistoryLocked(-1)
				case 'B':
					t.selectHistoryLocked(1)
				case 'C':
					t.resetCompletionLocked()
					if t.cursor < len(t.line) {
						t.cursor++
					}
					t.redrawLocked(true)
				case 'D':
					t.resetCompletionLocked()
					if t.cursor > 0 {
						t.cursor--
					}
					t.redrawLocked(true)
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
			t.detachHistoryLocked()
			t.line = append(t.line, 0)
			copy(t.line[t.cursor+1:], t.line[t.cursor:])
			t.line[t.cursor] = r
			t.cursor++
			t.redrawLocked(true)
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
		t.clearInputLocked()
		_, _ = io.WriteString(t.out, message)
		_, _ = io.WriteString(t.out, "\r\n")
		if t.reading {
			t.reserveHintRowLocked()
			t.redrawLocked(true)
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
	t.clearInputLocked()
	_, _ = io.WriteString(t.out, "\r\n")
	t.hintRow = false
}

func (t *Terminal) redrawLocked(showHint bool) {
	t.clearInputLocked()
	_, _ = io.WriteString(t.out, t.prompt)
	_, _ = io.WriteString(t.out, string(t.line))

	hint := ""
	if showHint {
		hint = t.completionHintLocked()
	}
	if hint != "" {
		_, _ = io.WriteString(t.out, "\x1b[1B\r\x1b[2K")
		if t.colors {
			_, _ = io.WriteString(t.out, "\x1b["+string(ColorGray)+"m"+hint+"\x1b[0m")
		} else {
			_, _ = io.WriteString(t.out, hint)
		}
		_, _ = io.WriteString(t.out, "\x1b[1A\r")
	}

	_, _ = io.WriteString(t.out, "\r")
	columns := displayWidth([]rune(t.prompt)) + displayWidth(t.line[:t.cursor])
	if columns > 0 {
		_, _ = fmt.Fprintf(t.out, "\x1b[%dC", columns)
	}
}

func (t *Terminal) reserveHintRowLocked() {
	_, _ = io.WriteString(t.out, "\r\n\x1b[1A")
	t.hintRow = true
}

func (t *Terminal) clearInputLocked() {
	_, _ = io.WriteString(t.out, "\r\x1b[2K")
	if !t.hintRow {
		return
	}
	_, _ = io.WriteString(t.out, "\x1b[1B\r\x1b[2K\x1b[1A\r")
}

func (t *Terminal) completeLocked() {
	if len(t.completionMatches) > 0 {
		t.completionAt = (t.completionAt + 1) % len(t.completionMatches)
		t.line = []rune(t.completionMatches[t.completionAt].Value)
		t.cursor = len(t.line)
		t.redrawLocked(true)
		return
	}

	prefix := string(t.line)
	matches := t.matchingCompletionsLocked(prefix)
	if len(matches) == 0 {
		t.redrawLocked(true)
		return
	}

	values := make([]string, 0, len(matches))
	for _, match := range matches {
		values = append(values, match.Value)
	}
	completion := commonPrefix(values)
	if len(matches) == 1 {
		completion = matches[0].Value
	}
	if len(completion) > len(prefix) {
		t.detachHistoryLocked()
		t.line = []rune(completion)
		t.cursor = len(t.line)
	} else if len(matches) > 1 {
		t.detachHistoryLocked()
		t.completionMatches = append([]Completion(nil), matches...)
		t.completionAt = 0
		t.line = []rune(matches[0].Value)
		t.cursor = len(t.line)
	}
	t.redrawLocked(true)
}

func (t *Terminal) matchingCompletionsLocked(prefix string) []Completion {
	if t.completer != nil {
		return t.completer(prefix)
	}
	var matches []Completion
	for _, command := range t.commands {
		if strings.HasPrefix(command, prefix) {
			matches = append(matches, Completion{
				Value:   command,
				Display: strings.TrimSpace(command),
			})
		}
	}
	return matches
}

func (t *Terminal) completionHintLocked() string {
	matches := t.matchingCompletionsLocked(string(t.line))
	if len(matches) == 0 {
		return ""
	}
	displays := make([]string, 0, len(matches))
	for _, match := range matches {
		display := match.Display
		if display == "" {
			display = strings.TrimSpace(match.Value)
		}
		displays = append(displays, display)
	}
	return formatCompletionHint(displays, t.terminalWidthLocked()-1)
}

func (t *Terminal) terminalWidthLocked() int {
	width := 120
	if t.interactive {
		if columns, _, err := term.GetSize(int(t.in.Fd())); err == nil && columns > 0 {
			width = columns
		}
	}
	return width
}

func formatCompletionHint(displays []string, maxWidth int) string {
	if len(displays) == 0 || maxWidth < 4 {
		return ""
	}
	for count := len(displays); count >= 1; count-- {
		parts := append([]string(nil), displays[:count]...)
		if remaining := len(displays) - count; remaining > 0 {
			parts = append(parts, fmt.Sprintf("+%d", remaining))
		}
		hint := "[" + strings.Join(parts, " | ") + "]"
		if displayWidth([]rune(hint)) <= maxWidth {
			return hint
		}
	}
	suffix := ""
	if len(displays) > 1 {
		suffix = fmt.Sprintf(" | +%d", len(displays)-1)
	}
	available := maxWidth - displayWidth([]rune("["+suffix+"]"))
	if available < 2 {
		return truncateWidth("[+"+fmt.Sprint(len(displays))+"]", maxWidth)
	}
	return "[" + truncateWidth(displays[0], available) + suffix + "]"
}

func (t *Terminal) rememberLineLocked(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	if len(t.history) == 0 || t.history[len(t.history)-1] != line {
		t.history = append(t.history, line)
		if len(t.history) > 200 {
			t.history = append([]string(nil), t.history[len(t.history)-200:]...)
		}
	}
	t.historyAt = len(t.history)
	t.draft = t.draft[:0]
}

func (t *Terminal) selectHistoryLocked(direction int) {
	t.resetCompletionLocked()
	if len(t.history) == 0 {
		t.redrawLocked(true)
		return
	}
	if t.historyAt == len(t.history) && direction < 0 {
		t.draft = append(t.draft[:0], t.line...)
	}
	t.historyAt += direction
	if t.historyAt < 0 {
		t.historyAt = 0
	}
	if t.historyAt > len(t.history) {
		t.historyAt = len(t.history)
	}
	if t.historyAt == len(t.history) {
		t.line = append(t.line[:0], t.draft...)
	} else {
		t.line = []rune(t.history[t.historyAt])
	}
	t.cursor = len(t.line)
	t.redrawLocked(true)
}

func (t *Terminal) detachHistoryLocked() {
	t.resetCompletionLocked()
	if t.historyAt != len(t.history) {
		t.historyAt = len(t.history)
		t.draft = t.draft[:0]
	}
}

func (t *Terminal) resetCompletionLocked() {
	t.completionMatches = t.completionMatches[:0]
	t.completionAt = -1
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

func truncateWidth(value string, maxWidth int) string {
	if maxWidth <= 1 {
		return ""
	}
	limit := maxWidth - 1
	var result []rune
	width := 0
	for _, r := range []rune(value) {
		runeWidth := displayWidth([]rune{r})
		if width+runeWidth > limit {
			break
		}
		result = append(result, r)
		width += runeWidth
	}
	return string(result) + "…"
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
