package ipmsg

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPacketRoundTrip(t *testing.T) {
	id := Identity{Version: "1", Name: "tester", Host: "host"}
	raw := EncodePacket(id, 42, CmdSendMsg|OptSendCheck, []byte("hello"))
	packet, err := DecodePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if packet.PacketNo != 42 || packet.Command != CmdSendMsg|OptSendCheck || string(packet.Extra) != "hello" {
		t.Fatalf("unexpected packet: %#v", packet)
	}
}

func TestAttachmentRoundTrip(t *testing.T) {
	want := Attachment{FileID: 1, Name: "a:b.txt", Size: 0x123, ModTime: 0x456, Attr: FileRegular}
	extra := append([]byte("text\x00"), EncodeAttachment(want)...)
	got, err := DecodeAttachments(extra)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDirectoryStreamRoundTrip(t *testing.T) {
	source := t.TempDir()
	root := filepath.Join(source, "root")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "world.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	if err := writeDirectoryStream(&stream, root); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	saved, err := receiveDirectoryStream(&stream, output)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"hello.txt", filepath.Join("nested", "world.txt")} {
		got, err := os.ReadFile(filepath.Join(saved, relative))
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Base(relative)
		want = map[string]string{"hello.txt": "hello", "world.txt": "world"}[want]
		if string(got) != want {
			t.Fatalf("%s: got %q, want %q", relative, got, want)
		}
	}
}
