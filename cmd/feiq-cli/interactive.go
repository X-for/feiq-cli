package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"feiq-cli/internal/clipboard"
	"feiq-cli/internal/console"
	"feiq-cli/internal/history"
	"feiq-cli/ipmsg"
)

type interactiveCommand struct {
	kind    string
	target  string
	payload string
}

var interactiveCommands = []string{
	"/to ",
	"/msg ",
	"/file ",
	"/dir ",
	"/image",
	"/compose",
	"/users ",
	"/history ",
	"/help",
	"/exit",
}

func interactive(args []string) error {
	config, configPath, err := loadAppConfig(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("interactive", flag.ContinueOnError)
	var common commonFlags
	common.add(fs, config)
	addConfigFlag(fs, configPath)
	output := fs.String("output", configString(config.Output, "./downloads"), "directory for automatically received files/directories")
	historyPath := fs.String("history-file", configString(config.HistoryFile, history.DefaultPath()), "local JSONL chat history file")
	colorMode := fs.String("color", configString(config.Color, "auto"), "terminal colors: auto, always or never")
	messageWait := fs.Duration("message-wait", configDuration(config.MessageWait, 5*time.Second), "time to wait for message acknowledgement")
	transferWait := fs.Duration("transfer-wait", configDuration(config.TransferWait, 5*time.Minute), "time to keep each attachment offer active")
	if err := fs.Parse(args); err != nil {
		return err
	}
	historyStore, err := history.Open(*historyPath)
	if err != nil {
		return err
	}
	localUsers, err := historyStore.SearchUsers("")
	if err != nil {
		return err
	}

	terminal, err := console.New(os.Stdin, os.Stdout, "feiq> ")
	if err != nil {
		return err
	}
	if err := terminal.SetColorMode(*colorMode); err != nil {
		_ = terminal.Close()
		return err
	}
	terminal.SetCommands(interactiveCommands)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var sendWG sync.WaitGroup
	session, err := common.node().StartSession(
		ctx,
		*output,
		func(event ipmsg.ReceiveEvent) {
			recordReceiveEvent(historyStore, event, func(err error) {
				terminal.PrintfColor(console.ColorRed, "[%s] [历史记录错误] %v", clock(), err)
			})
			printReceiveEvent(terminal, event)
		},
		func(err error) { terminal.PrintfColor(console.ColorRed, "[%s] [错误] %v", clock(), err) },
	)
	if err != nil {
		cancel()
		_ = terminal.Close()
		return err
	}
	contacts := contactBook{session: session, local: localUsers}
	terminal.SetCompleter(func(line string) []console.Completion {
		return interactiveCompletions(line, contacts.search)
	})
	defer func() {
		cancel()
		session.Close()
		sendWG.Wait()
		_ = terminal.Close()
	}()

	sendText := func(target, text string) {
		appendHistory(terminal, historyStore, history.Entry{Direction: "out", PeerIP: target, Kind: "msg", Content: text})
		terminal.PrintfColor(console.ColorCyan, "[%s] [我 -> %s] 消息: %s", clock(), target, text)
		sendWG.Add(1)
		go func() {
			defer sendWG.Done()
			sendCtx, stop := context.WithTimeout(ctx, *messageWait)
			defer stop()
			acked, err := session.SendMessage(sendCtx, target, text)
			if err != nil {
				terminal.PrintfColor(console.ColorRed, "[%s] [发送失败 -> %s] %v", clock(), target, err)
			} else if acked {
				terminal.PrintfColor(console.ColorGreen, "[%s] [已送达 -> %s] 对方已确认接收", clock(), target)
			} else {
				terminal.PrintfColor(console.ColorYellow, "[%s] [未确认 -> %s] 回执等待超时", clock(), target)
			}
		}()
	}

	sendPath := func(target, kind, path string) {
		label := "文件"
		if kind == "dir" {
			label = "目录"
		}
		appendHistory(terminal, historyStore, history.Entry{Direction: "out", PeerIP: target, Kind: kind, Content: path})
		terminal.PrintfColor(console.ColorMagenta, "[%s] [我 -> %s] %s: %s（等待对方接收）", clock(), target, label, path)
		sendWG.Add(1)
		go func() {
			defer sendWG.Done()
			sendCtx, stop := context.WithTimeout(ctx, *transferWait)
			defer stop()
			if err := session.SendPath(sendCtx, target, path); err != nil {
				terminal.PrintfColor(console.ColorRed, "[%s] [%s发送失败 -> %s] %v", clock(), label, target, err)
				return
			}
			terminal.PrintfColor(console.ColorGreen, "[%s] [%s发送完成 -> %s] %s", clock(), label, target, path)
		}()
	}

	sendImage := func(target string) {
		file, err := os.CreateTemp("", "feiq-clipboard-*.png")
		if err != nil {
			terminal.PrintfColor(console.ColorRed, "[%s] [图片发送失败] %v", clock(), err)
			return
		}
		imagePath := file.Name()
		_ = file.Close()
		if err := clipboard.SavePNG(imagePath); err != nil {
			_ = os.Remove(imagePath)
			terminal.PrintfColor(console.ColorRed, "[%s] [图片发送失败] %v", clock(), err)
			return
		}
		appendHistory(terminal, historyStore, history.Entry{Direction: "out", PeerIP: target, Kind: "file", Content: "剪贴板图片"})
		terminal.PrintfColor(console.ColorMagenta, "[%s] [我 -> %s] 图片: 剪贴板图片（等待对方接收）", clock(), target)
		sendWG.Add(1)
		go func() {
			defer sendWG.Done()
			defer os.Remove(imagePath)
			sendCtx, stop := context.WithTimeout(ctx, *transferWait)
			defer stop()
			if err := session.SendPath(sendCtx, target, imagePath); err != nil {
				terminal.PrintfColor(console.ColorRed, "[%s] [图片发送失败 -> %s] %v", clock(), target, err)
				return
			}
			terminal.PrintfColor(console.ColorGreen, "[%s] [图片发送完成 -> %s]", clock(), target)
		}()
	}

	var current contact
	var composing bool
	var composedLines []string
	resetPrompt := func() {
		if current.IP == "" {
			terminal.SetPrompt("feiq> ")
			return
		}
		name := current.Name
		if name == "" {
			name = current.IP
		}
		terminal.SetPrompt(fmt.Sprintf("feiq[%s]> ", name))
	}
	requireCurrent := func() bool {
		if current.IP != "" {
			return true
		}
		terminal.PrintfColor(console.ColorYellow, "请先使用 /to <用户名或 IP> 选择联系人")
		return false
	}

	terminal.PrintfColor(console.ColorBlue, "[%s] feiq-cli 已启动，监听 %s:%d，附件保存到 %s", clock(), common.bind, common.port, *output)
	terminal.PrintfColor(console.ColorGray, "聊天记录保存到 %s", historyStore.Path())
	terminal.PrintfColor(console.ColorBlue, "使用 /to <用户名或 IP> 选择联系人；输入 /help 查看帮助")
	for {
		line, err := terminal.ReadLine()
		if errors.Is(err, console.ErrInterrupt) || errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if composing {
			switch line {
			case ".send":
				if len(composedLines) == 0 {
					terminal.PrintfColor(console.ColorYellow, "多行消息为空；输入内容、.cancel 取消")
					continue
				}
				text := strings.Join(composedLines, "\n")
				composing = false
				composedLines = composedLines[:0]
				resetPrompt()
				sendText(current.IP, text)
			case ".cancel":
				composing = false
				composedLines = composedLines[:0]
				resetPrompt()
				terminal.PrintfColor(console.ColorGray, "已取消多行消息")
			default:
				composedLines = append(composedLines, line)
				terminal.SetPrompt(fmt.Sprintf("...[%d]> ", len(composedLines)+1))
			}
			continue
		}
		command, err := parseInteractiveCommand(line)
		if err != nil {
			terminal.PrintfColor(console.ColorRed, "[%s] [命令错误] %v", clock(), err)
			continue
		}
		switch command.kind {
		case "":
			continue
		case "help":
			printInteractiveHelp(terminal)
		case "quit":
			return nil
		case "select":
			selected, err := contacts.resolve(command.payload)
			if err != nil {
				terminal.PrintfColor(console.ColorRed, "[%s] [联系人错误] %v", clock(), err)
				continue
			}
			current = selected
			resetPrompt()
			name := selected.Name
			if name == "" {
				name = "未知名称"
			}
			terminal.PrintfColor(console.ColorBlue, "当前联系人: %s (%s)", name, selected.IP)
		case "history":
			target := current.IP
			if command.payload != "" {
				selected, err := contacts.resolve(command.payload)
				if err != nil {
					terminal.PrintfColor(console.ColorRed, "[%s] [联系人错误] %v", clock(), err)
					continue
				}
				target = selected.IP
			}
			if target == "" {
				terminal.PrintfColor(console.ColorYellow, "请提供联系人，或先使用 /to 选择联系人")
				continue
			}
			printHistory(terminal, historyStore, target)
		case "users":
			_ = session.Discover()
			time.Sleep(350 * time.Millisecond)
			printUsers(terminal, historyStore, session, command.payload)
		case "compose":
			if !requireCurrent() {
				continue
			}
			composing = true
			composedLines = composedLines[:0]
			terminal.SetPrompt("...[1]> ")
			terminal.PrintfColor(console.ColorBlue, "多行模式：逐行输入，使用 .send 发送，.cancel 取消")
		case "msg-current":
			if requireCurrent() {
				sendText(current.IP, command.payload)
			}
		case "image":
			target := command.target
			if target == "" {
				if !requireCurrent() {
					continue
				}
				target = current.IP
			}
			sendImage(target)
		case "file", "dir":
			target := command.target
			if target == "" {
				if !requireCurrent() {
					continue
				}
				target = current.IP
			}
			if err := validateInteractivePath(command.kind, command.payload); err != nil {
				terminal.PrintfColor(console.ColorRed, "[%s] [命令错误] %v", clock(), err)
				continue
			}
			sendPath(target, command.kind, command.payload)
		case "msg":
			sendText(command.target, command.payload)
		}
	}
}

func parseInteractiveCommand(line string) (interactiveCommand, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return interactiveCommand{}, nil
	}
	if trimmed == "/help" {
		return interactiveCommand{kind: "help"}, nil
	}
	if trimmed == "exit" || trimmed == "quit" || trimmed == "/quit" || trimmed == "/exit" {
		return interactiveCommand{kind: "quit"}, nil
	}
	if !strings.HasPrefix(trimmed, "/") {
		return interactiveCommand{kind: "msg-current", payload: line}, nil
	}

	command, rest := cutInteractiveField(line)
	switch command {
	case "/to":
		if rest == "" {
			return interactiveCommand{}, fmt.Errorf("用法: /to <用户名或 IP>")
		}
		return interactiveCommand{kind: "select", payload: rest}, nil
	case "/msg":
		payload := remainderAfterCommand(line, command)
		if payload == "" {
			return interactiveCommand{}, fmt.Errorf("用法: /msg <消息>")
		}
		return interactiveCommand{kind: "msg-current", payload: payload}, nil
	case "/file", "/dir":
		path := remainderAfterCommand(line, command)
		if unquoted, err := strconv.Unquote(path); err == nil {
			path = unquoted
		}
		if path == "" {
			return interactiveCommand{}, fmt.Errorf("用法: %s <路径>", command)
		}
		return interactiveCommand{kind: strings.TrimPrefix(command, "/"), payload: path}, nil
	case "/image":
		if rest != "" {
			return interactiveCommand{}, fmt.Errorf("用法: /image")
		}
		return interactiveCommand{kind: "image"}, nil
	case "/compose":
		if rest != "" {
			return interactiveCommand{}, fmt.Errorf("用法: /compose")
		}
		return interactiveCommand{kind: "compose"}, nil
	case "/users":
		return interactiveCommand{kind: "users", payload: rest}, nil
	case "/history":
		return interactiveCommand{kind: "history", payload: rest}, nil
	case "/search":
		scope, query := cutInteractiveField(rest)
		if scope != "user" {
			return interactiveCommand{}, fmt.Errorf("用法: /search user [关键词]")
		}
		return interactiveCommand{kind: "users", payload: query}, nil
	}

	if command != "/send" {
		return interactiveCommand{}, fmt.Errorf("未知命令；输入 /help 查看用法")
	}
	kind, rest := cutInteractiveField(rest)
	if kind != "msg" && kind != "file" && kind != "dir" && kind != "image" {
		return interactiveCommand{}, fmt.Errorf("发送类型必须是 msg、file、dir 或 image")
	}
	target, payload := cutInteractiveField(rest)
	if target == "" {
		return interactiveCommand{}, fmt.Errorf("目标地址不能为空")
	}
	if kind == "image" {
		if payload != "" {
			return interactiveCommand{}, fmt.Errorf("用法: /send image <IP>")
		}
		return interactiveCommand{kind: kind, target: target}, nil
	}
	if kind == "file" || kind == "dir" {
		if unquoted, err := strconv.Unquote(payload); err == nil {
			payload = unquoted
		}
	}
	if payload == "" {
		return interactiveCommand{}, fmt.Errorf("发送内容不能为空")
	}
	return interactiveCommand{kind: kind, target: target, payload: payload}, nil
}

func remainderAfterCommand(line, command string) string {
	index := strings.Index(line, command)
	if index < 0 {
		return ""
	}
	rest := line[index+len(command):]
	return strings.TrimLeft(rest, " \t")
}

func cutInteractiveField(input string) (string, string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", ""
	}
	if index := strings.IndexAny(input, " \t"); index >= 0 {
		return input[:index], strings.TrimSpace(input[index:])
	}
	return input, ""
}

type contact struct {
	IP     string
	Name   string
	Host   string
	Online bool
}

type contactBook struct {
	session *ipmsg.Session
	local   []history.User
}

func (book contactBook) search(query string) []contact {
	query = strings.ToLower(strings.TrimSpace(query))
	byIP := make(map[string]contact)
	for _, user := range book.local {
		if query != "" && !containsContactQuery(user.IP, user.Name, "", query) {
			continue
		}
		byIP[user.IP] = contact{IP: user.IP, Name: user.Name}
	}
	if book.session != nil {
		for _, peer := range book.session.SearchPeers(query) {
			byIP[peer.IP] = contact{
				IP:     peer.IP,
				Name:   peer.Name,
				Host:   peer.Host,
				Online: true,
			}
		}
	}
	result := make([]contact, 0, len(byIP))
	for _, item := range byIP {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Online != result[j].Online {
			return result[i].Online
		}
		left := strings.ToLower(result[i].Name)
		right := strings.ToLower(result[j].Name)
		if left != right {
			return left < right
		}
		return result[i].IP < result[j].IP
	})
	return result
}

func containsContactQuery(ip, name, host, query string) bool {
	return strings.Contains(strings.ToLower(ip), query) ||
		strings.Contains(strings.ToLower(name), query) ||
		strings.Contains(strings.ToLower(host), query)
}

func (book contactBook) resolve(query string) (contact, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return contact{}, fmt.Errorf("联系人不能为空")
	}
	matches := book.search(query)
	for _, item := range matches {
		if item.IP == query || strings.EqualFold(item.Name, query) || strings.EqualFold(item.Host, query) {
			return item, nil
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		names := make([]string, 0, min(len(matches), 5))
		for _, item := range matches {
			names = append(names, contactLabel(item))
			if len(names) == 5 {
				break
			}
		}
		return contact{}, fmt.Errorf("匹配到多个联系人: %s；请继续输入名称或 IP", strings.Join(names, "、"))
	}
	if validTarget(query) {
		return contact{IP: query}, nil
	}
	return contact{}, fmt.Errorf("找不到 %q；先用 /users 搜索在线用户", query)
}

func validTarget(value string) bool {
	host := value
	if parsedHost, _, err := net.SplitHostPort(value); err == nil {
		host = parsedHost
	}
	return net.ParseIP(host) != nil
}

func contactLabel(item contact) string {
	name := item.Name
	if name == "" {
		name = item.Host
	}
	if name == "" {
		return item.IP
	}
	return fmt.Sprintf("%s (%s)", name, item.IP)
}

func interactiveCompletions(line string, findContacts func(string) []contact) []console.Completion {
	var commands []console.Completion
	for _, command := range interactiveCommands {
		if command != line && strings.HasPrefix(command, line) {
			commands = append(commands, console.Completion{
				Value:   command,
				Display: strings.TrimSpace(command),
			})
		}
	}
	if len(commands) > 0 {
		return commands
	}
	command, rest := cutInteractiveField(line)
	if command == "/to" || command == "/history" {
		items := findContacts(rest)
		result := make([]console.Completion, 0, len(items))
		for _, item := range items {
			result = append(result, console.Completion{
				Value:   command + " " + item.IP,
				Display: contactLabel(item),
			})
		}
		return result
	}
	if command == "/file" || command == "/dir" {
		return pathCompletions(command+" ", remainderAfterCommand(line, command), command == "/dir")
	}
	kind, legacyRest := cutInteractiveField(rest)
	target, _ := cutInteractiveField(legacyRest)
	if command != "/send" || target == "" || kind != "file" && kind != "dir" {
		return nil
	}
	targetAt := strings.Index(line, target)
	if targetAt < 0 {
		return nil
	}
	prefixEnd := targetAt + len(target)
	commandPrefix := line[:prefixEnd] + " "
	pathInput := strings.TrimSpace(line[prefixEnd:])
	return pathCompletions(commandPrefix, pathInput, kind == "dir")
}

func pathCompletions(commandPrefix, input string, directoriesOnly bool) []console.Completion {
	values := completeLocalPaths(commandPrefix, input, directoriesOnly)
	result := make([]console.Completion, 0, len(values))
	for _, value := range values {
		result = append(result, console.Completion{
			Value:   value,
			Display: strings.TrimPrefix(value, commandPrefix),
		})
	}
	return result
}

func completeLocalPaths(commandPrefix, input string, directoriesOnly bool) []string {
	input = strings.TrimSpace(input)
	if unquoted, err := strconv.Unquote(input); err == nil {
		input = unquoted
	} else {
		input = strings.TrimPrefix(input, "\"")
		input = strings.TrimSuffix(input, "\"")
	}
	expanded := input
	if expanded == "~" || strings.HasPrefix(expanded, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			expanded = filepath.Join(home, strings.TrimPrefix(expanded, "~/"))
		}
	}
	directory := filepath.Dir(expanded)
	base := filepath.Base(expanded)
	displayDirectory := filepath.Dir(input)
	if input == "" {
		directory, base, displayDirectory = ".", "", "."
	} else if strings.HasSuffix(input, string(os.PathSeparator)) {
		directory, base, displayDirectory = expanded, "", input
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil
	}
	var result []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), base) || directoriesOnly && !entry.IsDir() {
			continue
		}
		candidate := entry.Name()
		if displayDirectory != "." {
			candidate = filepath.Join(displayDirectory, candidate)
		}
		if entry.IsDir() {
			candidate += string(os.PathSeparator)
		}
		if strings.ContainsAny(candidate, " \t") {
			candidate = strconv.Quote(candidate)
		}
		result = append(result, commandPrefix+candidate)
	}
	return result
}

func validateInteractivePath(kind, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if kind == "file" && !info.Mode().IsRegular() {
		return fmt.Errorf("%s 不是普通文件", path)
	}
	if kind == "dir" && !info.IsDir() {
		return fmt.Errorf("%s 不是目录", path)
	}
	return nil
}

func printReceiveEvent(terminal *console.Terminal, event ipmsg.ReceiveEvent) {
	if event.Text != "" {
		terminal.PrintfColor(console.ColorGreen, "[%s] [%s %s] 消息: %s", clock(), event.User, event.From, event.Text)
	}
	for index, attachment := range event.Attachments {
		label := "文件"
		if attachment.Attr&0xff == ipmsg.FileDirectory {
			label = "目录"
		}
		if index < len(event.SavedPaths) && event.SavedPaths[index] != "" {
			terminal.PrintfColor(console.ColorMagenta, "[%s] [%s %s] 收到%s: %s -> %s", clock(), event.User, event.From, label, attachment.Name, event.SavedPaths[index])
		} else {
			terminal.PrintfColor(console.ColorYellow, "[%s] [%s %s] 收到%s通知: %s（下载未完成）", clock(), event.User, event.From, label, attachment.Name)
		}
	}
}

func printInteractiveHelp(terminal *console.Terminal) {
	terminal.PrintfColor(console.ColorBlue, "交互方式:\n  /to <用户名或 IP>  选择联系人（可按 Tab 补全）\n  直接输入文字       发送给当前联系人\n  /msg <消息>        发送单行消息\n  /compose           输入多行消息；.send 发送，.cancel 取消\n  /file <路径>       发送文件（可按 Tab 补全路径）\n  /dir <路径>        发送目录（可按 Tab 补全路径）\n  /image             发送剪贴板图片\n  /users [关键词]    搜索在线及本地联系人\n  /history [联系人]  查看聊天记录\n  /help\n  /exit\n\n↑/↓ 切换完整输入历史，←/→ 移动光标。旧版 /send ... 和 /search user ... 命令仍可使用。")
}

func appendHistory(terminal *console.Terminal, store *history.Store, entry history.Entry) {
	if err := store.Append(entry); err != nil {
		terminal.PrintfColor(console.ColorRed, "[%s] [历史记录错误] %v", clock(), err)
	}
}

func recordReceiveEvent(store *history.Store, event ipmsg.ReceiveEvent, onError func(error)) {
	if event.Text != "" {
		if err := store.Append(history.Entry{Direction: "in", PeerIP: event.From, PeerName: event.User, Kind: "msg", Content: event.Text}); err != nil {
			onError(err)
		}
	}
	for index, attachment := range event.Attachments {
		kind := "file"
		if attachment.Attr&0xff == ipmsg.FileDirectory {
			kind = "dir"
		}
		entry := history.Entry{Direction: "in", PeerIP: event.From, PeerName: event.User, Kind: kind, Content: attachment.Name}
		if index < len(event.SavedPaths) && event.SavedPaths[index] != "" {
			entry.SavedPath = event.SavedPaths[index]
		}
		if err := store.Append(entry); err != nil {
			onError(err)
		}
	}
}

func printHistory(terminal *console.Terminal, store *history.Store, peerIP string) {
	entries, err := store.History(peerIP, 50)
	if err != nil {
		terminal.PrintfColor(console.ColorRed, "[%s] [历史记录错误] %v", clock(), err)
		return
	}
	if len(entries) == 0 {
		terminal.PrintfColor(console.ColorYellow, "没有找到与 %s 的本地聊天记录", peerIP)
		return
	}
	terminal.PrintfColor(console.ColorBlue, "与 %s 最近的 %d 条记录:", peerIP, len(entries))
	for _, entry := range entries {
		arrow := "我 ->"
		if entry.Direction == "in" {
			arrow = "<- " + entry.PeerName
		}
		content := entry.Content
		if entry.SavedPath != "" {
			content += " -> " + entry.SavedPath
		}
		terminal.PrintfColor(console.ColorGray, "[%s] [%s] %s: %s", entry.Time.Local().Format("2006-01-02 15:04:05"), arrow, historyKindLabel(entry.Kind), content)
	}
}

func printUsers(terminal *console.Terminal, store *history.Store, session *ipmsg.Session, query string) {
	peers := session.SearchPeers(query)
	users, err := store.SearchUsers(query)
	if err != nil {
		terminal.PrintfColor(console.ColorRed, "[%s] [历史记录错误] %v", clock(), err)
		return
	}
	if len(peers) == 0 && len(users) == 0 {
		terminal.PrintfColor(console.ColorYellow, "没有发现匹配的在线用户，本地记录中也没有匹配用户")
		return
	}
	for _, peer := range peers {
		terminal.PrintfColor(console.ColorGreen, "[在线] %s  %s  主机 %s", peer.IP, peer.Name, peer.Host)
	}
	online := make(map[string]bool, len(peers))
	for _, peer := range peers {
		online[peer.IP] = true
	}
	for _, user := range users {
		if online[user.IP] {
			continue
		}
		name := user.Name
		if name == "" {
			name = "未知名称"
		}
		terminal.PrintfColor(console.ColorGray, "[本地] %s  %s  最近联系 %s  %d 条记录", user.IP, name, user.LastSeen.Local().Format("2006-01-02 15:04"), user.Count)
	}
}

func historyKindLabel(kind string) string {
	switch kind {
	case "file":
		return "文件"
	case "dir":
		return "目录"
	default:
		return "消息"
	}
}

func clock() string {
	return time.Now().Format("15:04:05")
}
