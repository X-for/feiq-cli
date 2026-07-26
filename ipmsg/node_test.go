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

func TestDownloadDirectoryUsesFeiQCompatibleRequest(t *testing.T) {
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
		_, err = io.WriteString(conn, "0016:root:000000000:2:0013:.:000000000:3:")
		serverErrors <- err
	}()

	node := testNode("127.0.0.1", testPort(t))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	saved, err := node.downloadAttachment(
		ctx,
		"127.0.0.1",
		listener.Addr().(*net.TCPAddr).Port,
		0x2a,
		Attachment{FileID: 0xb, Name: "root", Attr: FileDirectory},
		t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(saved); err != nil {
		t.Fatal(err)
	} else if !info.IsDir() {
		t.Fatalf("saved path %q is not a directory", saved)
	}

	request := <-requests
	if CommandMode(request.Command) != CmdGetDirFiles {
		t.Fatalf("command mode = %#x, want GETDIRFILES", request.Command)
	}
	if request.Command&OptFileAttach == 0 {
		t.Fatalf("command %#x does not include FILEATTACHOPT", request.Command)
	}
	if got, want := string(request.Extra), "2a:b:0"; got != want {
		t.Fatalf("request extra = %q, want %q", got, want)
	}
	if err := <-serverErrors; err != nil {
		t.Fatal(err)
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
