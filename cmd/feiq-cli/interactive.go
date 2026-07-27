package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
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
	"/send msg ",
	"/send file ",
	"/send dir ",
	"/send image ",
	"/history ",
	"/search user ",
	"/help",
	"/exit",
	"/quit",
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
	recentTargets, err := historyStore.RecentTargets(20)
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
	terminal.SetCompleter(interactiveCompletions)
	terminal.SetTargets(recentTargets)
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
	defer func() {
		cancel()
		session.Close()
		sendWG.Wait()
		_ = terminal.Close()
	}()

	terminal.PrintfColor(console.ColorBlue, "[%s] feiq-cli 已启动，监听 %s:%d，附件保存到 %s", clock(), common.bind, common.port, *output)
	terminal.PrintfColor(console.ColorGray, "聊天记录保存到 %s", historyStore.Path())
	terminal.PrintfColor(console.ColorBlue, "输入 /help 查看交互命令，输入 exit 退出")
	for {
		line, err := terminal.ReadLine()
		if errors.Is(err, console.ErrInterrupt) || errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
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
		case "history":
			printHistory(terminal, historyStore, command.target)
		case "search-user":
			_ = session.Discover()
			time.Sleep(350 * time.Millisecond)
			printUsers(terminal, historyStore, session, command.payload)
		case "msg":
			terminal.RememberTarget(command.target)
			appendHistory(terminal, historyStore, history.Entry{Direction: "out", PeerIP: command.target, Kind: "msg", Content: command.payload})
			terminal.PrintfColor(console.ColorCyan, "[%s] [我 -> %s] 消息: %s", clock(), command.target, command.payload)
			sendWG.Add(1)
			go func(command interactiveCommand) {
				defer sendWG.Done()
				sendCtx, stop := context.WithTimeout(ctx, *messageWait)
				defer stop()
				acked, err := session.SendMessage(sendCtx, command.target, command.payload)
				if err != nil {
					terminal.PrintfColor(console.ColorRed, "[%s] [发送失败 -> %s] %v", clock(), command.target, err)
				} else if acked {
					terminal.PrintfColor(console.ColorGreen, "[%s] [已送达 -> %s] 对方已确认接收", clock(), command.target)
				} else {
					terminal.PrintfColor(console.ColorYellow, "[%s] [未确认 -> %s] 回执等待超时", clock(), command.target)
				}
			}(command)
		case "image":
			terminal.RememberTarget(command.target)
			file, err := os.CreateTemp("", "feiq-clipboard-*.png")
			if err != nil {
				terminal.PrintfColor(console.ColorRed, "[%s] [图片发送失败] %v", clock(), err)
				continue
			}
			imagePath := file.Name()
			_ = file.Close()
			if err := clipboard.SavePNG(imagePath); err != nil {
				_ = os.Remove(imagePath)
				terminal.PrintfColor(console.ColorRed, "[%s] [图片发送失败] %v", clock(), err)
				continue
			}
			appendHistory(terminal, historyStore, history.Entry{Direction: "out", PeerIP: command.target, Kind: "file", Content: "剪贴板图片"})
			terminal.PrintfColor(console.ColorMagenta, "[%s] [我 -> %s] 图片: 剪贴板图片（等待对方接收）", clock(), command.target)
			sendWG.Add(1)
			go func(target, path string) {
				defer sendWG.Done()
				defer os.Remove(path)
				sendCtx, stop := context.WithTimeout(ctx, *transferWait)
				defer stop()
				if err := session.SendPath(sendCtx, target, path); err != nil {
					terminal.PrintfColor(console.ColorRed, "[%s] [图片发送失败 -> %s] %v", clock(), target, err)
					return
				}
				terminal.PrintfColor(console.ColorGreen, "[%s] [图片发送完成 -> %s]", clock(), target)
			}(command.target, imagePath)
		case "file", "dir":
			if err := validateInteractivePath(command.kind, command.payload); err != nil {
				terminal.PrintfColor(console.ColorRed, "[%s] [命令错误] %v", clock(), err)
				continue
			}
			label := "文件"
			if command.kind == "dir" {
				label = "目录"
			}
			terminal.RememberTarget(command.target)
			appendHistory(terminal, historyStore, history.Entry{Direction: "out", PeerIP: command.target, Kind: command.kind, Content: command.payload})
			terminal.PrintfColor(console.ColorMagenta, "[%s] [我 -> %s] %s: %s（等待对方接收）", clock(), command.target, label, command.payload)
			sendWG.Add(1)
			go func(command interactiveCommand, label string) {
				defer sendWG.Done()
				sendCtx, stop := context.WithTimeout(ctx, *transferWait)
				defer stop()
				if err := session.SendPath(sendCtx, command.target, command.payload); err != nil {
					terminal.PrintfColor(console.ColorRed, "[%s] [%s发送失败 -> %s] %v", clock(), label, command.target, err)
					return
				}
				terminal.PrintfColor(console.ColorGreen, "[%s] [%s发送完成 -> %s] %s", clock(), label, command.target, command.payload)
			}(command, label)
		}
	}
}

func parseInteractiveCommand(line string) (interactiveCommand, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return interactiveCommand{}, nil
	}
	if line == "/help" {
		return interactiveCommand{kind: "help"}, nil
	}
	if line == "exit" || line == "quit" || line == "/quit" || line == "/exit" {
		return interactiveCommand{kind: "quit"}, nil
	}
	if command, rest := cutInteractiveField(line); command == "/history" {
		target, extra := cutInteractiveField(rest)
		if target == "" || extra != "" {
			return interactiveCommand{}, fmt.Errorf("用法: /history <IP>")
		}
		return interactiveCommand{kind: "history", target: target}, nil
	}
	if command, rest := cutInteractiveField(line); command == "/search" {
		scope, query := cutInteractiveField(rest)
		if scope != "user" {
			return interactiveCommand{}, fmt.Errorf("用法: /search user [关键词]")
		}
		return interactiveCommand{kind: "search-user", payload: query}, nil
	}
	command, rest := cutInteractiveField(line)
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
	if unquoted, err := strconv.Unquote(payload); err == nil {
		payload = unquoted
	}
	if payload == "" {
		return interactiveCommand{}, fmt.Errorf("发送内容不能为空")
	}
	return interactiveCommand{kind: kind, target: target, payload: payload}, nil
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

func interactiveCompletions(line string) []string {
	var commands []string
	for _, command := range interactiveCommands {
		if strings.HasPrefix(command, line) {
			commands = append(commands, command)
		}
	}
	if len(commands) > 0 {
		return commands
	}
	command, rest := cutInteractiveField(line)
	kind, rest := cutInteractiveField(rest)
	target, _ := cutInteractiveField(rest)
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
	return completeLocalPaths(commandPrefix, pathInput, kind == "dir")
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
	terminal.PrintfColor(console.ColorBlue, "交互命令:\n  /send msg   <IP> <消息>\n  /send file  <IP> <文件路径>\n  /send dir   <IP> <目录路径>\n  /send image <IP>\n  /history <IP>\n  /search user [关键词]\n  /help\n  exit（也支持 quit、/exit、/quit）")
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
