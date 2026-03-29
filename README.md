# emby-reverse-proxy-go

一个给 Emby 用的轻量反向代理。它通过路径编码上游协议、域名、端口和目标路径，把普通 HTTP、媒体流和 WebSocket 请求转发到任意 Emby 服务。

这个项目除了适配通用 Emby，还兼容一个二次开发的 Emby 后端。那个后端会在响应头和文本响应体里返回硬编码的上游绝对 URL，这部分没法在后端修，所以代理会在响应阶段把它们改写回代理 URL。

## 适合什么场景

- 你想把多个 Emby 入口统一收口到一个反代域名下
- 你前面已经有 Nginx Proxy Manager 或其他反向代理
- 你只想要一个能跑、好排错、不折腾配置的工具

## 核心能力

- 支持 `/{scheme}/{domain}/{port}/{path}` 格式代理任意上游 Emby
- 改写响应头中的 `Location`、`Content-Location`
- 改写特定文本响应中的绝对 URL
- 自动把代理后的 `Referer`、`Origin` 还原成真实上游 URL
- 清理常见代理请求头：`X-Real-Ip`、`X-Forwarded-*`、`Forwarded`、`Via`
- 清理部分响应头：`Server`、`X-Powered-By`、`X-Frame-Options`、`X-Content-Type-Options`
- 支持媒体流透传、`Range` / `If-Range`、WebSocket

## 路径规则

唯一合法格式：

```text
/{scheme}/{domain}/{port}/{path}
```

规则：

- `scheme` 只能是 `http` 或 `https`
- `domain` 必填
- `port` 必填，范围 `1-65535`
- 即使是 `80` 或 `443` 也不能省略 `port`
- `path` 可为空；为空时实际请求上游 `/`
- 查询参数会原样透传
- 根路径 `/` 会返回 `400 Bad Request`
- 健康检查固定为 `/health`
- 目标地址会做安全限制：拒绝本机、Docker 宿主机名、私网、链路本地地址和未指定地址

示例：

```text
/https/emby.example.com/443/
/http/public-emby.example.net/8096/web/index.html
/http/public-emby.example.net/8096/emby/Items?api_key=xxxx
```

错误示例：

```text
/https/emby.example.com/web/index.html   # 缺少 port
/                                         # 根路径不是首页
```

## 快速开始

### 1. 准备 `docker-compose.yml`

**不需要下载整个仓库。** 对大多数用户来说，只需要单独下载仓库里的 `docker-compose.yml` 文件，放到一个你准备用来部署的目录里就够了。

如果你只是想直接部署现成镜像，`docker-compose.yml` 就是必需品；源码、测试文件和 `Dockerfile` 都不是启动代理服务的前提。

### 2. 启动前先改数据库配置

在第一次执行 `docker compose up -d` 之前，先打开 `docker-compose.yml`，把数据库相关配置改掉，**不要直接使用示例里的默认用户名和密码**。

至少改这几项，并保持两边一致：

```yaml
services:
  app:
    environment:
      DB_MYSQL_USER: '改成你自己的用户名'
      DB_MYSQL_PASSWORD: '改成强密码'
      DB_MYSQL_NAME: '改成你自己的数据库名'

  db:
    environment:
      MYSQL_ROOT_PASSWORD: '改成强密码'
      MYSQL_DATABASE: '改成和上面一致的数据库名'
      MYSQL_USER: '改成和上面一致的用户名'
      MYSQL_PASSWORD: '改成和上面一致的强密码'
```

注意：

- `app` 里的 `DB_MYSQL_*` 要和 `db` 里的 `MYSQL_*` 对应上
- 不要只改一边，不然 Nginx Proxy Manager 连不上数据库
- 这是有状态服务，**上线后不要随手只改环境变量不处理现有数据卷**，否则很容易把现有部署搞坏
- 简单说：**首次部署前改最省事，跑起来以后再改最麻烦**

这样做不是形式主义，而是为了避免把过于弱的默认凭据直接带进长期运行环境。

### 3. 启动

仓库自带的 `docker-compose.yml` 会一起启动：

- `app`：Nginx Proxy Manager
- `db`：MariaDB
- `emby-proxy`：本项目代理服务

启动：

```bash
docker compose up -d
```

默认行为：

- NPM 后台：`http://<宿主机IP>:81`
- 公共入口：`80` / `443`
- `emby-proxy` 只在 compose 内部网络暴露 `:8080`
- 数据目录：`./data`、`./letsencrypt`、`./mysql`

### 4. 在 Nginx Proxy Manager 里配置上游

Proxy Host 推荐配置：

- **Scheme**: `http`
- **Forward Hostname / IP**: `emby-proxy`
- **Forward Port**: `8080`

推荐按钮状态：

- **WebSocket Support**: 开
- **Cache Assets**: 关
- **Block Common Exploits**: 建议先关

`Custom Nginx Configuration` 建议只填这三行：

```nginx
proxy_buffering off;
proxy_request_buffering off;
proxy_max_temp_file_size 0;
```

别乱加这些东西：

- 不要额外手写 `proxy_set_header Connection ...`
- 不要额外手写 `proxy_set_header Upgrade ...`
- 一般也不需要再包一层 `location / { ... }`

如果前置代理没有正确传递 `X-Forwarded-Proto` 和 `X-Forwarded-Host`，响应里改写出来的 URL 会不对。

## 访问示例

假设外部代理域名是 `https://proxy.example.com`：

- Emby HTTPS 首页：`https://proxy.example.com/https/emby.example.com/443/`
- Emby HTTP 首页：`https://proxy.example.com/http/public-emby.example.net/8096/`
- API 请求：`https://proxy.example.com/http/public-emby.example.net/8096/emby/Items?api_key=xxxx`
- Web 页面：`https://proxy.example.com/http/public-emby.example.net/8096/web/index.html`

## 目标地址安全限制

为了减少公网暴露时被滥用成跳板代理的风险，代理现在会拒绝以下目标：

- 主机名：`localhost`、`host.docker.internal`
- IPv4：`127.0.0.0/8`、`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`、`169.254.0.0/16`、`0.0.0.0`
- IPv6：`::1`、`fc00::/7`、`fe80::/10`、`::`

另外，域名不是只看字符串。代理会先做 DNS 解析；只要解析结果里有任意一个 IP 落在上面的范围内，请求就会直接返回 `400 Bad Request`。

这意味着：

- 仍然支持代理公网可达的 Emby
- **不再支持** 代理 `192.168.x.x`、`10.x.x.x`、`172.16-31.x.x` 这类内网 Emby
- 如果你本来就是拿它代理家庭局域网 Emby，这个版本不适合直接升级后无脑继续用

## 改写规则

### 请求侧

会清理这些代理相关请求头：

- `X-Real-Ip`
- `X-Forwarded-For`
- `X-Forwarded-Proto`
- `X-Forwarded-Host`
- `X-Forwarded-Port`
- `Forwarded`
- `Via`

另外：

- 代理后的 `Referer`、`Origin` 会被还原成真实上游 URL
- 发往上游时会把 `Host` 设为目标主机和端口
- 非媒体请求会主动发 `Accept-Encoding: identity`，便于改写文本响应

### 响应侧

会处理这些内容：

- 改写 `Location`
- 改写 `Content-Location`
- 改写特定文本响应中的绝对 URL
- 移除 `Server`
- 移除 `X-Powered-By`
- 移除 `X-Frame-Options`
- 移除 `X-Content-Type-Options`

响应体改写只在下面条件同时满足时发生：

1. `Content-Type` 属于以下之一：
   - `application/json`
   - `text/html`
   - `text/xml`
   - `text/plain`
   - `application/xml`
   - `application/xhtml`
   - `text/javascript`
   - `application/javascript`
2. `Content-Encoding` 为空或为 `identity`

也就是说：

- 压缩响应（如 `gzip`、`br`）不会先解压再改写
- 媒体流默认直接透传
- 这不是通用 HTML/JSON 解析器，只是把上游绝对 URL 换回代理 URL

## 媒体流和 WebSocket

### 媒体流

媒体识别是启发式的，不是完整 Emby 协议语义。通常命中以下特征就会走流式透传：

- 路径包含 `/videos/`
- 路径包含 `/audio/`
- 路径包含 `/images/`
- 路径包含 `/items/images`
- 路径包含 `/stream`
- 或者文件扩展名是常见视频、音频、图片、字体、压缩包、字幕类型

支持：

- `Range`
- `If-Range`

### WebSocket

依赖标准升级头：

- `Connection: Upgrade`
- `Upgrade: websocket`

如果前置代理没把升级头透传对，WebSocket 就起不来。

如果上游拒绝升级，代理会把该 HTTP 响应直接透传给客户端，并记录为 `[PROXY]`，不是 `[WS]`。

## 健康检查

路径：

```text
/health
```

返回：

- 状态码：`200`
- 响应体：`ok`

说明：

- 根路径 `/` 不是健康检查
- `/health` 成功时默认不输出访问分类日志
- 只有写回失败时才记错误日志

## 环境变量

- `LISTEN_ADDR`：默认 `:8080`，服务监听地址

目前就这一个。

## 日志怎么读

### 服务日志

- `[SERVER]`：启动和致命退出

```text
[SERVER] listening on :8080
[SERVER] fatal: listen tcp :8080: bind: address already in use
```

### 访问结果日志

- `[API]`：命中文本响应改写
- `[STREAM]`：媒体流或大文件透传
- `[PROXY]`：普通透传，或 WebSocket 升级被上游拒绝
- `[WS]`：WebSocket 成功升级并完成双向转发

```text
[API] 200 GET emby.example.com/emby/Items | rewritten | 45ms
[STREAM] 206 GET emby.example.com/Videos/1/stream.mp4 | bytes 1.2 GB | 3m22s
[PROXY] 200 GET emby.example.com/web/index.html | bytes 156 KB | 89ms
[PROXY] 426 GET emby.example.com/socket | upgrade rejected | 12ms
[WS] 101 GET emby.example.com/socket | up 12 KB | down 48 KB | 2m10s
```

### 异常日志

- `[WARN]`：预期中的断连，比如客户端关闭、EOF、broken pipe、connection reset
- `[ERROR]`：真正要排查的错误，比如上游不可达、握手失败、读写失败

```text
[WARN] websocket downstream copy emby.example.com/socket failed: broken pipe
[ERROR] GET emby.example.com/Items upstream request failed: connection refused
```

一句话：**`WARN` 多半是用户断开，`ERROR` 才是真的问题。**

## 快速排错

按这个顺序看：

### 1. 服务有没有起来

```bash
docker compose ps
```

至少要看到：

- `nginx-proxy-manager`
- `nginx-proxy-manager-db`
- `emby-proxy`

### 2. 健康检查通不通

```bash
curl -i "http://<你的代理域名或IP>/health"
```

预期：`200 OK`，响应体 `ok`

### 3. 根路径是不是误用了

```bash
curl -i "http://<你的代理域名或IP>/"
```

预期：`400 Bad Request`

### 4. 基础代理路径能不能通

```bash
curl -i "https://proxy.example.com/http/public-emby.example.net/8096/"
```

### 5. 文本接口有没有改写 URL

请求一个 JSON 或 HTML 接口，检查里面的绝对 URL 是否已经变成代理路径。

### 6. 媒体流能不能断点续传

对媒体资源发起带 `Range` 的请求，预期返回 `206 Partial Content`。

### 7. WebSocket 能不能升级

如果需要 WebSocket 的页面打不开，先查前置代理是否真的把升级头透传到了本服务。

## 已知边界

- 这是轻量工具，不提供复杂日志配置，也不输出 JSON 日志
- 媒体识别是启发式的，不保证所有边界路径都完美分类
- 只有符合条件的文本响应才会改写绝对 URL
- 外部 URL 推断依赖 `X-Forwarded-Proto` 和 `X-Forwarded-Host`
- Docker 镜像本身不做 TLS 终止，HTTPS 一般应该交给 NPM 或其他前置反代

## 项目结构

```text
├── main.go              # 入口
├── handler.go           # HTTP 主流程编排与响应处理
├── headers.go           # 请求/响应头规则与辅助函数
├── target.go            # 代理路径解析与 target 语义
├── websocket.go         # WebSocket 透传逻辑
├── rewriter.go          # 响应改写：URL 替换、Header 改写
├── Dockerfile           # 多阶段构建镜像
└── docker-compose.yml   # NPM、数据库和代理服务的一体化 Compose 部署方案
```
