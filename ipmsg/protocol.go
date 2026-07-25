package ipmsg

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	DefaultPort = 2425

	CmdBrEntry     = 0x00000001
	CmdBrExit      = 0x00000002
	CmdAnsEntry    = 0x00000003
	CmdSendMsg     = 0x00000020
	CmdRecvMsg     = 0x00000021
	CmdGetFileData = 0x00000060
	CmdGetDirFiles = 0x00000062

	OptSendCheck  = 0x00000100
	OptFileAttach = 0x00200000

	FileRegular   = 0x00000001
	FileDirectory = 0x00000002
	FileRetParent = 0x00000003
)

type Identity struct {
	Version string
	Name    string
	Host    string
}

type Packet struct {
	Version  string
	PacketNo uint64
	User     string
	Host     string
	Command  uint64
	Extra    []byte
}

type Attachment struct {
	FileID  uint64
	Name    string
	Size    int64
	ModTime int64
	Attr    uint64
}

func EncodePacket(id Identity, packetNo, command uint64, extra []byte) []byte {
	var header bytes.Buffer
	fmt.Fprintf(&header, "%s:%d:", id.Version, packetNo)
	header.Write(encodeText(id.Name))
	header.WriteByte(':')
	header.Write(encodeText(id.Host))
	fmt.Fprintf(&header, ":%d:", command)
	out := append(header.Bytes(), extra...)
	return append(out, 0)
}

func DecodePacket(data []byte) (Packet, error) {
	data = bytes.TrimSuffix(data, []byte{0})
	parts := bytes.SplitN(data, []byte{':'}, 6)
	if len(parts) != 6 {
		return Packet{}, errors.New("invalid IPMsg packet: expected 6 fields")
	}
	packetNo, err := strconv.ParseUint(string(parts[1]), 10, 64)
	if err != nil {
		return Packet{}, fmt.Errorf("invalid packet number: %w", err)
	}
	command, err := strconv.ParseUint(string(parts[4]), 10, 64)
	if err != nil {
		return Packet{}, fmt.Errorf("invalid command: %w", err)
	}
	return Packet{
		Version:  string(parts[0]),
		PacketNo: packetNo,
		User:     string(parts[2]),
		Host:     string(parts[3]),
		Command:  command,
		Extra:    append([]byte(nil), parts[5]...),
	}, nil
}

func CommandMode(command uint64) uint64 {
	return command & 0xff
}

func EncodeAttachment(a Attachment) []byte {
	name := strings.ReplaceAll(a.Name, ":", "::")
	return []byte(fmt.Sprintf("%x:%s:%x:%x:%x:\a", a.FileID, name, a.Size, a.ModTime, a.Attr))
}

func DecodeAttachments(extra []byte) ([]Attachment, error) {
	nul := bytes.IndexByte(extra, 0)
	if nul < 0 || nul+1 >= len(extra) {
		return nil, nil
	}
	var result []Attachment
	for _, raw := range bytes.Split(extra[nul+1:], []byte{7}) {
		if len(raw) == 0 {
			continue
		}
		fields := splitEscapedColon(raw)
		if len(fields) < 5 {
			return nil, fmt.Errorf("invalid attachment metadata %q", raw)
		}
		fileID, err := strconv.ParseUint(string(fields[0]), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid file id: %w", err)
		}
		size, err := strconv.ParseInt(string(fields[2]), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid file size: %w", err)
		}
		modTime, err := strconv.ParseInt(string(fields[3]), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid modify time: %w", err)
		}
		attr, err := strconv.ParseUint(string(fields[4]), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid file attribute: %w", err)
		}
		result = append(result, Attachment{
			FileID:  fileID,
			Name:    decodeText(fields[1]),
			Size:    size,
			ModTime: modTime,
			Attr:    attr,
		})
	}
	return result, nil
}

func splitEscapedColon(raw []byte) [][]byte {
	var fields [][]byte
	var field []byte
	for i := 0; i < len(raw); i++ {
		if raw[i] != ':' {
			field = append(field, raw[i])
			continue
		}
		if i+1 < len(raw) && raw[i+1] == ':' {
			field = append(field, ':')
			i++
			continue
		}
		fields = append(fields, field)
		field = nil
	}
	if len(field) > 0 {
		fields = append(fields, field)
	}
	return fields
}

func encodeText(text string) []byte {
	// The cloned FeiQ implementation uses GBK on the wire. ASCII and UTF-8
	// remain usable without external dependencies; Chinese conversion is
	// supplied by the small table-free GB18030 codec in text_gbk.go.
	return encodeGBK(text)
}

func decodeText(raw []byte) string {
	return decodeGBK(raw)
}
