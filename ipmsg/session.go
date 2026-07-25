package ipmsg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
)

// Session owns one UDP socket and one TCP listener so a long-running process
// can receive messages while also sending messages and serving attachments.
type Session struct {
	node      *Node
	udp       *net.UDPConn
	tcp       net.Listener
	outputDir string
	onEvent   func(ReceiveEvent)
	onError   func(error)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	ackMu      sync.Mutex
	ackWaiters map[uint64]chan struct{}

	offerMu sync.Mutex
	offers  map[string]*sessionOffer
	fileSeq atomic.Uint64
}

type sessionOffer struct {
	path      string
	directory bool
	done      chan error
	once      sync.Once
}

// StartSession starts the shared UDP/TCP service used by interactive mode.
func (n *Node) StartSession(
	parent context.Context,
	outputDir string,
	onEvent func(ReceiveEvent),
	onError func(error),
) (*Session, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, err
	}
	udp, err := n.listenUDP()
	if err != nil {
		return nil, fmt.Errorf("listen UDP %s:%d: %w", n.BindIP, n.Port, err)
	}
	tcp, err := net.Listen("tcp4", net.JoinHostPort(n.BindIP, strconv.Itoa(n.Port)))
	if err != nil {
		_ = udp.Close()
		return nil, fmt.Errorf("listen TCP %s:%d: %w", n.BindIP, n.Port, err)
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Session{
		node:       n,
		udp:        udp,
		tcp:        tcp,
		outputDir:  outputDir,
		onEvent:    onEvent,
		onError:    onError,
		ctx:        ctx,
		cancel:     cancel,
		ackWaiters: make(map[uint64]chan struct{}),
		offers:     make(map[string]*sessionOffer),
	}
	s.wg.Add(2)
	go s.udpLoop()
	go s.tcpLoop()
	go func() {
		<-ctx.Done()
		_ = udp.Close()
		_ = tcp.Close()
	}()
	return s, nil
}

func (s *Session) Close() {
	s.cancel()
	s.wg.Wait()
}

func (s *Session) Done() <-chan struct{} {
	return s.ctx.Done()
}

func (s *Session) SendMessage(ctx context.Context, target, message string) (bool, error) {
	packetNo := s.node.nextPacketNo()
	waiter := make(chan struct{}, 1)
	s.ackMu.Lock()
	s.ackWaiters[packetNo] = waiter
	s.ackMu.Unlock()
	defer func() {
		s.ackMu.Lock()
		delete(s.ackWaiters, packetNo)
		s.ackMu.Unlock()
	}()

	targetAddr, err := resolveTarget(target, s.node.Port)
	if err != nil {
		return false, err
	}
	packet := EncodePacket(s.node.Identity, packetNo, CmdSendMsg|OptSendCheck, encodeText(message))
	if _, err := s.udp.WriteToUDP(packet, targetAddr); err != nil {
		return false, err
	}
	select {
	case <-waiter:
		return true, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return false, nil
		}
		return false, ctx.Err()
	case <-s.ctx.Done():
		return false, s.ctx.Err()
	}
}

func (s *Session) SendPath(ctx context.Context, target, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return fmt.Errorf("only regular files and directories are supported: %s", path)
	}
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
	targetAddr, err := resolveTarget(target, s.node.Port)
	if err != nil {
		return err
	}
	fileID := s.fileSeq.Add(1)
	packetNo := s.node.nextPacketNo()
	attachment := Attachment{
		FileID:  fileID,
		Name:    info.Name(),
		Size:    size,
		ModTime: info.ModTime().Unix(),
		Attr:    attr,
	}
	offer := &sessionOffer{path: path, directory: info.IsDir(), done: make(chan error, 1)}
	key := offerKey(targetAddr.IP.String(), fileID)
	s.offerMu.Lock()
	s.offers[key] = offer
	s.offerMu.Unlock()
	defer func() {
		s.offerMu.Lock()
		delete(s.offers, key)
		s.offerMu.Unlock()
	}()

	extra := append([]byte{0}, EncodeAttachment(attachment)...)
	packet := EncodePacket(s.node.Identity, packetNo, CmdSendMsg|OptFileAttach, extra)
	if _, err := s.udp.WriteToUDP(packet, targetAddr); err != nil {
		return err
	}
	select {
	case err := <-offer.done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("waiting for receiver request: %w", ctx.Err())
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (s *Session) udpLoop() {
	defer s.wg.Done()
	buf := make([]byte, 64*1024)
	for {
		size, from, err := s.udp.ReadFromUDP(buf)
		if err != nil {
			if s.ctx.Err() == nil {
				s.reportError(err)
			}
			return
		}
		packet, err := DecodePacket(buf[:size])
		if err != nil {
			s.reportError(err)
			continue
		}
		switch CommandMode(packet.Command) {
		case CmdRecvMsg:
			s.handleAck(packet)
		case CmdSendMsg:
			if packet.Command&OptSendCheck != 0 {
				ack := EncodePacket(
					s.node.Identity,
					s.node.nextPacketNo(),
					CmdRecvMsg,
					[]byte(strconv.FormatUint(packet.PacketNo, 10)),
				)
				if _, err := s.udp.WriteToUDP(ack, from); err != nil {
					s.reportError(err)
				}
			}
			s.wg.Add(1)
			go func(packet Packet, from *net.UDPAddr) {
				defer s.wg.Done()
				s.handleIncoming(packet, from)
			}(packet, from)
		}
	}
}

func (s *Session) handleAck(packet Packet) {
	ack, err := strconv.ParseUint(string(bytes.TrimRight(packet.Extra, "\x00")), 10, 64)
	if err != nil {
		return
	}
	s.ackMu.Lock()
	waiter := s.ackWaiters[ack]
	s.ackMu.Unlock()
	if waiter != nil {
		select {
		case waiter <- struct{}{}:
		default:
		}
	}
}

func (s *Session) handleIncoming(packet Packet, from *net.UDPAddr) {
	event := ReceiveEvent{From: from.IP.String(), User: decodeText([]byte(packet.User))}
	textRaw := packet.Extra
	if nul := bytes.IndexByte(textRaw, 0); nul >= 0 {
		textRaw = textRaw[:nul]
	}
	event.Text = decodeText(textRaw)
	if packet.Command&OptFileAttach != 0 {
		attachments, err := DecodeAttachments(packet.Extra)
		if err != nil {
			s.reportError(err)
			return
		}
		event.Attachments = attachments
		for _, attachment := range attachments {
			path, err := s.node.downloadAttachment(
				s.ctx,
				from.IP.String(),
				from.Port,
				packet.PacketNo,
				attachment,
				s.outputDir,
			)
			if err != nil {
				s.reportError(fmt.Errorf("receive %s from %s: %w", attachment.Name, from.IP, err))
				continue
			}
			event.SavedPaths = append(event.SavedPaths, path)
		}
	}
	if s.onEvent != nil {
		s.onEvent(event)
	}
}

func (s *Session) tcpLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.tcp.Accept()
		if err != nil {
			if s.ctx.Err() == nil {
				s.reportError(err)
			}
			return
		}
		s.wg.Add(1)
		go func(conn net.Conn) {
			defer s.wg.Done()
			defer conn.Close()
			s.handleTransferRequest(conn)
		}(conn)
	}
}

func (s *Session) handleTransferRequest(conn net.Conn) {
	stopClose := make(chan struct{})
	defer close(stopClose)
	go func() {
		select {
		case <-s.ctx.Done():
			_ = conn.Close()
		case <-stopClose:
		}
	}()
	request, err := readTransferRequest(conn)
	if err != nil {
		s.reportError(err)
		return
	}
	remote, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		s.reportError(fmt.Errorf("unexpected TCP peer %s", conn.RemoteAddr()))
		return
	}
	key := offerKey(remote.IP.String(), request.fileID)
	s.offerMu.Lock()
	offer := s.offers[key]
	s.offerMu.Unlock()
	if offer == nil {
		s.reportError(fmt.Errorf("no active attachment offer for %s (file %x)", remote.IP, request.fileID))
		return
	}
	err = sendTransferData(conn, request, offer.path, offer.directory)
	offer.once.Do(func() { offer.done <- err })
}

func (s *Session) reportError(err error) {
	if err != nil && s.onError != nil {
		s.onError(err)
	}
}

func offerKey(ip string, fileID uint64) string {
	return ip + ":" + strconv.FormatUint(fileID, 16)
}
