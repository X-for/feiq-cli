# feiq-cli

使用 Go 实现的 IP Messenger/飞秋兼容命令行工具：

- UDP 2425：消息和附件通知
- TCP 2425：文件和目录数据
- 字符编码：优先通过系统 `iconv` 使用 GBK；没有 `iconv` 时回退 UTF-8

## 构建

```bash
go test ./...
go build -o feiq ./cmd/feiq
```

## 发送消息

```bash
./feiq send-message --to 192.168.110.150 --text "来自命令行的消息"
```

命令会等待 `IPMSG_RECVMSG` 回执，并分别报告“收到回执”或“仅完成 UDP
发送但没有回执”。

## 发送文件

```bash
./feiq send-file --to 192.168.110.150 --path ./example.txt
```

进程必须持续运行，直到对方接受文件并通过 TCP 回连下载。默认最多等待 5
分钟，可用 `--wait 30s` 等参数调整。

## 发送目录

```bash
./feiq send-dir --to 192.168.110.150 --path ./example-directory
```

目录按 IP Messenger 的 `IPMSG_GETDIRFILES` 数据流发送，使用
`IPMSG_FILE_DIR` 和 `IPMSG_FILE_RETPARENT` 表示进入和退出目录。

## 接收消息、文件和目录

```bash
./feiq receive --output ./downloads
```

接收服务会监听 UDP 2425。收到普通消息会输出到终端；收到文件或目录附件后，
会自动通过 TCP 连接发送方并下载到指定目录。已经存在的名字不会覆盖，而会
自动追加数字后缀。

## 常用参数

所有子命令均支持：

```text
--bind 0.0.0.0    本地监听地址
--port 2425       UDP/TCP 端口
--name feiq-cli   显示名称
--host HOST       协议中的主机名
--version 1       IP Messenger 版本字段
```

同一台机器上不能同时运行两个都独占 TCP 2425 的文件发送服务。如果原飞秋
程序正在监听该端口，请先退出它，或者让测试双方同时改用另一个端口。
