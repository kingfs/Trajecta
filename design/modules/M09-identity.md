# M09 Harness 与 Session

## 0. 头部
| 字段 | 值 |
| --- | --- |
| 模块 ID | M09 |
| 模块名 | Harness 与 Session |
| 归属 | `pkg/harness`、`pkg/session` |
| 状态 | Landed (P2) |
| 依赖模块 | 无（仅标准库） |
| 被依赖模块 | `internal/recorder`（harness）、`internal/router` sticky（session） |
| 目标版本 | 2.0 |
| 最后更新 | 2026-11-09 |

## 1. 目标与非目标
**目标**：从请求头/URL 识别发起请求的客户端 harness，产出稳定 ID、版本、置信度、标签和证据；从请求头和少量 body 字段解析会话信号，生成不暴露原值的 fingerprint 和 `(session, model)` 绑定键。两者都是纯函数，可被 recorder、router、审计和外部程序复用。

**非目标**：harness 不读 body、不改写请求、不做鉴权或安全边界；session 不决定路由、不持久化、不暴露原始 session。两者都不读写 DB/cassette/配置，不访问网络。

## 2. 当前实现与约束
- `pkg/harness`：`catalog.go`（28 个具体产品规则，按 specific→generic 排序）、`detect.go`、`signals.go`；`Detect` 只读 headers 和 URL path/query，绝不读 body。
- `pkg/session`：`session.go`；headers 优先、body 兜底。header 顺序为 `Session_id` → `X-Session-Id` → `Session-Id` → `X-Conversation-Id` → `X-Claude-Code-Session-Id` → `X-DeepSeek-Harness-Session-Id` → `X-Codex-Window-Id`（取 `:` 前的前缀）→ `X-Codex-Turn-Metadata` 的 JSON `session_id`。body 顺序为 `previous_response_id` → `prompt_cache_key` → `metadata.session_id` → `conversation_id` → `session_id`。
- harness 目录含：qwen-code、claude-code、codex-vscode/app/cli、gemini-code-assist/cli、opencode、deepseek-harness、factory-droid、kimi-code、pi、cursor、windsurf、github-copilot、continue、cline、roo-code、kilo-code、aider、crush、goose、amp、openhands、zed、litellm、portkey、openrouter。`KindSDK` 保留但当前没有规则返回。
- 已知限制：harness 不读 body，无法识别只靠 prompt/body 特征的客户端；产品 UA/header 可被用户覆盖，分类不是安全边界。session 会完整 `json.Unmarshal` body（仅当首字节为 `{`），没有大小上限；`Resolve` 失败只返回零值，不返回 error。

## 3. 对外契约（`pkg`）
### 3.1 包结构
```text
pkg/harness/  catalog.go detect.go signals.go
pkg/session/  session.go
```

### 3.2 Go API
```go
package harness

const (
    KindAgentCLI = "agent-cli"; KindIDE = "ide-extension"
    KindAgentApp = "agent-app"; KindGateway = "gateway"
    KindSDK = "sdk" // reserved; no rule returns it
)
const (ConfidenceHigh = "high"; ConfidenceMedium = "medium"; ConfidenceLow = "low")

type Detection struct {
    ID, Name, Version, Kind, Confidence string
    Tags, Evidence []string
}
func (d Detection) Empty() bool
func Detect(req *http.Request) Detection
```

```go
package session

type Kind string // header | previous_response | prompt_cache | conversation | metadata | body
type Confidence string // high | medium | low

type Ref struct {
    Key         string     `json:"-"`
    Fingerprint string     `json:"fingerprint,omitempty"`
    Kind        Kind       `json:"kind,omitempty"`
    Confidence  Confidence `json:"confidence,omitempty"`
    Source      string     `json:"source,omitempty"`
}
func (r Ref) Valid() bool
func Resolve(headers http.Header, body []byte) Ref
func Fingerprint(key string) string
func Compose(key, model string) string
func BindingFingerprint(ref Ref, model string) string
```

`Detection.Tags` 形如 `harness:<id>`、`kind:<kind>`、`sdk:<sdk>`、`capability:<capability>`；`Evidence` 只记录信号名（如 `user-agent:claude-cli`），不记录值。`Ref.Key` 是内部原值，JSON 中为 `-`；`Fingerprint` 为 `sha256:<16 hex>`。

### 3.3 错误与诊断
两个包都没有 error 返回值：无法识别时返回零值 `Detection{}`/`Ref{}`，`Empty()`/`Valid()` 为 false。没有诊断类型；harness 的 `Evidence` 是可观测信号，不是错误。

### 3.4 并发、生命周期、资源
都是无状态纯函数，并发安全；不持有 goroutine、连接或文件，无 `Close`，无超时/取消参数。`Detect`/`Resolve` 不修改入参（`Resolve` 只复制 body 做 trim）。

## 4. `internal` 集成
```text
internal/recorder ──Detect──▶ pkg/harness      // 仅 client-visible exchange
internal/router   ──Resolve─▶ pkg/session      // sticky.ExtractStickyKey
internal/monitor  ──reads───▶ 已持久化的 harness 列/事件（不直接 import）
```

- recorder 在写 cassette 前调用一次 `harness.Detect`，把结果写入 trace meta 和 `harness.detected` prelude event；`exchange_kind=="model"` 的本地子调用不分类。
- router sticky 只用 `session.Resolve(...).Key`，随后用 router 自己的 `composeStickyKey`/`stickyKeyFingerprint`；`session.Compose`/`BindingFingerprint` 当前没有 internal 调用方。
- monitor 只读 store 中的 `client_harness*` 列，不 import `pkg/harness`；`internal/identity`（设计中的组合层）尚不存在。
- 无配置、无 feature flag、无失败降级：识别不到就是空值。

## 5. 数据模型与格式
### 5.1 类型
`Detection` 的零值表示“无可识别客户端”，这是 curl/内部合成请求的正常结果。`Ref` 的零值表示没有会话；`Source` 说明命中的 header/body 字段，便于审计。

### 5.2 序列化
包本身不序列化；`Detection` 由 recorder 映射到 `recordfile.MetaData` 字段和事件，`Ref` 的 JSON tag 供调用方在需要时输出安全形式（`Key` 永远不出现）。`Key` 绝不能被写进 trace/DB/cassette；需要持久化时只用 `Fingerprint`/`BindingFingerprint`。

### 5.3 配置
无配置项；识别规则和 header 顺序是编译进包的固定数据。

### 5.4 数据放置
| 数据 | 文件 / DB / 内存 | 理由 | 丢失后果 |
| --- | --- | --- | --- |
| `Detection` | 内存；recorder 另行写 meta/event | 请求头事实，录制后不可再取 | 丢失筛选标签，可重算 |
| `Ref` | 内存 | 一次请求的会话信号 | 可重新解析 |
| `Fingerprint` | 内存；调用方决定是否落 trace/DB | 安全的分组键 | 丢失分组，可重算 |
| 原始 `Key` | 只在内存 | 隐私 | 不应持久化 |
| cassette / DB | 本包不写 | 纯识别 | 不适用 |

## 6. 算法与不变量
### 6.1 算法
**Detect**：构造只读 `requestView`（path、query、headers）→ 按目录顺序匹配规则 → 置信度取最高（high 立即停止）→ 无产品命中时只返回 SDK/capability 标签 → 标签排序去重。UA 解析为 product/tail/version；`productPrefix` 要求精确 token 或 `-`/`_`/`.` 边界，避免 `continue` 命中 `continuedev`。

**session.Resolve**：先按固定 header 顺序取第一个非空值（Codex window 取前缀，turn metadata 解析 `session_id`），没有 header 才解析 body 的第一个 JSON object；命中后计算 fingerprint。`Fingerprint` 是 SHA-256 前 8 字节的 16 位小写 hex，前缀 `sha256:`；`Compose` 用 `\x1f` 连接 trim 后的 session 和小写 model，空 model 退化为 session；`BindingFingerprint` 对 `Compose` 再取指纹。

### 6.2 不变量
不变量：harness 永不读 body、永不修改请求、证据只含信号名；header 优先级固定（generic `Session_id` 赢过产品 id，保持历史行为）；同一输入产生相同 ID/fingerprint；原始 session 不出现在 fingerprint 中；目录顺序和 lexicographic header 选择保证可重放。

### 6.3 边界条件
边界：`req==nil`/`headers==nil`、空 header 值、body 非 object 或坏 JSON、重复 header、UA 无 `/`（空格分隔形式）、只有 SDK 无产品、用户覆盖 UA/自定义 header、超大 body（session 全量解析）。

## 7. 失败语义
| 场景 | 行为 | 影响上游健康 | 客户端看到 |
| --- | --- | --- | --- |
| 无可识别 harness | 返回空 `Detection`（可能带 SDK/能力标签） | 不适用 | 无影响 |
| UA/header 被覆盖 | 按实际信号分类，可能误判 | 不适用 | 无影响 |
| 无 session 信号 | 返回零值 `Ref` | 不适用 | 不做 sticky/亲和 |
| body 坏 JSON | header 结果仍可用；否则零值 | 不适用 | 无影响 |
| 超大 body | 完整解析，耗时/内存随 body 增长 | 不适用 | 可能变慢 |

两个包都 fail-open：识别失败不阻断请求。

## 8. 性能
harness 是数十次 header 查找和字符串比较，与 body 大小无关。session 的 header 路径是常数次查找；body 路径需要一次 JSON 解析，成本与 body 大小线性。没有 benchmark；recorder 在写路径调用一次，未知 body 大小是主要风险。

## 9. 安全
harness 的证据只存信号名，不把 UA/header 值写进 trace index；但 UA/header 是客户端可伪造的，**不能**作为鉴权/安全边界。session 的原始 `Key` 可能含用户标识，绝不持久化；fingerprint 是单向哈希。两包都不执行外部输入、不发网络、不读文件。`X-Codex-Turn-Metadata` 的值会被解析成 JSON，但只取 `session_id` 字符串。

## 10. 可观测性
无指标/日志。recorder 写 `harness.detected` 事件（含 harness/name/version/kind/confidence/tags/evidence）和 trace meta；monitor 以 `client_harness*` 列过滤/展示。session 的可观测形式是 router 记录的 `sha256:` 指纹，不是原值。

## 11. 测试与验收
### 11.1 单元测试
`GOCACHE=/tmp/trajecta-gocache go test ./pkg/harness ./pkg/session` 通过（Go 1.26.9，分别约 0.018s/0.005s）。harness 覆盖 Claude Code、Codex CLI/VS Code/exec、DeepSeek Harness、Factory Droid、Kimi Code、Pi、Gemini CLI、Qwen Code 优先级、产品 token 边界、空格分隔 UA、header-only 产品、纯 SDK 标签、Anthropic/OpenAI SDK、未识别请求、不修改请求、body 无关、证据只含信号名。session 覆盖 header 优先级、Codex window 前缀、turn metadata、body 兜底顺序、坏 JSON、fingerprint 不泄漏 key、Compose/BindingFingerprint、稳定性。无 fuzz/benchmark。
### 11.2 集成测试
recorder/monitor 有 harness 字段测试；router sticky 有 session 解析测试。
### 11.3 差分测试
harness 从 `internal/harness` 迁到 `pkg/harness` 时是纯移动（commit 中 0 行内容变化）。
### 11.4 验收标准
- [x] 公开 API 有 doc，离线测试通过。
- [x] 证据/指纹不泄漏原始值。
- [x] 不写 cassette/DB，纯内存。
- [ ] harness body 信号（明确非目标，未实现）。
- [ ] `internal/identity` 组合层（不存在）。
- [x] 文档同步（本文）。

## 12. 迁移、兼容、回滚
harness 是 `internal/harness` → `pkg/harness` 的移动，行为不变；session 是新增包。无 DB/格式变化、无 feature flag。回滚：把 import 改回 `internal/harness`（若仍存在）或移除调用；session 由 router sticky 使用，回滚需恢复其原有内联解析（当前已替换为 `session.Resolve`）。

## 13. 开放问题
| 问题 | 影响 | 建议 | 需要谁决定 |
| --- | --- | --- | --- |
| 是否增加 body 信号？ | 识别率 vs 隐私/性能 | 当前明确不读 body；如需要另立设计并声明 lane | 架构 |
| `internal/identity` 是否落地？ | 统一 harness+session+上下文 | M10 组合，不把逻辑塞回 router/recorder | M10 |
| session header 顺序是否改为产品优先？ | 兼容性 | 保持现状，改动会迁移已有会话 | 路由负责人 |
| router 是否复用 `session.Compose`？ | 两套拼接/指纹 | 统一到 pkg/session，删除 router 私有实现 | M10 |

## 14. 实施拆分
1. 已完成：harness 目录/检测、session 解析/指纹、单元测试。
2. 下一步：`internal/identity` 组合二者并供 gateway 使用；router 复用 `session.Compose`/`BindingFingerprint`。
3. 清理：删除 router 重复的 compose/fingerprint 和 recorder 内联分类。

验证 `go test ./pkg/harness ./pkg/session`，接入后跑 `go test ./internal/recorder ./internal/router ./internal/monitor` 与 `task check:quick`。

## 15. 参考
- [Harness 检测](../../pkg/harness/detect.go)、[目录](../../pkg/harness/catalog.go)、[信号](../../pkg/harness/signals.go)
- [Session 解析](../../pkg/session/session.go)
- [Harness 文档](../../docs/HARNESS_DETECTION.md)
- [总体架构](../01-overall-architecture.md) §6.2、§9.3
