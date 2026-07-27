package ipmsg

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func testPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func testNode(bind string, port int) *Node {
	return NewNode(Identity{Version: "1", Name: "test", Host: "localhost"}, bind, port)
}

func TestLocalMessageIntegration(t *testing.T) {
	receiverPort := testPort(t)
	senderPort := testPort(t)
	receiver := testNode("127.0.0.1", receiverPort)
	sender := testNode("127.0.0.1", senderPort)
	receiveCtx, stopReceive := context.WithCancel(context.Background())
	defer stopReceive()
	events := make(chan ReceiveEvent, 1)
	errors := make(chan error, 1)
	output := t.TempDir()
	go func() {
		errors <- receiver.Receive(receiveCtx, output, func(event ReceiveEvent) {
			events <- event
		})
	}()
	time.Sleep(50 * time.Millisecond)
	assertReceiverRunning(t, errors)

	sendCtx, stopSend := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopSend()
	acked, err := sender.SendMessage(sendCtx, net.JoinHostPort("127.0.0.1", strconv.Itoa(receiverPort)), "你好，FeiQ")
	if err != nil {
		t.Fatal(err)
	}
	if !acked {
		t.Fatal("message was not acknowledged")
	}
	select {
	case event := <-events:
		if event.Text != "你好，FeiQ" {
			t.Fatalf("got %q", event.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("message event timed out")
	}
	stopReceive()
	select {
	case err := <-errors:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("receiver did not stop")
	}
}

func TestLocalFileIntegration(t *testing.T) {
	sourceDir := t.TempDir()
	source := filepath.Join(sourceDir, "sample.txt")
	content := []byte("file transfer content")
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}
	saved := localPathTransfer(t, source, false)
	got, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("got %q, want %q", got, content)
	}
}

func TestLocalDirectoryIntegration(t *testing.T) {
	sourceParent := t.TempDir()
	source := filepath.Join(sourceParent, "sample-dir")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "one.txt"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "two.txt"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	saved := localPathTransfer(t, source, true)
	for relative, want := range map[string]string{
		"one.txt":                          "one",
		filepath.Join("nested", "two.txt"): "two",
	} {
		got, err := os.ReadFile(filepath.Join(saved, relative))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s: got %q, want %q", relative, got, want)
		}
	}
}

func TestDownloadAttachmentUsesThreeFieldRequest(t *testing.T) {
	tests := []struct {
		name       string
		attachment Attachment
		command    uint64
		response   string
	}{
		{
			name:       "regular file",
			attachment: Attachment{FileID: 0xb, Name: "file.txt", Size: 4, Attr: FileRegular},
			command:    CmdGetFileData,
			response:   "data",
		},
		{
			name:       "directory",
			attachment: Attachment{FileID: 0xb, Name: "root", Attr: FileDirectory},
			command:    CmdGetDirFiles,
			response:   "0016:root:000000000:2:0013:.:000000000:3:",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()

			requests := make(chan Packet, 1)
			serverErrors := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					serverErrors <- err
					return
				}
				defer conn.Close()
				raw, err := bufio.NewReader(conn).ReadBytes(0)
				if err != nil {
					serverErrors <- err
					return
				}
				request, err := DecodePacket(raw)
				if err != nil {
					serverErrors <- err
					return
				}
				requests <- request
				_, err = io.WriteString(conn, test.response)
				serverErrors <- err
			}()

			node := testNode("127.0.0.1", testPort(t))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := node.downloadAttachment(
				ctx,
				"127.0.0.1",
				listener.Addr().(*net.TCPAddr).Port,
				0x2a,
				test.attachment,
				t.TempDir(),
			); err != nil {
				t.Fatal(err)
			}

			request := <-requests
			if request.Command != test.command {
				t.Fatalf("command = %#x, want %#x", request.Command, test.command)
			}
			if got, want := string(request.Extra), "2a:b:0"; got != want {
				t.Fatalf("request extra = %q, want %q", got, want)
			}
			if err := <-serverErrors; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDownloadAttachmentRetriesEmptyTransfer(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requests := make(chan Packet, 2)
	serverErrors := make(chan error, 1)
	go func() {
		for attempt := 0; attempt < 2; attempt++ {
			conn, err := listener.Accept()
			if err != nil {
				serverErrors <- err
				return
			}
			raw, readErr := bufio.NewReader(conn).ReadBytes(0)
			if readErr != nil {
				_ = conn.Close()
				serverErrors <- readErr
				return
			}
			request, decodeErr := DecodePacket(raw)
			if decodeErr != nil {
				_ = conn.Close()
				serverErrors <- decodeErr
				return
			}
			requests <- request
			if attempt == 1 {
				_, err = io.WriteString(conn, "data")
			}
			if closeErr := conn.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				serverErrors <- err
				return
			}
		}
		serverErrors <- nil
	}()

	output := t.TempDir()
	node := testNode("127.0.0.1", testPort(t))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	saved, err := node.downloadAttachment(
		ctx,
		"127.0.0.1",
		listener.Addr().(*net.TCPAddr).Port,
		0x2a,
		Attachment{FileID: 0xb, Name: "retry.txt", Size: 4, Attr: FileRegular},
		output,
	)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(saved); err != nil || string(data) != "data" {
		t.Fatalf("received data = %q, err = %v", data, err)
	}
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if err := <-serverErrors; err != nil {
		t.Fatal(err)
	}
}

func TestDownloadFileRemovesPartialOutput(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufio.NewReader(conn).ReadBytes(0)
		_, _ = io.WriteString(conn, "short")
	}()

	output := t.TempDir()
	node := testNode("127.0.0.1", testPort(t))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = node.downloadAttachment(
		ctx,
		"127.0.0.1",
		listener.Addr().(*net.TCPAddr).Port,
		0x2a,
		Attachment{FileID: 0xb, Name: "partial.bin", Size: 10, Attr: FileRegular},
		output,
	)
	if err == nil {
		t.Fatal("expected a short download error")
	}
	if _, statErr := os.Stat(filepath.Join(output, "partial.bin")); !os.IsNotExist(statErr) {
		t.Fatalf("partial file was not removed: %v", statErr)
	}
}

func localPathTransfer(t *testing.T, source string, directory bool) string {
	t.Helper()
	receiverPort := testPort(t)
	senderPort := testPort(t)
	receiver := testNode("127.0.0.1", receiverPort)
	sender := testNode("127.0.0.1", senderPort)
	output := t.TempDir()
	receiveCtx, stopReceive := context.WithCancel(context.Background())
	defer stopReceive()
	events := make(chan ReceiveEvent, 1)
	errors := make(chan error, 1)
	go func() {
		errors <- receiver.Receive(receiveCtx, output, func(event ReceiveEvent) {
			events <- event
		})
	}()
	time.Sleep(50 * time.Millisecond)
	assertReceiverRunning(t, errors)

	sendCtx, stopSend := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopSend()
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(receiverPort))
	if err := sender.SendPath(sendCtx, target, source); err != nil {
		t.Fatal(err)
	}
	var saved string
	select {
	case event := <-events:
		if len(event.SavedPaths) != 1 {
			t.Fatalf("unexpected saved paths: %#v", event.SavedPaths)
		}
		saved = event.SavedPaths[0]
	case <-time.After(2 * time.Second):
		t.Fatal("attachment event timed out")
	}
	info, err := os.Stat(saved)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() != directory {
		t.Fatalf("saved path directory=%v, want %v", info.IsDir(), directory)
	}
	stopReceive()
	select {
	case err := <-errors:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("receiver did not stop")
	}
	return saved
}

func assertReceiverRunning(t *testing.T, errors <-chan error) {
	t.Helper()
	select {
	case err := <-errors:
		t.Fatalf("receiver stopped during startup: %v", err)
	default:
	}
}
