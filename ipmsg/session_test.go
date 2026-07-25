package ipmsg

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSessionMessageRoundTrip(t *testing.T) {
	sender, _, senderErrors := startTestSession(t, testPort(t), t.TempDir())
	receiver, receiverEvents, receiverErrors := startTestSession(t, testPort(t), t.TempDir())
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(receiver.node.Port))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	acked, err := sender.SendMessage(ctx, target, "交互消息")
	if err != nil {
		t.Fatal(err)
	}
	if !acked {
		t.Fatal("interactive session message was not acknowledged")
	}
	select {
	case event := <-receiverEvents:
		if event.Text != "交互消息" {
			t.Fatalf("got %q", event.Text)
		}
	case err := <-receiverErrors:
		t.Fatal(err)
	case err := <-senderErrors:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("receive event timed out")
	}
}

func TestSessionFileAndDirectoryRoundTrip(t *testing.T) {
	sourceParent := t.TempDir()
	filePath := filepath.Join(sourceParent, "message.txt")
	if err := os.WriteFile(filePath, []byte("session file"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirPath := filepath.Join(sourceParent, "folder")
	if err := os.MkdirAll(filepath.Join(dirPath, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirPath, "nested", "data.txt"), []byte("session dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	sender, _, senderErrors := startTestSession(t, testPort(t), t.TempDir())
	output := t.TempDir()
	receiver, receiverEvents, receiverErrors := startTestSession(t, testPort(t), output)
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(receiver.node.Port))

	for _, path := range []string{filePath, dirPath} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := sender.SendPath(ctx, target, path)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		select {
		case event := <-receiverEvents:
			if len(event.SavedPaths) != 1 {
				t.Fatalf("unexpected saved paths: %#v", event.SavedPaths)
			}
		case err := <-receiverErrors:
			t.Fatal(err)
		case err := <-senderErrors:
			t.Fatal(err)
		case <-time.After(2 * time.Second):
			t.Fatal("attachment event timed out")
		}
	}

	fileData, err := os.ReadFile(filepath.Join(output, "message.txt"))
	if err != nil || string(fileData) != "session file" {
		t.Fatalf("received file: data=%q err=%v", fileData, err)
	}
	dirData, err := os.ReadFile(filepath.Join(output, "folder", "nested", "data.txt"))
	if err != nil || string(dirData) != "session dir" {
		t.Fatalf("received directory file: data=%q err=%v", dirData, err)
	}
}

func startTestSession(t *testing.T, port int, output string) (*Session, <-chan ReceiveEvent, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan ReceiveEvent, 4)
	errors := make(chan error, 4)
	node := testNode("127.0.0.1", port)
	session, err := node.StartSession(ctx, output, func(event ReceiveEvent) {
		events <- event
	}, func(err error) {
		errors <- err
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		session.Close()
	})
	return session, events, errors
}
