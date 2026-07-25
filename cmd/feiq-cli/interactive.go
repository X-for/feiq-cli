package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"feiq-cli/internal/console"
	"feiq-cli/ipmsg"
)

type interactiveCommand struct {
	kind    string
	target  string
	payload string
}

func interactive(args []string) error {
	fs := flag.NewFlagSet("interactive", flag.ContinueOnError)
	var common commonFlags
	common.add(fs)
	output := fs.String("output", "./downloads", "directory for automatically received files/directories")
	messageWait := fs.Duration("message-wait", 5*time.Second, "time to wait for message acknowledgement")
	transferWait := fs.Duration("transfer-wait", 5*time.Minute, "time to keep each attachment offer active")
	if err := fs.Parse(args); err != nil {
		return err
	}

	terminal, err := console.New(os.Stdin, os.Stdout, "feiq> ")
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var sendWG sync.WaitGroup
	session, err := common.node().StartSession(
		ctx,
		*output,
		func(event ipmsg.ReceiveEvent) { printReceiveEvent(terminal, event) },
		func(err error) { terminal.Printf("[%s] [错误] %v", clock(), err) },
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

	terminal.Printf("[%s] feiq-cli 已启动，监听 %s:%d，附件保存到 %s", clock(), common.bind, common.port, *output)
	terminal.Printf("输入 /help 查看交互命令，输入 exit 退出")
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
			terminal.Printf("[%s] [命令错误] %v", clock(), err)
			continue
		}
		switch command.kind {
		case "":
			continue
		case "help":
			printInteractiveHelp(terminal)
		case "quit":
			return nil
		case "msg":
			terminal.Printf("[%s] [我 -> %s] 消息: %s", clock(), command.target, command.payload)
			sendWG.Add(1)
			go func(command interactiveCommand) {
				defer sendWG.Done()
				sendCtx, stop := context.WithTimeout(ctx, *messageWait)
				defer stop()
				acked, err := session.SendMessage(sendCtx, command.target, command.payload)
				if err != nil {
					terminal.Printf("[%s] [发送失败 -> %s] %v", clock(), command.target, err)
				} else if acked {
					terminal.Printf("[%s] [已送达 -> %s] 对方已确认接收", clock(), command.target)
				} else {
					terminal.Printf("[%s] [未确认 -> %s] 回执等待超时", clock(), command.target)
				}
			}(command)
		case "file", "dir":
			if err := validateInteractivePath(command.kind, command.payload); err != nil {
				terminal.Printf("[%s] [命令错误] %v", clock(), err)
				continue
			}
			label := "文件"
			if command.kind == "dir" {
				label = "目录"
			}
			terminal.Printf("[%s] [我 -> %s] %s: %s（等待对方接收）", clock(), command.target, label, command.payload)
			sendWG.Add(1)
			go func(command interactiveCommand, label string) {
				defer sendWG.Done()
				sendCtx, stop := context.WithTimeout(ctx, *transferWait)
				defer stop()
				if err := session.SendPath(sendCtx, command.target, command.payload); err != nil {
					terminal.Printf("[%s] [%s发送失败 -> %s] %v", clock(), label, command.target, err)
					return
				}
				terminal.Printf("[%s] [%s发送完成 -> %s] %s", clock(), label, command.target, command.payload)
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
	command, rest := cutInteractiveField(line)
	if command != "/send" {
		return interactiveCommand{}, fmt.Errorf("未知命令；输入 /help 查看用法")
	}
	kind, rest := cutInteractiveField(rest)
	if kind != "msg" && kind != "file" && kind != "dir" {
		return interactiveCommand{}, fmt.Errorf("发送类型必须是 msg、file 或 dir")
	}
	target, payload := cutInteractiveField(rest)
	if target == "" {
		return interactiveCommand{}, fmt.Errorf("目标地址不能为空")
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
		terminal.Printf("[%s] [%s %s] 消息: %s", clock(), event.User, event.From, event.Text)
	}
	for index, attachment := range event.Attachments {
		label := "文件"
		if attachment.Attr&0xff == ipmsg.FileDirectory {
			label = "目录"
		}
		if index < len(event.SavedPaths) {
			terminal.Printf("[%s] [%s %s] 收到%s: %s -> %s", clock(), event.User, event.From, label, attachment.Name, event.SavedPaths[index])
		} else {
			terminal.Printf("[%s] [%s %s] 收到%s通知: %s（下载未完成）", clock(), event.User, event.From, label, attachment.Name)
		}
	}
}

func printInteractiveHelp(terminal *console.Terminal) {
	terminal.Printf("交互命令:\n  /send msg  <IP> <消息>\n  /send file <IP> <文件路径>\n  /send dir  <IP> <目录路径>\n  /help\n  exit（也支持 quit、/exit、/quit）")
}

func clock() string {
	return time.Now().Format("15:04:05")
}
