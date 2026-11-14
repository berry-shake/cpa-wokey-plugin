# CPA Wokey Plugin

CLIProxyAPI（CPA）插件：把 [Wokey](https://wokey.ai) 的 API Key 作为 CPA 认证文件接入，转发 OpenAI Chat Completions、OpenAI Responses 和 Anthropic Messages。

- 每个 Key 是一个认证文件，和其他凭证一样参与 CPA 的选号、冷却和故障转移，可以单独设出口代理
- 自动拉取 Wokey 模型目录注册到 CPA，默认模型名 `wokey/<模型 ID>`，也可以只注册选中的模型
- 请求原样转发，不做协议转换
- 查看账户余额、Key 的消费和限额，CPA 额度面板也能看；配了发卡令牌还能看 Provider 收益
- 自带管理页：Key、模型、出口代理、请求日志

## 安装

需要 CLIProxyAPI v8 并开启动态插件（Unix 上 CPA 要用 cgo 编译）；支持 Linux / macOS（amd64、arm64）和 Windows amd64。

**插件商店**：在 CPA 配置里加上商店源，然后在管理面板的插件商店里安装 **Wokey**。

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/berry-shake/CLIProxyAPI-Plugins-Store/main/registry.json
```

**手动安装**：从 [Release](https://github.com/berry-shake/cpa-wokey-plugin/releases) 下载对应平台的 zip，把里面的 `wokey.so`（macOS 是 `.dylib`，Windows 是 `.dll`）放到 `plugins/<系统>/<架构>/` 下，再在配置里打开 `plugins.configs.wokey.enabled`。配置里已有 `store.version` 时，CPA 只加载版本号对得上的文件，手动换文件要同时改 `store.version` 和 `release-tag`。

## 配置

都是可选项，写在 `plugins.configs.wokey` 下；填错的值回退默认并记一条警告。

```yaml
plugins:
  configs:
    wokey:
      enabled: true
      base-url: https://api.wokey.ai        # 末尾的 /v1 会自动去掉
      model-prefix: wokey                   # 认证文件没写 prefix 时的默认前缀，"" = 不加
      models-refresh-minutes: 60            # 模型目录轮询间隔，0 = 不轮询
      request-timeout-seconds: 600          # 非流式请求超时
      stream-idle-timeout-seconds: 300      # 流式响应多久没数据就断开
      data-dir: plugins/wokey-data          # 请求日志和代理列表的目录
      log-retention: 100000                 # 请求日志最多保留条数，0 = 不记录
      log-retention-days: 30                # 请求日志保留天数，0 = 只按条数清理
```

## 认证文件

`type` 为 `wokey` 的认证文件归本插件管理；管理页添加的 Key 保存为 `wokey-<指纹>.json`。

```json
{
  "type": "wokey",
  "api_key": "sk-wk-...",
  "label": "主账号",
  "models": ["claude-opus-5-5", "gpt-6-sol"],
  "prefix": "wokey",
  "proxy_url": "socks5://127.0.0.1:1080",
  "issuer_token": "wkt_..."
}
```

| 字段 | 说明 |
|---|---|
| `api_key` | 必填 |
| `label` | 显示名称，默认用文件名 |
| `models` | 要注册的模型。不写或 `null` = 目录里全部模型，`[]` = 不注册 |
| `prefix` | 模型名前缀。不写用配置里的 `model-prefix`，`""` = 不加前缀 |
| `proxy_url` | 出口代理，支持 `http(s)://`、`socks5(h)://`，`direct` = 强制直连；不写时依次用 CPA 的 `proxy-url`、环境变量 |
| `issuer_token` | Wokey 发卡令牌（`wkt_` 开头），用来查 Provider 收益，见[额度](#额度) |
| `base_url` | 覆盖配置里的 `base-url` |
| `priority` / `note` / `disabled` | CPA 通用字段 |

`model_revision` 由插件维护，不要手改。

> CPA 的 `routing.force-model-prefix` 默认关闭，这时模型会同时注册成 `wokey/claude-opus-5-5` 和 `claude-opus-5-5`，后者会和其他渠道的同名模型混在一起轮询。只想用带前缀的名字就把它打开（全局生效）。

## 工作方式

**模型**：每隔 `models-refresh-minutes` 拉一次 `/v1/models`（带 ETag，没变化不重复下载）；模型增减或改名才重新注册，价格变化不会；拉取失败保留上一次的目录。

**转发**：

| 客户端格式 | Wokey 接口 |
|---|---|
| OpenAI Chat Completions | `POST /v1/chat/completions` |
| OpenAI Responses | `POST /v1/responses` |
| Anthropic Messages | `POST /v1/messages`、`/v1/messages/count_tokens` |

- 只改写请求体里的 `model`，其余原样转发；Chat 流式请求会自动加上 `stream_options.include_usage`
- 流式响应在第一帧真正的输出之前，上游报错（限流、过载）按普通错误处理，CPA 照常冷却或换 Key；流没收到结束帧就断了，记为 `stream_truncated`（502）

**上游错误**：

| Wokey 返回 | 交给 CPA | 效果 |
|---|---|---|
| 401 / 402 / 账号级 403 | 原状态码 | 冷却这个 Key |
| 403 `request_blocked` / `model_denylisted`、404 | 400 | 只失败这一次 |
| 429 | 429，带 `Retry-After` | 按上游给的时间冷却 |
| 529 / 499 | 503 / 408 | 可重试 |

## 额度

- **管理页**：每个 Key 显示账户余额、近 24 小时 / 7 天 / 30 天消费和 Key 限额。同一账户的多个 Key 汇总时只算一次。
- **CPA 额度面板**：每个 Key 一条 `monthly` 进度条，剩余比例 = 余额 ÷（余额 + 这个 Key 本月消费）。月内充值过或多个 Key 共用余额时只是近似值。
- **发卡令牌**：`issuer_token` 填 Wokey「礼品卡与直充」页创建的令牌后，管理页多查一次 `GET /api/issuer/v1/balance`，显示可提现的 Provider 收益和返佣（不算进余额）。这个接口会顺带结算冷却期已满的作废礼品卡，除此之外只查询。令牌设了 IP 白名单的话，要把这个 Key 的出口 IP 加进去。

查询按出口排队：同一个出口一次只查一个 Key，不同出口同时查。

## 管理页

在 CPA 管理面板的插件菜单里点 **Wokey**（`/v0/resource/plugins/wokey/manage`）。

- **凭据**：Key 卡片，添加、编辑、启停、删除，选模型，看余额、消费和收益
- **代理**：常用出口代理列表，编辑 Key 时直接选；可以检测出口 IP、地区和延迟
- **模型目录**：模型的提供方、上下文、价格，以及在 CPA 里注册的名字
- **请求日志**：每次调用的模型、状态、首字时间、耗时、Token 和速度，可以用 CPA 日志里的 8 位请求 ID 搜索；只记元数据，不记内容
- **配置概览**：当前生效的配置

请求日志里有两种速度，统计条取成功请求的中位数：

- **生成速度** =（输出 Token − 1）÷（总耗时 − 首字时间），只算流式请求，即 vLLM 的 1 ÷ TPOT
- **端到端速度** = 输出 Token ÷ 总耗时，包含等首字的时间，即 Artificial Analysis 的 Output Speed

数据都在 `data-dir` 下：`wokey.db`（请求日志，SQLite）和 `proxies.json`（代理列表，含代理密码，权限 0600）。

## 管理接口

都在 `/v0/management/plugins/wokey/` 下，CPA 先校验管理密钥再转给插件。

| 接口 | 用途 |
|---|---|
| `GET status` | 插件状态和 Key 列表 |
| `GET models`、`POST models/refresh` | 模型目录、强制重新拉取 |
| `POST credentials`、`PATCH credentials` | 添加、编辑 Key |
| `GET credentials/quota?auth_index=` | 余额、消费和收益 |
| `GET logs`、`DELETE logs` | 查询、清空请求日志 |
| `GET` / `POST` / `PATCH` / `DELETE proxies`，`POST proxies/import`、`proxies/order`、`proxies/check` | 代理列表 |

```bash
curl -X POST -H "Authorization: Bearer $MANAGEMENT_KEY" -H "Content-Type: application/json" \
  -d '{"api_key":"sk-wk-...","models":["claude-opus-5-5"],"prefix":""}' \
  http://127.0.0.1:8317/v0/management/plugins/wokey/credentials
```

编辑只改传了的字段：`null` 恢复默认，`""` 清空。`api_key` 不能改，换 Key 就新增一个再删掉旧的。启停和删除用 CPA 原生的认证文件接口。

## 构建

需要 Go 和 Node 20：

```bash
make web    # 构建管理页（web/，React + Vite），嵌进插件
CGO_ENABLED=1 go build -buildmode=c-shared -o wokey.so .
make web-test && go test ./...
```

推送 `v*` tag 后，GitHub Actions 跑测试、构建五个平台并发布 Release。

## License

MIT。国旗图标来自 [Twemoji](https://github.com/jdecked/twemoji)（CC-BY 4.0）。
