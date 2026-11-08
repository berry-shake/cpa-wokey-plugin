# CPA Wokey Plugin

一个 CLIProxyAPI（CPA）插件：**把 [Wokey](https://wokey.ai) 的 API Key 作为 CPA 认证文件接入，原生转发 OpenAI Chat Completions、OpenAI Responses 和 Anthropic Messages 三种协议。**

- 每个 Wokey API Key 是一个认证文件（`{"type":"wokey","api_key":"..."}`），和其他凭证一样参与 CPA 的选号、冷却和故障转移；
- 自动拉取 Wokey 公开模型目录并注册到 CPA，前缀走 CPA 通用的 `prefix` 机制，默认 `wokey/<模型 ID>`；每个 Key 可以只注册选中的模型；
- 请求按客户端格式原样转发到 Wokey 对应的接口，不做协议转换；
- 余额、Key 用量和 Key 限额显示在 CPA 的额度面板里；
- 每个 Key 可以单独配置出口代理（CPA 通用认证文件字段 `proxy_url`）；
- 自带管理页：添加、编辑、启停、删除 Key，选择上游模型，查看余额。

## 环境要求

- CLIProxyAPI v8（已在 v8.0.58 / v8.0.60 上验证），且开启了动态插件支持
- Linux amd64/arm64、macOS amd64/arm64 或 Windows amd64
- Unix 平台上需要启用 cgo 编译的 CPA

## 安装

### 通过插件商店

在 CPA 配置里加上 berry-shake 的商店源：

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/berry-shake/CLIProxyAPI-Plugins-Store/main/registry.json
```

然后在管理面板的插件商店里找到 **Wokey**（ID `wokey`）安装，也可以直接调用管理接口：

```bash
curl -X POST -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"version":"0.1.0"}' \
  http://127.0.0.1:8317/v0/management/plugin-store/wokey/install
```

### 手动安装

1. 从 Release 下载对应平台的 zip，解压出 `wokey.<so|dylib|dll>`。
2. 放进对应平台的插件目录，例如：

   ```text
   plugins/linux/amd64/wokey.so
   plugins/linux/arm64/wokey.so
   plugins/darwin/amd64/wokey.dylib
   plugins/darwin/arm64/wokey.dylib
   plugins/windows/amd64/wokey.dll
   ```

3. 在 `config.yaml` 里启用：

   ```yaml
   plugins:
     enabled: true
     configs:
       wokey:
         enabled: true
   ```

> 如果 `config.yaml` 里已经有 `store.version`，CPA 只会加载文件名版本和它一致的插件文件。手动换文件时记得同时改 `store.version` 和 `release-tag`。

## 插件配置

所有配置项都是可选的，写在 `plugins.configs.wokey` 下面：

```yaml
plugins:
  configs:
    wokey:
      enabled: true
      base-url: https://api.wokey.ai      # Wokey API 地址，末尾的 /v1 会自动去掉
      model-prefix: wokey                 # 认证文件没写 prefix 时使用的默认前缀；写 "" 表示不加前缀
      models-refresh-minutes: 60          # 模型目录轮询间隔（分钟），0 = 不轮询，只按需加载
      request-timeout-seconds: 600        # 非流式请求超时
      stream-idle-timeout-seconds: 300    # 流式响应多久没有新数据就中断
```

非法的值会被忽略并回退到默认值，同时写一条警告日志，不会让插件注册失败。

## 认证文件

放在 CPA 认证目录里的 JSON 文件，`type` 为 `wokey` 的文件由本插件接管。通过管理页添加的 Key 会保存为 `wokey-<Key 指纹>.json`，文件名不包含 Key 本身。

```json
{
  "type": "wokey",
  "api_key": "sk-wk-...",
  "label": "主账号",
  "models": ["claude-opus-5-5", "gpt-6-sol"],
  "prefix": "wokey",
  "proxy_url": "socks5://127.0.0.1:1080",
  "priority": 0,
  "note": "",
  "disabled": false
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `type` | 是 | 固定为 `wokey` |
| `api_key` | 是 | Wokey API Key（也接受 `api-key`） |
| `label` | 否 | 显示名称；为空时显示打码后的 Key |
| `models` | 否 | 要注册的上游模型 ID 列表。**不写或为 `null` 表示注册目录里的全部模型**；`[]` 表示一个都不注册 |
| `prefix` | 否 | CPA 通用的模型前缀字段。不写时使用插件配置的 `model-prefix`；`""` 表示这个 Key 不加前缀 |
| `proxy_url` | 否 | 出口代理，见下文 |
| `base_url` | 否 | 覆盖插件配置里的 `base-url`，一般不需要 |
| `priority` | 否 | CPA 通用的凭证优先级 |
| `note` | 否 | 备注 |
| `disabled` | 否 | CPA 通用的停用开关 |
| `model_revision` | — | 插件自己维护的目录版本标记，不要手动修改 |

### 模型前缀

前缀使用 CPA 通用的 `prefix` 字段，由 CPA 自己加到注册的模型名上，转发前再去掉。前缀为 `wokey` 时，客户端请求 `wokey/claude-opus-5-5`，Wokey 收到的是 `claude-opus-5-5`。

- 认证文件没写 `prefix` 时，插件把配置里的 `model-prefix`（默认 `wokey`）作为这个 Key 的前缀交给 CPA，不会写进文件；改了 `model-prefix` 后插件会重新保存所有 Wokey 认证文件，让 CPA 按新前缀重新注册。
- 写了 `prefix` 就用文件里的值，`""` 表示不加前缀。在管理页或 CPA 面板里修改认证文件的 `prefix`，效果相同。

> CPA 的 `routing.force-model-prefix` 默认是 `false`。这时 CPA 会同时注册带前缀和不带前缀的两个名字（`wokey/claude-opus-5-5` 和 `claude-opus-5-5`），Wokey 的 Key 也会进入同名模型的公共号池，和其他渠道一起轮询。如果只想通过 `wokey/...` 访问 Wokey，在 CPA 配置里打开：
>
> ```yaml
> routing:
>   force-model-prefix: true
> ```
>
> 这是全局开关，打开后其他配置了 `prefix` 的凭证也只会注册带前缀的名字。管理页的"注册到 CPA 的模型名"预览会按这个开关显示。

选中的模型如果暂时不在 Wokey 目录里（比如上游下线了），仍然会注册，管理页会把它标成"目录中不存在"。

### 出口代理

`proxy_url` 是 CPA 认证文件的通用字段，本插件的所有上游请求（对话、模型目录、余额、用量）都按以下顺序选择出口：

1. 认证文件里的 `proxy_url`；
2. CPA 全局配置的 `proxy-url`；
3. 环境变量 `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY`。

支持 `http://`、`https://`、`socks5://`、`socks5h://`，可以带用户名密码。写 `direct` 或 `none` 表示这个 Key 强制直连，不走全局代理。管理页里显示的代理地址会隐藏密码。

## 模型同步

- 插件启动后按 `models-refresh-minutes` 轮询 `GET /v1/models`（带 ETag，没变化时上游返回 304）。
- 目录里的模型增减或改名时，插件会重新保存受影响的认证文件，CPA 随即重新注册这些 Key 的模型；价格变化不会触发重新注册。
- 目录拉取失败时保留上一次的目录，不会把已注册的模型清空。
- 管理页的"重新拉取模型目录"会忽略 ETag 强制刷新。

## 请求转发

| 客户端格式 | Wokey 接口 |
|---|---|
| OpenAI Chat Completions | `POST /v1/chat/completions` |
| OpenAI Responses | `POST /v1/responses` |
| Anthropic Messages | `POST /v1/messages`（`count_tokens` 走 `/v1/messages/count_tokens`） |

- 请求体只改写顶层的 `model` 字段，其余字节原样转发；
- Chat Completions 流式请求会自动加上 `stream_options.include_usage`，让 CPA 能统计用量；
- `anthropic-version` / `anthropic-beta` 只转发给 Messages 接口。

上游错误到 CPA 的映射：

| Wokey 返回 | 交给 CPA | 效果 |
|---|---|---|
| 401 / 402 / 账号级 403 | 原状态码 | 这个 Key 进入冷却 |
| 403 `request_blocked` / `model_denylisted` | 400 | 只失败这一次请求，不冷却 Key |
| 404（模型不存在） | 400 | 同上 |
| 429 | 429，保留 `Retry-After` | 按上游给的时间冷却 |
| 529 | 503 | 可重试 |
| 499 | 408 | 可重试 |

## 管理页

在 CPA 管理面板的插件菜单里点 **Wokey** 打开，页面地址是：

```text
/v0/resource/plugins/wokey/manage
```

- 从管理面板里打开时复用面板"记住密码"保存的管理密钥；直接打开时手动输入，只保存在页面内存里。
- 概览：模型目录数量和更新时间、Key 数量；可以刷新、重新拉取模型目录、添加 Key。
- Key 列表：状态、注册的模型数、前缀、出口代理、余额和 Key 用量；每行可以编辑、启用/停用、删除（删除需要 4 秒内再点一次确认）。
- 编辑：名称、出口代理、模型前缀、优先级，以及"全部模型 / 只注册选中的模型"，选模型时可以搜索、按价格和上下文长度对比。
- 添加 Key 时默认先调用 Wokey 校验 Key 是否有效，Key 的名称默认用 Wokey 那边的 label。
- 支持 English / 中文。

启用、停用和删除走 CPA 原生的认证文件接口（`PATCH /v0/management/auth-files/status`、`DELETE /v0/management/auth-files?name=...`），和在 CPA 面板里操作效果相同。

## 管理接口

```text
GET   /v0/resource/plugins/wokey/manage             管理页外壳，无需鉴权，不含数据
GET   /v0/resource/plugins/wokey/logo.svg           插件 logo，无需鉴权
GET   /v0/management/plugins/wokey/status           插件状态、目录摘要、Key 列表
GET   /v0/management/plugins/wokey/models           模型目录（价格为每百万 token 美元）
POST  /v0/management/plugins/wokey/models/refresh   强制重新拉取模型目录
POST  /v0/management/plugins/wokey/credentials      添加 Key
PATCH /v0/management/plugins/wokey/credentials      编辑 Key
GET   /v0/management/plugins/wokey/credentials/quota?auth_index=...   余额和 Key 用量
```

`/v0/management` 路由由 CPA 宿主先校验管理密钥，通过后才转发给插件。

```bash
# 添加 Key，只注册两个模型，不加前缀
curl -X POST -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"api_key":"sk-wk-...","models":["claude-opus-5-5","gpt-6-sol"],"prefix":""}' \
  http://127.0.0.1:8317/v0/management/plugins/wokey/credentials

# 改成注册全部模型、使用默认前缀、走指定代理
curl -X PATCH -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"auth_index":"<AUTH_INDEX>","models":null,"prefix":null,"proxy_url":"socks5://127.0.0.1:1080"}' \
  http://127.0.0.1:8317/v0/management/plugins/wokey/credentials
```

编辑接口只修改请求里出现的字段：`null` 表示恢复默认（`models` 恢复为全部模型，`prefix` 恢复为插件默认前缀，`priority` 清除），空字符串表示清除（`label`、`note`、`proxy_url`、`base_url`）。`api_key` 不能修改，换 Key 请新增一个再删掉旧的。

## 安全说明

- 读取和修改 Key 的接口都需要 CPA 管理密钥；公开的只有不含数据的页面外壳和 logo。
- 管理页设置了 `frame-ancestors 'self'`，其他站点不能嵌入。
- 接口和页面只显示打码后的 Key 和隐藏了密码的代理地址。

## 本地构建

```bash
go test ./...
CGO_ENABLED=1 go build -buildmode=c-shared \
  -ldflags '-X main.pluginVersion=0.1.0-dev' \
  -o wokey.dylib .
```

Linux 用 `.so`，Windows 用 `.dll`。c-shared 构建会顺带生成一个 C 头文件，宿主用不到它。

推送 `v*` tag 后，GitHub Actions 会先跑 gofmt / vet / test，再构建五个平台的产物，按插件商店规范打包成 `wokey_<version>_<goos>_<goarch>.zip` 并连同 `checksums.txt` 发布 Release。
