package ipmsg

import (
	"bufio"
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
	want := Attachment{FileID: 1, Name: "中文:a.txt", Size: 0x123, ModTime: 0x456, Attr: FileRegular}
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
	root := filepath.Join(source, "根目录")
	if err := os.MkdirAll(filepath.Join(root, "新建文件夹"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "新建文件夹", "世界.txt"), []byte("world"), 0o644); err != nil {
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
	for _, relative := range []string{"hello.txt", filepath.Join("新建文件夹", "世界.txt")} {
		got, err := os.ReadFile(filepath.Join(saved, relative))
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Base(relative)
		want = map[string]string{"hello.txt": "hello", "世界.txt": "world"}[want]
		if string(got) != want {
			t.Fatalf("%s: got %q, want %q", relative, got, want)
		}
	}
}

func TestDirectoryHeaderUsesFourHexDigitLength(t *testing.T) {
	var stream bytes.Buffer
	if err := writeDirHeader(&stream, dirRecord{Name: "root", Attr: FileDirectory}); err != nil {
		t.Fatal(err)
	}
	if got, want := stream.String(), "0016:root:000000000:2:"; got != want {
		t.Fatalf("directory header = %q, want %q", got, want)
	}
}

func TestDirectoryHeaderEncodesChineseNameAsGBK(t *testing.T) {
	var stream bytes.Buffer
	if err := writeDirHeader(&stream, dirRecord{Name: "新建文件夹", Attr: FileDirectory}); err != nil {
		t.Fatal(err)
	}
	raw := stream.Bytes()
	if bytes.Contains(raw, []byte("新建文件夹")) {
		t.Fatalf("directory header contains UTF-8 filename: %x", raw)
	}
	record, err := readDirHeader(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if record.Name != "新建文件夹" || record.Attr != FileDirectory {
		t.Fatalf("unexpected directory record: %#v", record)
	}
}

func TestDirectoryHeaderAcceptsFeiQBinaryPreamble(t *testing.T) {
	var stream bytes.Buffer
	stream.WriteString("0<")
	stream.Write(make([]byte, 4096))
	stream.WriteString("0016:root:000000000:2:")
	record, err := readDirHeader(bufio.NewReader(&stream))
	if err != nil {
		t.Fatal(err)
	}
	if record.Name != "root" || record.Size != 0 || record.Attr != FileDirectory {
		t.Fatalf("unexpected directory record: %#v", record)
	}
}
