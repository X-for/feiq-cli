package console

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/term"
)

var ErrInterrupt = errors.New("terminal interrupted")

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

	mu      sync.Mutex
	line    []rune
	reading bool
	closed  bool
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
			if len(t.line) > 0 {
				t.line = t.line[:len(t.line)-1]
				t.redrawLocked()
			}
			t.mu.Unlock()
		case 21: // Ctrl-U clears the current input.
			t.mu.Lock()
			t.line = t.line[:0]
			t.redrawLocked()
			t.mu.Unlock()
		case 27: // Ignore terminal escape sequences such as arrow keys.
			var discard [2]byte
			_, _ = io.ReadFull(t.in, discard[:])
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
			t.line = append(t.line, r)
			t.redrawLocked()
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

func (t *Terminal) finishRead() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reading = false
	t.line = t.line[:0]
	_, _ = io.WriteString(t.out, "\r\n")
}

func (t *Terminal) redrawLocked() {
	_, _ = io.WriteString(t.out, "\r\x1b[2K")
	_, _ = io.WriteString(t.out, t.prompt)
	_, _ = io.WriteString(t.out, string(t.line))
}

func trimLineEnding(line string) string {
	for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
		line = line[:len(line)-1]
	}
	return line
}
