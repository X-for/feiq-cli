package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"feiq-cli/ipmsg"
)

type commonFlags struct {
	bind    string
	port    int
	name    string
	host    string
	version string
}

func (c *commonFlags) add(fs *flag.FlagSet, config appConfig) { // 公共标志
	defaultHost, _ := os.Hostname()
	fs.StringVar(&c.bind, "bind", configString(config.Bind, "0.0.0.0"), "local IPv4 address to bind")
	fs.IntVar(&c.port, "port", configInt(config.Port, ipmsg.DefaultPort), "IP Messenger UDP/TCP port")
	fs.StringVar(&c.name, "name", configString(config.Name, defaultHost), "sender/display name")
	fs.StringVar(&c.host, "host", configString(config.Host, defaultHost), "host name placed in packets")
	fs.StringVar(&c.version, "version", configString(config.Version, "1"), "IP Messenger version field")
}

func (c commonFlags) node() *ipmsg.Node {
	return ipmsg.NewNode(ipmsg.Identity{
		Version: c.version,
		Name:    c.name,
		Host:    c.host,
	}, c.bind, c.port)
}

func main() {
	if len(os.Args) < 2 {
		if err := interactive(nil); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	var err error
	switch os.Args[1] {
	case "send-message":
		err = sendMessage(os.Args[2:])
	case "send-file":
		err = sendPath(os.Args[2:], false)
	case "send-dir":
		err = sendPath(os.Args[2:], true)
	case "receive":
		err = receive(os.Args[2:])
	case "version":
		printVersion(os.Stdout)
	case "help", "-h", "--help":
		usage()
	default:
		if strings.HasPrefix(os.Args[1], "-") {
			err = interactive(os.Args[1:])
		} else {
			err = fmt.Errorf("unknown command %q", os.Args[1])
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func sendMessage(args []string) error {
	config, configPath, err := loadAppConfig(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("send-message", flag.ContinueOnError)
	var common commonFlags
	common.add(fs, config)
	addConfigFlag(fs, configPath)
	to := fs.String("to", "", "target IPv4 address (required)")
	text := fs.String("text", "", "message text (required)")
	wait := fs.Duration("wait", configDuration(config.MessageWait, 3*time.Second), "time to wait for RECVMSG acknowledgement")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *to == "" || *text == "" {
		return fmt.Errorf("--to and --text are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *wait)
	defer cancel()
	acked, err := common.node().SendMessage(ctx, *to, *text)
	if err != nil {
		return err
	}
	if acked {
		fmt.Println("message acknowledged by", *to)
	} else {
		fmt.Println("message sent; no acknowledgement received before timeout")
	}
	return nil
}

func sendPath(args []string, wantDir bool) error {
	command := "send-file"
	if wantDir {
		command = "send-dir"
	}
	config, configPath, err := loadAppConfig(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	var common commonFlags
	common.add(fs, config)
	addConfigFlag(fs, configPath)
	to := fs.String("to", "", "target IPv4 address (required)")
	path := fs.String("path", "", "file or directory path (required)")
	wait := fs.Duration("wait", configDuration(config.TransferWait, 5*time.Minute), "maximum time to wait for the target to download")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *to == "" || *path == "" {
		return fmt.Errorf("--to and --path are required")
	}
	info, err := os.Stat(*path)
	if err != nil {
		return err
	}
	if wantDir != info.IsDir() {
		if wantDir {
			return fmt.Errorf("%s is not a directory", *path)
		}
		return fmt.Errorf("%s is not a regular file", *path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *wait)
	defer cancel()
	fmt.Printf("offering %s to %s; waiting for the receiver to download it\n", filepath.Base(*path), *to)
	if err := common.node().SendPath(ctx, *to, *path); err != nil {
		return err
	}
	fmt.Println("transfer completed")
	return nil
}

func receive(args []string) error {
	config, configPath, err := loadAppConfig(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("receive", flag.ContinueOnError)
	var common commonFlags
	common.add(fs, config)
	addConfigFlag(fs, configPath)
	output := fs.String("output", configString(config.Output, "./downloads"), "directory for automatically received files/directories")
	timeout := fs.Duration("timeout", 0, "optional receiver lifetime; 0 waits until Ctrl-C")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	fmt.Printf("listening on %s:%d; saving attachments to %s\n", common.bind, common.port, *output)
	return common.node().Receive(ctx, *output, func(event ipmsg.ReceiveEvent) {
		fmt.Printf("from %s (%s)", event.User, event.From)
		if event.Text != "" {
			fmt.Printf(": %s", event.Text)
		}
		fmt.Println()
		for _, path := range event.SavedPaths {
			if path != "" {
				fmt.Println("saved:", path)
			}
		}
	})
}

func usage() {
	fmt.Print(`feiq-cli - standalone IP Messenger/FeiQ command line tool

Usage:
	feiq-cli                                             interactive mode
  feiq-cli send-message --to IP --text TEXT [options]
  feiq-cli send-file    --to IP --path FILE [options]
  feiq-cli send-dir     --to IP --path DIRECTORY [options]
  feiq-cli receive      [--output DIRECTORY] [options]
  feiq-cli version

All commands accept --bind, --port, --name, --host and --version.
File and directory offers must keep running until the receiver accepts them.
`)
}
