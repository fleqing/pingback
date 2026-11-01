# pingback

包装并执行任意命令，命令结束后通过邮件通知结果。适合构建、训练、备份这类跑得久、你不想一直盯着的任务。

- 命令名 `pingback`，短别名 `pb`（构建时可自行建软链或改名）
- 只支持 SMTP（QQ、163、Outlook、Gmail 等），不支持 OAuth，见 [ADR 0001](docs/adr/0001-smtp-only-no-oauth.md)
- 本工具的退出码始终等于被包装命令的退出码，发信失败不会影响它，见 [ADR 0002](docs/adr/0002-exit-code-passthrough.md)
- 平台：Linux
- 密码/授权码只通过环境变量 `PINGBACK_PASSWORD` 提供

## 构建

需要 Go 1.25 或更新版本。

```bash
go build -o pb ./cmd/pb
sudo install -m 755 pb /usr/local/bin/pb
```

纯 Go、无 cgo，也可以在其他机器上交叉编译后拷到 Linux：

```bash
GOOS=linux GOARCH=amd64 go build -o pb ./cmd/pb
```

## 快速开始

1. 写配置文件 `~/.config/pingback/config.toml`（QQ 邮箱示例见下文）
2. 设置环境变量 `PINGBACK_PASSWORD`
3. 发测试邮件验证配置：

```bash
pb test
```

4. 用它包装命令：

```bash
pb -- make build                  # 直接执行，不经过 shell
pb -c "make build && make test"   # 交给 $SHELL -c，支持管道和 &&
```

## 用法

```
pb [选项] [--] 命令 [参数...]
pb test [--profile 名] [--to 地址,...]
```

| 选项 | 说明 |
| --- | --- |
| `-c` | 把命令当作字符串，交给 `$SHELL -c` 执行（没有 `SHELL` 时用 `/bin/sh`） |
| `--on-fail` | 只在命令失败时发邮件 |
| `--min-duration 30s` | 成功且耗时短于该值时不发邮件，失败总是发。格式为 Go 的 duration，如 `30s`、`2m`、`1h30m` |
| `--tail N` | 邮件里保留输出的最后 N 行，默认 50 |
| `--no-output` | 邮件里不带命令输出（终端上仍正常显示） |
| `--to a@x.com,b@y.com` | 覆盖配置里的收件人，逗号分隔 |
| `--profile 名` | 选择邮箱配置；只有一个 profile 时自动选用 |

选项必须写在命令之前，命令之后的内容全部属于被包装的命令。

`pb test` 会发一封测试邮件。与普通运行不同，发信失败时它退出码为 1 并把原因打到 stderr。`test` 是保留的子命令名，要运行系统的 `test` 命令请写 `pb -- test -f x`。

## 邮件内容

主题：

```
[pb ✓] make build (2m13s) @主机名
[pb ✗ 2] make build (2m13s) @主机名
```

失败时标出退出码，命令超过 60 个字符会被截断。正文是纯文本：命令、退出码、主机、工作目录、开始时间、耗时，空一行后是输出的最后 N 行。

输出会做尽力而为的脱敏（`Bearer <token>`、`password=`/`token:` 一类的值、`AKIA…`/`ASIA…` 开头的 AWS key）。这只是正则，**不保证**覆盖所有密钥；输出里有敏感内容时请用 `--no-output`。

## 信号与退出码

- SIGINT、SIGTERM、SIGHUP 会转发给子进程，等它退出后照常发通知
- 被信号终止时退出码为 128+N（如 SIGTERM 为 143）
- SIGKILL 杀掉 `pb` 本身时无法发通知
- 发信失败（重试仍失败）只在 stderr 打印 `pb: warning: notification not sent: ...`，退出码仍等于命令的退出码

## 配置

配置文件默认在 `~/.config/pingback/config.toml`，可用环境变量 `PINGBACK_CONFIG` 指定其他路径。

```toml
[profiles.名字]
host        = "smtp.example.com"
port        = 465
security    = "tls"            # "tls"（隐式 TLS）、"starttls"（默认）、"none"（仅限本机）
username    = "user@example.com"
from        = "user@example.com"
to          = ["me@example.com"]   # 可选；不写则发给 from 自己
retry_delay = "1s"                 # 可选；重试的基准间隔，每次翻倍，默认 1s
```

可以定义多个 profile。只有一个时自动选用；有多个时必须用 `--profile` 指定，否则不发邮件并在 stderr 列出可选名字。

发信失败时最多重试 3 次（共 4 次尝试），间隔指数退避。服务器返回 5xx（如认证失败）属于永久错误，不重试。

### 密码

密码不写进配置文件，只从环境变量 `PINGBACK_PASSWORD` 读取：

```bash
export PINGBACK_PASSWORD='你的密码或授权码'
```

为避免密码明文留在 `~/.bashrc`，可以放在一个仅自己可读的文件里，用时再加载：

```bash
mkdir -p ~/.config/pingback
install -m 600 /dev/null ~/.config/pingback/env
echo "export PINGBACK_PASSWORD='你的密码或授权码'" >> ~/.config/pingback/env
source ~/.config/pingback/env
```

没有设置时不带密码，服务器要求认证则发信失败并告警。

## QQ 邮箱配置示例

### 1. 获取授权码

登录网页版 QQ 邮箱，进入「设置 → 账号 → POP3/IMAP/SMTP/Exchange/CardDAV 服务」，开启 SMTP 服务，按提示（通常需要短信验证）生成**授权码**。后面填的是授权码，不是 QQ 密码。

### 2. 写配置文件

`~/.config/pingback/config.toml`：

```toml
[profiles.qq]
host     = "smtp.qq.com"
port     = 465
security = "tls"
username = "123456789@qq.com"
from     = "123456789@qq.com"
# to     = ["other@example.com"]   # 不写则发给自己
```

`from` 必须与 `username` 一致，QQ 通常会拒绝与登录账号不同的发件人。

### 3. 提供授权码

```bash
install -m 600 /dev/null ~/.config/pingback/env
echo "export PINGBACK_PASSWORD='你的授权码'" >> ~/.config/pingback/env
source ~/.config/pingback/env
```

### 4. 验证并使用

```bash
pb test
pb -c "sleep 2; echo done"
pb --on-fail --min-duration 1m -c "make build && make test"
```

## 已知限制

- 命令输出通过管道透传，**不是 PTY**：交互式程序、带颜色的输出（程序检测到非 TTY 会关闭颜色）、进度条可能表现不同
- 密码放在环境变量里，同一用户或 root 可通过 `/proc/<pid>/environ` 读到，不要在共享服务器上长期放置
- `security = "starttls"` 分支尚无自动测试；隐式 TLS（`tls`）有测试覆盖
- 暂无 `pb config` 交互式向导，配置文件需要手写
- 不支持 OAuth、Windows、macOS、邮件以外的通知渠道、本地补发队列

## 开发

```bash
go test ./...
```

领域术语见 [CONTEXT.md](CONTEXT.md)，设计决策见 [docs/adr](docs/adr)。
