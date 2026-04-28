# emby-reverse-proxy-go

> **警告：不要把这个项目用于禁止反代的服务器，否则后果自负。**

一个给 Emby 用的轻量反向代理。

它把上游地址编码进路径里，通过固定格式代理 Emby 页面、API、媒体流和 WebSocket：

```text
/{scheme}/{domain}/{port}/{path}
```

这个项目是 **Emby 专用**，不是通用网站反代。

看不懂可移步 [小白步骤流程](README-b.md)。

## 适用场景

适合下面这类情况：

- 你想把多个 Emby 入口统一收口到一个域名下
- 你前面已经有 Nginx Proxy Manager、Caddy、Traefik、Nginx 等负责 HTTPS
- 你需要一个尽量简单、可直接部署的 Emby 反代

## 核心行为

当前代码实际提供的能力：

- 代理 `/{scheme}/{domain}/{port}/{path}` 格式的 Emby 上游请求
- 支持 HTTP、HTTPS、媒体流、`Range` / `If-Range`、WebSocket
- 改写响应头里的 `Location`、`Content-Location`
- 还原代理后的 `Referer`、`Origin`
- 清理常见代理请求头：`X-Real-Ip`、`X-Forwarded-*`、`Forwarded`、`Via`
- 移除响应头中的 `Server`、`X-Powered-By`
- 支持通过标准环境变量为访问上游 Emby 的出站连接配置代理：`HTTP_PROXY`、`HTTPS_PROXY`、`ALL_PROXY`、`NO_PROXY`

另外，代理会对 **部分 Emby 文本接口** 做响应体里的绝对 URL 改写，当前主要覆盖：

- `.../Items/.../PlaybackInfo`
- `.../Sessions/Playing/Progress`

这部分是为了兼容某些 Emby 后端返回硬编码绝对 URL 的情况。不要把它理解成“所有页面和 API 响应都会改写”。

## 运行前提

至少满足这几点：

- 你已经有一个可访问的 Emby 上游
- 对外入口由前置反代提供 HTTPS
- 前置反代能正确传递 `X-Forwarded-Proto`、`X-Forwarded-Host`
- 如果要用实时能力，还要正确透传 WebSocket 升级头

本服务默认只监听内部 HTTP：`:8080`。

## 快速开始

### 1. 直接使用仓库自带的 Compose

仓库里的 `docker-compose.yml` 会一起启动：

- `app`：Nginx Proxy Manager
- `db`：MariaDB
- `emby-proxy`：本项目代理服务

第一次启动前，先改掉 `docker-compose.yml` 里的数据库用户名和密码，不要直接用示例值。

启动：

```bash
docker compose up -d
```

默认情况下：

- NPM 后台：`http://<宿主机IP>:81`
- 公共入口：`80` / `443`
- `emby-proxy` 仅在内部网络监听 `:8080`

### 2. 前置代理转发到 `emby-proxy:8080`

如果你用的不是 Nginx Proxy Manager，也一样。核心要求只有这几个：

- 对外提供 HTTPS
- 把请求转发到 `emby-proxy:8080`
- 透传 `X-Forwarded-Proto`、`X-Forwarded-Host`
- 透传 WebSocket 升级头

最小 Nginx 示例：

```nginx
location / {
    proxy_pass http://emby-proxy:8080;
    proxy_set_header Host $http_host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host $http_host;

    proxy_buffering off;
    proxy_request_buffering off;
    proxy_max_temp_file_size 0;

    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
}
```

如果你的公网入口不是 443，而是 8443 这类自定义端口，建议同时透传：

```nginx
proxy_set_header X-Forwarded-Port $server_port;
```

如果你想在前面再套一层固定前缀，例如 `/custom/`，可以加：

```nginx
proxy_set_header X-Forwarded-Prefix /custom;
```

## 访问规则

唯一合法格式：

```text
/{scheme}/{domain}/{port}/{path}
```

规则：

- `scheme` 只能是 `http` 或 `https`
- `domain` 必填
- `port` 必填，范围 `1-65535`
- 即使是 `80` 或 `443` 也不能省略
- `path` 可为空；为空时实际请求上游 `/`
- 查询参数会原样透传
- 根路径 `/` 会返回 `400 Bad Request`
- 健康检查固定为 `/health`

示例：

```text
/https/emby.example.com/443/
/http/public-emby.example.net/8096/web/index.html
/http/public-emby.example.net/8096/emby/Items?api_key=xxxx
```

## 安全限制

默认情况下，代理会拒绝一些危险目标，以免被当成跳板：

- `localhost`
- `host.docker.internal`
- 私网地址
- 链路本地地址
- 未指定地址

对应环境变量：

- `BLOCK_PRIVATE_TARGETS=true`：默认值，拒绝这类目标
- `BLOCK_PRIVATE_TARGETS=false`：允许访问内网 Emby，但风险更大

如果你的真实上游 Emby 就在家庭或办公内网，才考虑关掉它。

## 上游代理环境变量

如果运行本项目的机器不能直连上游 Emby，可以设置标准代理环境变量：

- `HTTP_PROXY` / `http_proxy`
- `HTTPS_PROXY` / `https_proxy`
- `ALL_PROXY` / `all_proxy`
- `NO_PROXY` / `no_proxy`

这些变量影响的是：

- 本服务访问上游 Emby 时的出站连接
- 包括普通 HTTP/HTTPS 请求和 WebSocket

不影响：

- 用户如何访问你的反代入口
- 前置反代到本服务 `:8080` 的这一跳

推荐优先直接写在 Compose 里。

统一走 SOCKS5：

```yaml
services:
  emby-proxy:
    image: ghcr.io/gsy-allen/emby-proxy-go:v1.3
    environment:
      LISTEN_ADDR: ':8080'
      BLOCK_PRIVATE_TARGETS: 'true'
      ALL_PROXY: 'socks5://username:password@VPS2:1080'
      NO_PROXY: 'emby1.example.com,emby2.example.com'
```

走 HTTP/HTTPS 代理：

```yaml
services:
  emby-proxy:
    image: ghcr.io/gsy-allen/emby-proxy-go:v1.3
    environment:
      LISTEN_ADDR: ':8080'
      BLOCK_PRIVATE_TARGETS: 'true'
      HTTP_PROXY: 'http://127.0.0.1:7890'
      HTTPS_PROXY: 'http://127.0.0.1:7890'
```

临时命令行启动也可以：

```bash
ALL_PROXY=socks5://127.0.0.1:1080 ./emby-proxy
```

```bash
HTTP_PROXY=http://127.0.0.1:7890 HTTPS_PROXY=http://127.0.0.1:7890 ./emby-proxy
```

## 环境变量

- `LISTEN_ADDR`：监听地址，默认 `:8080`
- `BLOCK_PRIVATE_TARGETS`：默认 `true`

## 健康检查

路径：

```text
/health
```

返回：

- 状态码：`200`
- 响应体：`ok`

## 常见访问示例

假设你的外部入口是 `https://proxy.example.com`：

- Emby HTTPS 首页：`https://proxy.example.com/https/emby.example.com/443/`
- Emby HTTP 首页：`https://proxy.example.com/http/public-emby.example.net/8096/`
- API 请求：`https://proxy.example.com/http/public-emby.example.net/8096/emby/Items?api_key=xxxx`
- Web 页面：`https://proxy.example.com/http/public-emby.example.net/8096/web/index.html`

## 快速排错

### 1. 服务是否启动

```bash
docker compose ps
```

### 2. 健康检查是否正常

```bash
curl -i "https://<你的代理域名>/health"
```

预期：`200 OK`，响应体 `ok`

### 3. 是否误访问了根路径

```bash
curl -i "https://<你的代理域名>/"
```

预期：`400 Bad Request`

### 4. 基础代理路径是否可达

```bash
curl -i "https://proxy.example.com/http/public-emby.example.net/8096/"
```

### 5. WebSocket 是否被前置层正确透传

如果实时页面打不开，优先检查前置代理是否传对了升级头。

## 已知边界

- 这是 Emby 专用工具，不承诺兼容任意网站或 HTTP 应用
- 响应体绝对 URL 改写只覆盖少数 Emby 接口，不是全站 HTML/JSON 重写器
- 外部 URL 推断依赖 `X-Forwarded-Proto` 和 `X-Forwarded-Host`
- 本服务本身不做 TLS 终止，公网入口的 HTTPS 应由前置反代负责
