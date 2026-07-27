# feiq-cli

`feiq-cli` 是一个使用 Go 实现的 IP Messenger/飞秋兼容命令行工具，可通过指定 IP 完成文本消息、普通文件和递归目录的发送与接收。

- UDP 2425：文本消息与附件通知
- TCP 2425：文件和目录数据传输
- 字符编码：优先调用系统 `iconv` 使用 GBK；不可用时回退到 UTF-8

## 环境要求

- Go 1.25 或更高版本
- macOS 或 Linux
- 本机 UDP/TCP 2425 端口未被其他飞秋、IP Messenger 或 `feiq-cli` 进程占用
- 建议安装 `iconv`，以确保中文名称、消息和文件名兼容传统飞秋客户端

检查 Go 环境：

```bash
go version
```

## 拉取项目

```bash
git clone git@github.com:X-for/feiq-cli.git
cd feiq-cli
```

如果没有配置 GitHub SSH 密钥，也可以使用 HTTPS：

```bash
git clone https://github.com/X-for/feiq-cli.git
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

## 配置文件

程序启动时默认尝试读取：

```text
~/.feiq-cli/config.json
```

默认配置文件不存在时不会报错，也不会自动创建，程序会继续使用内置默认值。仓库根目录的 `config.example.json` 只是模板，不会被自动加载。可以复制模板后再修改：

```bash
mkdir -p ~/.feiq-cli
cp config.example.json ~/.feiq-cli/config.json
```

配置文件可以只填写需要修改的字段。例如只修改接收文件的保存目录：

```json
{
  "output": "~/Downloads/飞秋接收"
}
```

完整配置示例：

```json
{
  "bind": "192.168.110.25",
  "port": 2425,
  "output": "~/Downloads/飞秋接收",
  "history_file": "~/.feiq-cli/history.jsonl",
  "name": "CLI 客户端",
  "host": "feiq-cli",
  "version": "1",
  "color": "auto",
  "message_wait": "5s",
  "transfer_wait": "30m"
}
```

支持的字段：

| 字段 | 内置默认值 | 适用范围 |
| --- | --- | --- |
| `bind` | `0.0.0.0` | 所有运行模式的本地 IPv4 监听地址 |
| `port` | `2425` | 所有运行模式的 IP Messenger UDP/TCP 端口，范围 `1`–`65535` |
| `name` | 当前系统主机名 | 所有运行模式中向对方显示的名称 |
| `host` | 当前系统主机名 | 所有运行模式中写入协议包的主机名 |
| `version` | `1` | 所有运行模式的 IP Messenger 版本字段 |
| `output` | `./downloads` | 交互模式和 `receive` 自动接收文件、目录的保存路径 |
| `history_file` | `~/.feiq-cli/history.jsonl` | 交互模式的本地聊天记录路径 |
| `color` | `auto` | 交互模式颜色，可设为 `auto`、`always` 或 `never` |
| `message_wait` | 交互模式 `5s`；`send-message` 为 `3s` | 消息回执等待时间 |
| `transfer_wait` | `5m` | 交互模式、`send-file` 和 `send-dir` 的附件等待接收时间 |

`message_wait` 和 `transfer_wait` 使用 Go 时间格式且必须大于零，例如 `500ms`、`5s`、`30m` 或 `1h`。

配置中的 `output`、`history_file` 以及 `--config` 路径支持以 `~/` 开头。其他相对路径以启动程序时的当前工作目录为基准。

所有命令均可使用其他配置文件：

```bash
./feiq-cli --config ./config.json
./feiq-cli receive --config ./config.json
./feiq-cli send-message --config ./config.json --to 192.168.110.150 --text "测试"
```

显式指定的配置文件不存在时，程序会报错退出。配置采用严格 JSON 格式，不支持注释、尾随逗号或未定义字段；字段拼写错误也会直接报告。

命令行参数的优先级高于配置文件。例如配置中设置了 `output`，仍可在本次运行中临时覆盖：

```bash
./feiq-cli --output /tmp/received
```

配置字段与命令行参数的对应关系：

```text
bind           --bind
port           --port
name           --name
host           --host
version        --version
output         --output
history_file   --history-file
color          --color
message_wait   --message-wait（交互模式）或 --wait（send-message）
transfer_wait  --transfer-wait（交互模式）或 --wait（send-file/send-dir）
```

## 快速开始

直接启动程序会进入推荐的常驻交互模式：

```bash
./feiq-cli
```

启动后会同时监听 UDP/TCP 2425，在 `feiq>` 输入栏中发送消息、文件或目录，并在输入栏上方实时显示双方事件：

```text
/send msg  192.168.110.150 你好
/send file 192.168.110.150 ./example.txt
/send dir  192.168.110.150 ./example-directory
/send image 192.168.110.150
```

输入 `/` 会立即显示全部交互命令。继续输入命令前缀并按 `Tab`，可以自动补全唯一匹配项；存在多个匹配项时会显示候选命令并补全公共前缀。

交互输入支持以下快捷操作：

- `←` / `→`：移动输入光标，可在消息中间插入或删除中文、Emoji 和普通字符
- `↑`：选择上一个发送目标，自动填入 `/send msg <IP> `
- `↓`：返回较新的发送目标
- `Tab`：补全命令；在 `/send file` 和 `/send dir` 的路径位置补全本地文件或目录

最近发送目标会保存在本地历史中，程序重启后仍可通过上方向键选择。

文件路径包含空格时可以使用双引号：

```text
/send file 192.168.110.150 "/Users/me/My Files/report.pdf"
```

macOS 下可以先复制一张截图或图片，再执行 `/send image <IP>`。程序会将剪贴板图片临时保存为 PNG，并通过普通文件协议发送；临时文件在传输结束后自动删除。对方收到的是普通 PNG 文件，不是飞秋聊天窗口中的原生图片消息。其他系统可以先将图片保存为文件，再使用 `/send file`。

交互消息默认使用不同颜色区分收发、附件、状态和错误。颜色模式可设置为：

```bash
./feiq-cli --color auto
./feiq-cli --color always
./feiq-cli --color never
```

默认 `auto` 仅在交互终端启用颜色，并遵循 `NO_COLOR` 环境变量。

其他交互命令：

```text
/search user
/search user 张三
/history 192.168.110.150
/help
exit
```

程序启动后会通过 IP Messenger 广播自动发现同一局域网中的在线用户，并每分钟刷新一次。`/search user` 会同时显示当前在线用户和聊天记录中的本地联系人；可以使用用户名、主机名或 IP 片段过滤。

`/history <IP>` 显示与指定 IP 最近的 50 条文本、文件和目录收发记录。历史默认保存在：

```text
~/.feiq-cli/history.jsonl
```

可以在启动时修改历史文件位置：

```bash
./feiq-cli --history-file ./data/history.jsonl
```

也支持 `quit`、`/exit` 和 `/quit` 退出。退出时程序会停止监听、取消未完成任务并恢复终端状态。

指定监听地址、显示名称和下载目录：

```bash
./feiq-cli \
  --bind 192.168.110.25 \
  --name "CLI 客户端" \
  --output ./downloads
```

交互模式收到消息时会显示：

```text
[12:30:01] [张三 192.168.110.150] 消息: 你好
[12:30:10] [张三 192.168.110.150] 收到文件: report.pdf -> downloads/report.pdf
```

自己发送的内容和后续状态也会显示在输入栏上方：

```text
[12:31:00] [我 -> 192.168.110.150] 消息: 收到
[12:31:00] [已送达 -> 192.168.110.150] 对方已确认接收
```

> 交互模式会自动接受并下载附件，建议仅在受信任的局域网内使用。

## 独立收发命令

原有的一次性命令仍然保留。以下示例假设目标地址为 `192.168.110.150`。

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

按 `Ctrl+C` 停止接收。也可以在交互模式中使用 `exit` 安全退出。

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
--config PATH      JSON 配置文件
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
- 交互模式在一个进程中共享 UDP/TCP 监听，可以同时收发。
- 独立命令之间不能同时占用相同监听地址和端口。
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
- 常驻交互命令行及输入行实时重绘
- 交互模式中并发发送消息和附件
- IP Messenger 局域网在线用户自动发现
- 本地 JSONL 聊天历史与用户检索
- 彩色事件输出、Unicode 光标编辑和最近目标方向键选择
- 文件与目录路径 Tab 补全
- macOS 剪贴板图片转普通 PNG 文件发送

暂未支持：

- 群聊及多目标群发
- 一次发送多个附件
- 加密、签名和密封消息
- 飞秋原生图片、截图与表情扩展协议（普通图片文件收发不受影响）
- 文件哈希校验和目录断点续传

## 开发验证

执行完整测试：

```bash
go test -race ./...
go vet ./...
```

协议核心位于 `ipmsg` 包，常驻会话位于 `ipmsg/session.go`，交互终端位于 `internal/console`，CLI 入口位于 `cmd/feiq-cli`。可以在此基础上继续开发好友发现和传输任务管理等功能。

## License

本项目采用 [GNU GPL v3.0](./LICENSE) 开源协议发布。

- 你可以使用、复制、修改和分发本项目代码。
- 如果你分发了修改版或基于本项目的衍生作品，通常需要继续以 GPL 兼容方式公开对应源码。
- 协议完整条款以仓库根目录的 [`LICENSE`](./LICENSE) 为准。
