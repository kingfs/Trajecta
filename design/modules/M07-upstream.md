# M07 上游目录与认证

## 0. 头部
| 字段 | 值 |
| --- | --- |
| 模块 ID | M07 |
| 模块名 | 上游目录与认证 |
| 归属 | `pkg/upstream` |
| 状态 | Landed (P2) |
| 依赖模块 | `pkg/xir` |
| 被依赖模块 | `internal/monitor`（渠道创建 base URL 回退）；计划由 M10/M11 使用 |
| 目标版本 | 2.0 |
| 最后更新 | 2026-11-09 |

## 1. 目标与非目标
**目标**：内置 provider preset 目录（20 个：anthropic、azure、cerebras、deepseek、fireworks、github、google、groq、minimax、mistral、moonshot、ollama、openai、openrouter、together、vertex、vllm、xai、zai、zhipu），把 preset 与调用方 `Config` 合并成不可变 `Resolved`；提供 endpoint/URL 构造、auth header 应用、协议能力回答和可选模型发现。纯解析/构造，可外部 import。

**非目标**：不持久化渠道、不拥有 credential store、不做加密、不做 OAuth/CLI 登录流程（只预留类型和 `AuthDriver` 接口）；不做健康检查、路由选择、限流或价格；不做模型列表缓存/重试。

## 2. 当前实现与约束
- 文件：`doc.go`、`preset.go`、`presets.go`、`resolve.go`、`url.go`、`auth.go`、`capabilities.go`、`models.go`、`errors.go`；仅 import 标准库和 `pkg/xir`。
- `Presets()`/`LookupPreset()` 返回深拷贝；别名（`gemini`→`google`、`kimi`→`moonshot`、`z.ai`→`zai` 等）与 plan（`zai`/`zhipu` 的 `coding`、`moonshot` 的 `kimi-code`）都是数据。
- 当前唯一生产调用方是 `internal/monitor` 的 `defaultPresetBaseURL`：创建渠道且未填 `base_url` 时按 preset endpoint 回退。`internal/upstream` 仍是运行时上游解析的事实源。
- 已知限制：OAuth/CLI credential 流程未实现；`ApplyAuth` 只应用已有 token/key；`DiscoverModels` 不缓存、不重试；Azure/Vertex 的通用 base URL 需调用方提供（Vertex 默认是 Express API-key endpoint）。

## 3. 对外契约（`pkg`）
### 3.1 包结构
`pkg/upstream/{preset,presets,resolve,url,auth,capabilities,models,errors}.go`；无 `internal` 子包。

### 3.2 Go API
```go
type Kind string // vendor | relay | local
type Preset struct { ID, Name string; Kind Kind; Endpoints map[xir.Protocol]string; Auth AuthSpec; Plans []PlanVariant; Models ModelSource; Headers map[string]string; DocsURL, KeysURL, Notes string }
type PlanVariant struct { ID, Name string; Endpoints map[xir.Protocol]string; ModelFilter string }
func Presets() []Preset
func PresetIDs() []string
func LookupPreset(id string) (Preset, bool)
func (p Preset) Plan(id string) (PlanVariant, bool)
func (p PlanVariant) MatchesModel(model string) bool

type Config struct { PresetID, Plan string; Endpoints map[xir.Protocol]string; Auth AuthSpec; Headers map[string]string; APIVersion, Deployment, Project, Location, ModelResource string; Protocols []xir.Protocol; ModelProtocols map[string][]xir.Protocol; Credential *Credential }
type Resolved struct { Preset Preset; Plan *PlanVariant; Endpoints map[xir.Protocol]string; Protocols []xir.Protocol; Auth AuthSpec; Headers map[string]string; APIVersion, Deployment, Project, Location, ModelResource string; ModelProtocols map[string][]xir.Protocol; Models ModelSource; Credential Credential }

func Resolve(cfg Config) (Resolved, error)
func (r Resolved) WithCredential(cred Credential) Resolved
func (r Resolved) Endpoint(protocol xir.Protocol) (string, bool)
func (r Resolved) BuildURL(protocol xir.Protocol, clientPath string) (string, error)
func (r Resolved) ApplyAuth(cred Credential, req *http.Request) error
func (r Resolved) ProtocolsForModel(model string) []xir.Protocol
func (r Resolved) Capabilities(model string) xir.Capabilities

func DiscoverModels(ctx context.Context, client *http.Client, resolved Resolved) ([]string, error)

type AuthMode string // api_key | oauth | cli_credential | none
type AuthSpec struct { Mode AuthMode; Header, Scheme string; OAuth *OAuthSpec; Refresh *RefreshSpec; Headers []HeaderTemplate }
type Credential struct { Mode AuthMode; APIKey, AccessToken, RefreshToken string; ExpiresAt time.Time; AccountID, Plan string; Scopes []string }
type AuthDriver interface { ID() string; Begin(context.Context, AuthRequest) (Flow, error); Exchange(context.Context, AuthRequest, string) (Credential, error); Refresh(context.Context, Credential) (Credential, error); Apply(context.Context, Credential, *http.Request) error; Status(Credential) CredentialStatus }

type HeaderTemplate struct { Name, Value string; IfAbsent bool }
type OAuthSpec struct { AuthorizeURL, TokenURL, DeviceCodeURL, ClientID string; Scopes []string; RedirectURI string; PKCE bool; ExtraParams map[string]string }
type RefreshSpec struct { TokenURL, ClientID string; Scopes []string; ExtraParams map[string]string }
type CredentialStatus string // unknown | valid | expired | revoked
type AuthRequest struct { PresetID string; Scopes []string; RedirectURI, State string; ExtraParams map[string]string }
type Flow struct { URL, DeviceCode, UserCode, VerificationURL, State, CodeVerifier string; ExpiresIn time.Duration }
type ModelSource struct { ListPath string; Static []string; NoList bool; Protocol xir.Protocol }
type StatusError struct { StatusCode int; Status, Body string }
```

`OAuthSpec`、`RefreshSpec`、`AuthRequest`、`Flow`、`CredentialStatus` 只是预留配置/接口；本包没有 OAuth 网络流程或 CLI 凭据读取实现。

### 3.3 错误与诊断
所有错误都是 sentinel-wrapped；用 `errors.Is` 分类。`DiscoverModels` 的非 2xx 返回 `*StatusError`，它实现 `Is` 以匹配 `ErrModelListUnauthorized`（401/403）、`ErrModelListNotFound`（404）、`ErrModelListUnavailable`（429/5xx）。其余 sentinel：`ErrUnknownPreset`/`ErrUnknownPlan`、`ErrNoEndpoints`/`ErrNoEndpoint`/`ErrUnsupportedProtocol`、`ErrInvalidEndpoint`、`ErrInvalidAuthMode`/`ErrMissingCredential`、`ErrNoModelList`。

### 3.4 并发、生命周期、资源
`Preset`/`Resolved`/`Credential` 都是值；目录是只读包级数据，查询返回深拷贝。`BuildURL`/`ApplyAuth`/`Capabilities` 不修改共享状态，可并发调用。`DiscoverModels` 使用调用方 `*http.Client`（nil 用 `http.DefaultClient`），不持有连接；取消通过 `ctx` 传播。

## 4. `internal` 集成
```text
internal/monitor ──LookupPreset──▶ pkg/upstream   // 渠道创建 base URL 回退
internal/upstream ──(独立)────────▶ 运行时解析（未改为调用 pkg/upstream）
internal/channel / internal/gateway ──计划接入
```

只有 `defaultPresetBaseURL` 使用本包；preset 回退在 `base_url` 为空时生效，显式 URL 优先。没有 feature flag、没有配置读取、没有失败降级：未知 preset 返回空串，调用方保持原行为（要求手填 URL）。OAuth 驱动未注册，`auth/start` 类接口不在本包范围。

## 5. 数据模型与格式
### 5.1 类型
`Preset` 是端点数据：`Endpoints` 按 `xir.Protocol` 存 base URL，`Auth` 是默认认证，`Plans` 覆盖端点（可带 `ModelFilter` glob），`Models` 描述列表来源，`Headers` 是静态 header。`Resolved` 是合并后的不可变视图；`Credential` 是调用方注入的秘密，不进入目录。

### 5.2 序列化
这些类型没有为 cassette/DB 设计的 JSON tag；本包不序列化任何值。凭据只在内存中传递，不写 DB、不写日志、不写 cassette。调用方若自行 JSON 化 `Credential` 需承担泄漏风险。

### 5.3 配置
本包不读 YAML/env，不做热更新；`Config` 由调用方构造并传给 `Resolve`，验证失败返回 error。`APIVersion` 仅 Azure 使用；`Project`/`Location` 仅 Vertex 且必须成对。

### 5.4 数据放置
| 数据 | 文件 / DB / 内存 | 理由 | 丢失后果 |
| --- | --- | --- | --- |
| preset 目录 | 编译进二进制（只读数据） | 静态端点事实 | 重新编译 |
| `Resolved` | 内存 | 每次解析派生 | 可重建 |
| `Credential` | 内存 | 秘密不落盘 | 需重新注入 |
| 模型列表 | 内存（调用方持有） | 发现结果不缓存 | 重新请求 |
| cassette / DB | 不写 | 本包无持久化职责 | 不适用 |

不写 cassette、不写 DB；无保留期/清理；所有值可由 preset + 调用方配置重建。

## 6. 算法与不变量
### 6.1 算法
**Resolve**：查找 preset（空则合成 `Custom` vendor）→ 克隆端点 → 叠加 plan 端点 → 叠加 `Config.Endpoints` → 校验非空、协议合法、每个 base URL 是绝对 http(s)、无 userinfo/fragment、去尾斜杠 → Vertex project/location 成对校验 → 合并 auth（mode/header/scheme/OAuth/Refresh/Headers）→ 按端点推导 `Protocols`（或校验显式列表）→ 归一化每模型协议覆盖 → 合并静态 headers → Vertex 默认 `ModelResource=publishers/google/models`。

**BuildURL**：base 非空时，Chat/Responses 把客户端 `/v1` 折进 base；Messages 只在 base 以 `/v1` 结尾时折叠；Gemini 处理 `/models` base 与版本段；保留客户端 query 并合并 base query；Azure 在缺省时补 `api-version`；拒绝绝对 client path。

**ApplyAuth**：credential 的 `Mode` 覆盖 resolved `Auth.Mode`；按 mode 取 APIKey/AccessToken；先删除竞争标准认证头（Authorization、X-Api-Key、X-Goog-Api-Key、Api-Key），再设置目标 header（Authorization 默认 Bearer），随后应用 `AuthSpec.Headers`（支持 `IfAbsent` 和 `{{api_key}}` 等占位符），最后应用静态 headers。

**DiscoverModels**：一次 GET（body 上限 4 MiB），先解析 `{"data":[{"id"}]}`，否则 `{"models":[{"name"/"id"}]}`，按上游顺序去重；无重试/缓存/持久化。

### 6.2 不变量
不变量：拒绝 URL 内嵌凭据；显式协议必须有端点；`ApplyAuth` 不把客户端认证头带到上游；`Resolved` 方法不修改共享 map。复杂度与端点数/模型覆盖数线性。

### 6.3 边界条件
空端点、未知 plan、Vertex 只给 project 或 location、列表空 body/坏 JSON、`client == nil`、context 取消；这些都有明确返回值或默认行为。

## 7. 失败语义
| 场景 | 行为 | 影响上游健康 | 客户端看到 |
| --- | --- | --- | --- |
| 未知 preset/plan | `Resolve` 返回 error | 不适用 | 配置错误 |
| 端点非法/缺失 | `Resolve` 返回 error | 不适用 | 配置错误 |
| 缺凭据 | `ApplyAuth` 返回 `ErrMissingCredential` | 不适用 | 请求不会发出 |
| 列表 401/403/404/429/5xx | `*StatusError`，可 `errors.Is` 分类 | 不适用 | 由调用方处理 |
| 列表网络错误/取消 | 包装错误，不重试 | 不适用 | 由调用方处理 |
| 无列表源 | `ErrNoModelList` | 不适用 | 回退静态列表（若调用方有） |

本包 fail-closed：配置或凭据不成立就不构造请求；它不替调用方重试或换渠道。

## 8. 性能
不在热路径。`Resolve`/`BuildURL`/`ApplyAuth` 是内存操作，分配与端点/header 数线性；`DiscoverModels` 是显式网络调用。无 benchmark；调用方应在配置变更时解析一次并复用 `Resolved`。

## 9. 安全
凭据只在内存，包内不记录日志，错误消息不回显 key/token。`normalizeEndpoint` 拒绝 `user:pass@host` 和 fragment；`ApplyAuth` 先清除客户端认证头，避免凭据串到上游。`DiscoverModels` 会对 `Resolved` 的端点发请求，base URL 由调用方配置，本包不做 SSRF 白名单/私网限制，需由渠道管理/网络策略负责。静态 header 模板可注入 `{{api_key}}` 等值，属显式配置行为，不要把不可信输入放进模板。

## 10. 可观测性
无指标、事件或日志。错误通过 sentinel/`StatusError` 返回；模型发现结果不缓存，排障需看调用方日志。`StatusError.Body` 截断到 512 字节，可能包含上游错误文本。

## 11. 测试与验收
### 11.1 单元测试
`GOCACHE=/tmp/trajecta-gocache go test ./pkg/upstream` 通过（Go 1.26.9，`ok` 约 0.016s）。覆盖 20 个 preset 的端点/认证/alias、plan 查找与 `MatchesModel`、Resolve 默认/覆盖/自定义/错误/Azure、BuildURL 全部 preset 端点与错误、ApplyAuth 各 mode/清头/模板/静态 header、Capabilities 与每模型覆盖、DiscoverModels OpenAI/Gemini/静态 header/状态码/取消/坏 JSON、example 输出。无 fuzz/benchmark，无网络测试。
### 11.2 集成测试
只有 monitor 的 base URL 回退有覆盖；未与 `internal/channel`/gateway 做端到端。
### 11.3 差分测试
与 `internal/upstream` 的旧解析逻辑未做逐项对照。
### 11.4 验收标准
- [x] 公开 API 有 doc/example，离线测试通过。
- [ ] OAuth/CLI credential 流程（预留，未实现）。
- [x] 端点/认证/URL 错误有分类测试，不写 cassette/DB。
- [x] 文档同步（本文）。

## 12. 迁移、兼容、回滚
新增包，无数据库迁移、无 cassette 变化、无 feature flag。现有 `internal/upstream` 保持运行时事实源；回滚只需移除 monitor 的 `LookupPreset` 调用。旧路径删除条件：渠道控制面与 gateway 完全改用 `pkg/upstream` 并通过差分测试。

## 13. 开放问题
| 问题 | 影响 | 建议 | 需要谁决定 |
| --- | --- | --- | --- |
| OAuth 首个 driver 做哪个？ | 登录/ToS | 先接 Claude Code 或 Codex，显式 opt-in | 产品/安全 |
| preset 与 DB 渠道如何分工？ | 控制面 | preset 只提供默认值，DB 覆盖并持久化 | M11 |
| 是否增加 SSRF/私网限制？ | 安全 | 在渠道配置/出站策略层做，不塞进 URL 构造 | 安全 |

## 14. 实施拆分
1. 已完成：preset 目录、Resolve、BuildURL、ApplyAuth、Capabilities、DiscoverModels、测试。
2. 下一步：M11 用 preset 默认端点/auth 创建渠道并落 DB；gateway 用 `Resolved` 取代 `internal/upstream` 的纯解析并做差分。
3. 后续：OAuth/CLI credential driver（本轮不实现）。

验证 `go test ./pkg/upstream`，接入后跑 `go test ./internal/monitor ./internal/upstream` 与 `task check:quick`。

## 15. 参考
- [Resolve](../../pkg/upstream/resolve.go)、[BuildURL](../../pkg/upstream/url.go)、[ApplyAuth](../../pkg/upstream/auth.go)
- [Preset 目录](../../pkg/upstream/presets.go)、[模型发现](../../pkg/upstream/models.go)
- [总体架构](../01-overall-architecture.md) §6.2、§8
- [协议与 Provider](../../docs/PROTOCOLS_AND_PROVIDERS.md)
- [路由与凭据](../../docs/ROUTING_AND_CREDENTIALS.md)
