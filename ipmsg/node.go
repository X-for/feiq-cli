package ipmsg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"
)

type Node struct {
	Identity Identity
	BindIP   string
	Port     int
	seq      atomic.Uint64
}

type ReceiveEvent struct {
	From        string
	User        string
	Text        string
	SavedPaths  []string
	Attachments []Attachment
}

func NewNode(identity Identity, bindIP string, port int) *Node {
	if identity.Version == "" {
		identity.Version = "1"
	}
	if identity.Name == "" {
		identity.Name = "feiq-cli"
	}
	if identity.Host == "" {
		identity.Host = "feiq-cli"
	}
	if bindIP == "" {
		bindIP = "0.0.0.0"
	}
	if port == 0 {
		port = DefaultPort
	}
	node := &Node{Identity: identity, BindIP: bindIP, Port: port}
	node.seq.Store(uint64(time.Now().Unix()))
	return node
}

func (n *Node) nextPacketNo() uint64 {
	return n.seq.Add(1)
}

func (n *Node) listenUDP() (*net.UDPConn, error) {
	addr := &net.UDPAddr{IP: net.ParseIP(n.BindIP), Port: n.Port}
	return net.ListenUDP("udp4", addr)
}

func (n *Node) SendMessage(ctx context.Context, target, text string) (bool, error) {
	conn, err := n.listenUDP()
	if err != nil {
		return false, fmt.Errorf("listen UDP %s:%d: %w", n.BindIP, n.Port, err)
	}
	defer conn.Close()
	packetNo := n.nextPacketNo()
	data := EncodePacket(n.Identity, packetNo, CmdSendMsg|OptSendCheck, encodeText(text))
	targetAddr, err := resolveTarget(target, n.Port)
	if err != nil {
		return false, err
	}
	if _, err := conn.WriteToUDP(data, targetAddr); err != nil {
		return false, err
	}
	buf := make([]byte, 64*1024)
	for {
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetReadDeadline(deadline)
		}
		size, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if isTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
				return false, nil
			}
			return false, err
		}
		packet, err := DecodePacket(buf[:size])
		if err != nil || CommandMode(packet.Command) != CmdRecvMsg {
			continue
		}
		ack, err := strconv.ParseUint(string(bytes.TrimRight(packet.Extra, "\x00")), 10, 64)
		if err == nil && ack == packetNo {
			return true, nil
		}
	}
}

func (n *Node) SendPath(ctx context.Context, target, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return fmt.Errorf("only regular files and directories are supported: %s", path)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(n.BindIP, strconv.Itoa(n.Port)))
	if err != nil {
		return fmt.Errorf("listen TCP %s:%d: %w", n.BindIP, n.Port, err)
	}
	defer listener.Close()

	var size int64
	attr := uint64(FileRegular)
	if info.IsDir() {
		attr = FileDirectory
		size, err = directorySize(path)
	} else {
		size = info.Size()
	}
	if err != nil {
		return err
	}
	attachment := Attachment{
		FileID:  1,
		Name:    info.Name(),
		Size:    size,
		ModTime: info.ModTime().Unix(),
		Attr:    attr,
	}
	packetNo := n.nextPacketNo()
	extra := append([]byte{0}, EncodeAttachment(attachment)...)
	data := EncodePacket(n.Identity, packetNo, CmdSendMsg|OptFileAttach, extra)
	udp, err := n.listenUDP()
	if err != nil {
		return fmt.Errorf("listen UDP %s:%d: %w", n.BindIP, n.Port, err)
	}
	defer udp.Close()
	targetAddr, err := resolveTarget(target, n.Port)
	if err != nil {
		return err
	}
	if _, err := udp.WriteToUDP(data, targetAddr); err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	conn, err := listener.Accept()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("waiting for receiver request: %w", ctx.Err())
		}
		return err
	}
	defer conn.Close()
	remote, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok || !remote.IP.Equal(targetAddr.IP) {
		return fmt.Errorf("rejected transfer request from unexpected peer %s", conn.RemoteAddr())
	}
	return n.servePathRequest(conn, packetNo, attachment.FileID, path, info.IsDir())
}

func (n *Node) servePathRequest(conn net.Conn, packetNo, fileID uint64, path string, directory bool) error {
	request, err := readTransferRequest(conn)
	if err != nil {
		return err
	}
	if request.fileID != fileID {
		return fmt.Errorf(
			"request does not match offered path (packet=%q file=%x, offered packet=%d file=%x)",
			request.packetText, request.fileID, packetNo, fileID,
		)
	}
	return sendTransferData(conn, request, path, directory)
}

type transferRequest struct {
	command    uint64
	packetText string
	fileID     uint64
	offset     int64
}

func readTransferRequest(conn net.Conn) (transferRequest, error) {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	raw, err := bufio.NewReader(conn).ReadBytes(0)
	if err != nil && len(raw) == 0 {
		return transferRequest{}, fmt.Errorf("read transfer request: %w", err)
	}
	// Some FeiQ variants omit the trailing NUL on TCP transfer requests.
	// ReadBytes returns the already received packet together with a timeout or
	// EOF in that case, and DecodePacket intentionally accepts either form.
	request, err := DecodePacket(raw)
	if err != nil {
		return transferRequest{}, err
	}
	fields := bytes.Split(request.Extra, []byte{':'})
	if len(fields) < 3 {
		return transferRequest{}, fmt.Errorf("invalid transfer request")
	}
	requestPacketText := string(fields[0])
	if _, err := strconv.ParseUint(requestPacketText, 16, 64); err != nil {
		return transferRequest{}, fmt.Errorf("invalid requested packet number: %w", err)
	}
	requestFile, err := strconv.ParseUint(string(fields[1]), 16, 64)
	if err != nil {
		return transferRequest{}, fmt.Errorf("invalid requested file id: %w", err)
	}
	var offset int64
	if len(fields[2]) > 0 {
		offset, err = strconv.ParseInt(string(fields[2]), 16, 64)
		if err != nil {
			return transferRequest{}, fmt.Errorf("invalid requested offset: %w", err)
		}
	} else if CommandMode(request.Command) != CmdGetDirFiles {
		return transferRequest{}, fmt.Errorf("file request omitted its offset")
	}
	return transferRequest{
		command:    request.Command,
		packetText: requestPacketText,
		fileID:     requestFile,
		offset:     offset,
	}, nil
}

func sendTransferData(conn net.Conn, request transferRequest, path string, directory bool) error {
	_ = conn.SetReadDeadline(time.Time{})
	_ = conn.SetWriteDeadline(time.Time{})
	if directory {
		if CommandMode(request.command) != CmdGetDirFiles {
			return fmt.Errorf("expected GETDIRFILES, got %#x", request.command)
		}
		if request.offset != 0 {
			return fmt.Errorf("directory resume offsets are not supported")
		}
		return writeDirectoryStream(conn, path)
	}
	if CommandMode(request.command) != CmdGetFileData {
		return fmt.Errorf("expected GETFILEDATA, got %#x", request.command)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(request.offset, io.SeekStart); err != nil {
		return err
	}
	_, err = io.Copy(conn, file)
	return err
}

func (n *Node) Receive(ctx context.Context, outputDir string, onEvent func(ReceiveEvent)) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	conn, err := n.listenUDP()
	if err != nil {
		return fmt.Errorf("listen UDP %s:%d: %w", n.BindIP, n.Port, err)
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	buf := make([]byte, 64*1024)
	for {
		size, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		packet, err := DecodePacket(buf[:size])
		if err != nil || CommandMode(packet.Command) != CmdSendMsg {
			continue
		}
		event := ReceiveEvent{From: from.IP.String(), User: decodeText([]byte(packet.User))}
		textRaw := packet.Extra
		if nul := bytes.IndexByte(textRaw, 0); nul >= 0 {
			textRaw = textRaw[:nul]
		}
		event.Text = decodeText(textRaw)

		if packet.Command&OptSendCheck != 0 {
			ack := EncodePacket(n.Identity, n.nextPacketNo(), CmdRecvMsg, []byte(strconv.FormatUint(packet.PacketNo, 10)))
			_, _ = conn.WriteToUDP(ack, from)
		}
		if packet.Command&OptFileAttach != 0 {
			event.Attachments, err = DecodeAttachments(packet.Extra)
			if err != nil {
				return err
			}
			for _, attachment := range event.Attachments {
				path, err := n.downloadAttachment(ctx, from.IP.String(), from.Port, packet.PacketNo, attachment, outputDir)
				if err != nil {
					return err
				}
				event.SavedPaths = append(event.SavedPaths, path)
			}
		}
		if onEvent != nil {
			onEvent(event)
		}
	}
}

func (n *Node) downloadAttachment(ctx context.Context, sender string, senderPort int, packetNo uint64, attachment Attachment, outputDir string) (string, error) {
	const (
		attachmentReadyDelay = 150 * time.Millisecond
		attachmentAttempts   = 3
		attachmentRetryDelay = 200 * time.Millisecond
	)
	if err := waitContext(ctx, attachmentReadyDelay); err != nil {
		return "", err
	}
	ports := []int{senderPort}
	if senderPort != n.Port {
		ports = append(ports, n.Port)
	}
	var lastErr error
	for _, port := range ports {
		for attempt := 1; attempt <= attachmentAttempts; attempt++ {
			path, err := n.downloadAttachmentAttempt(ctx, sender, port, packetNo, attachment, outputDir)
			if err == nil {
				return path, nil
			}
			if !errors.Is(err, errEmptyTransfer) {
				return "", fmt.Errorf(
					"download request to %s:%d (packet=%x file=%x): %w",
					sender, port, packetNo, attachment.FileID, err,
				)
			}
			lastErr = err
			if attempt < attachmentAttempts {
				if err := waitContext(ctx, attachmentRetryDelay); err != nil {
					return "", err
				}
			}
		}
	}
	return "", fmt.Errorf(
		"attachment sender %s ports %v closed without data (packet=%x file=%x) after %d attempts each: %w",
		sender, ports, packetNo, attachment.FileID, attachmentAttempts, lastErr,
	)
}

var errEmptyTransfer = errors.New("attachment sender closed without data")

func (n *Node) downloadAttachmentAttempt(ctx context.Context, sender string, senderPort int, packetNo uint64, attachment Attachment, outputDir string) (string, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp4", net.JoinHostPort(sender, strconv.Itoa(senderPort)))
	if err != nil {
		return "", fmt.Errorf("connect to attachment sender: %w", err)
	}
	defer conn.Close()
	stopClose := make(chan struct{})
	defer close(stopClose)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopClose:
		}
	}()
	command := uint64(CmdGetFileData)
	if attachment.Attr&0xff == FileDirectory {
		command = CmdGetDirFiles
	}
	extra := []byte(fmt.Sprintf("%x:%x:0", packetNo, attachment.FileID))
	request := EncodePacket(n.Identity, n.nextPacketNo(), command, extra)
	if _, err := conn.Write(request); err != nil {
		return "", err
	}
	reader := bufio.NewReader(conn)
	if _, err := reader.Peek(1); err != nil {
		if errors.Is(err, io.EOF) {
			return "", errEmptyTransfer
		}
		return "", err
	}
	if CommandMode(command) == CmdGetDirFiles {
		return receiveDirectoryStream(reader, outputDir)
	}
	name, err := safeName(attachment.Name)
	if err != nil {
		return "", err
	}
	path, err := uniquePath(outputDir, name)
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	_, copyErr := io.CopyN(file, reader, attachment.Size)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(path)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", closeErr
	}
	return filepath.Clean(path), nil
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func resolveTarget(target string, defaultPort int) (*net.UDPAddr, error) {
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(target, strconv.Itoa(defaultPort))
	}
	return net.ResolveUDPAddr("udp4", target)
}
