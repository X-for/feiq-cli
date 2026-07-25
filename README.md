# feiq-cli

`feiq-cli` 是一个使用 Go 实现的 IP Messenger/飞秋兼容命令行工具，可通过指定 IP 完成文本消息、普通文件和递归目录的发送与接收。

- UDP 2425：文本消息与附件通知
- TCP 2425：文件和目录数据传输
- 字符编码：优先调用系统 `iconv` 使用 GBK；不可用时回退到 UTF-8

## 环境要求

- Go 1.22 或更高版本
- macOS 或 Linux
- 本机 UDP/TCP 2425 端口未被其他飞秋、IP Messenger 或 `feiq-cli` 进程占用
- 建议安装 `iconv`，以确保中文名称、消息和文件名兼容传统飞秋客户端

检查 Go 环境：

```bash
go version
```

## 拉取项目

将下面的地址替换为你自己的仓库地址：

```bash
git clone <repository-url> feiq-cli
cd feiq-cli
```

## 测试与编译

先运行测试：

```bash
go test ./...
```

编译命令行程序：

```bash
go build -o feiq-cli ./cmd/feiq-cli
```

确认程序可以运行：

```bash
./feiq-cli help
```

也可以将它安装到 Go 的二进制目录：

```bash
go install ./cmd/feiq-cli
```

## 快速开始

假设接收方地址为 `192.168.110.150`。

### 启动接收端

接收端先启动常驻监听：

```bash
./feiq-cli receive --output ./downloads
```

该命令会：

- 监听 UDP 2425
- 在终端输出收到的文本消息
- 自动接受文件和目录附件
- 通过 TCP 连接发送方下载数据
- 将内容保存到当前目录下的 `downloads` 目录

按 `Ctrl+C` 停止接收。

> `receive` 当前会自动下载附件，建议仅在受信任的局域网内使用。

### 发送消息

```bash
./feiq-cli send-message \
  --to 192.168.110.150 \
  --text "来自 feiq-cli 的消息"
```

发送后程序会等待 `IPMSG_RECVMSG` 回执，并报告对方是否确认接收。可调整等待时间：

```bash
./feiq-cli send-message \
  --to 192.168.110.150 \
  --text "测试消息" \
  --wait 8s
```

### 发送文件

```bash
./feiq-cli send-file \
  --to 192.168.110.150 \
  --path ./example.txt
```

发送方会先发送附件通知，然后等待接收方通过 TCP 回连下载。因此命令必须保持运行，直到传输完成或超时。

调整最长等待时间：

```bash
./feiq-cli send-file \
  --to 192.168.110.150 \
  --path ./example.zip \
  --wait 10m
```

### 发送目录

```bash
./feiq-cli send-dir \
  --to 192.168.110.150 \
  --path ./example-directory
```

目录会递归发送，保留内部子目录结构。符号链接和特殊文件不会发送。

### 指定接收目录

相对路径：

```bash
./feiq-cli receive --output ./received-files
```

绝对路径：

```bash
./feiq-cli receive --output /path/to/received-files
```

同名文件或目录不会覆盖，程序会自动追加数字后缀：

```text
example.txt
example-1.txt
example-2.txt
```

## 通用参数

所有子命令均支持：

```text
--bind 0.0.0.0    本地 IPv4 监听地址
--port 2425       IP Messenger UDP/TCP 端口
--name feiq-cli   向对方显示的名称
--host HOST       协议包中的主机名
--version 1       IP Messenger 版本字段
```

指定本地网卡地址和显示名称：

```bash
./feiq-cli receive \
  --bind 192.168.110.25 \
  --name "CLI 接收端" \
  --output ./downloads
```

## 端口与运行限制

- 默认同时使用 UDP 2425 和 TCP 2425。
- 同一台机器不能同时运行多个占用相同监听地址和端口的接收或文件发送进程。
- 如果桌面版飞秋正在监听 2425，请先退出桌面版飞秋。
- 使用自定义端口时，通信双方必须使用相同端口。
- 防火墙必须允许 UDP/TCP 2425 入站和出站通信。

## 当前支持范围

已支持：

- 指定 IPv4 地址发送和接收文本消息
- 消息接收回执
- 普通文件发送和接收
- 嵌套目录递归发送和接收
- GBK 中文兼容
- 同名文件自动重命名
- 接收路径安全检查

暂未支持：

- 自动发现局域网好友和好友列表
- 群聊及多目标群发
- 一次发送多个附件
- 多任务并发传输
- 加密、签名和密封消息
- 图片、截图与表情附件协议
- 文件哈希校验和目录断点续传

## 开发验证

执行完整测试：

```bash
go test -race ./...
go vet ./...
```

协议核心位于 `ipmsg` 包，CLI 入口位于 `cmd/feiq-cli`，可以在此基础上继续开发好友发现、交互式终端和任务管理等功能。
