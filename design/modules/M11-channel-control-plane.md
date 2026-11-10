# M11 渠道控制面

## 0. 头部

| 字段 | 值 |
| --- | --- |
| 模块 ID | M11 |
| 模块名 | 渠道控制面 |
| 归属 | `internal/channel`、`internal/store`、`internal/monitor`、`pkg/upstream` |
| 状态 | 部分落地 (P2) |
| 依赖模块 | M07、M08 |
| 被依赖模块 | M13 |
| 目标版本 | 2.0 |
| 最后更新 | 2026-11-09 |

## 1. 目标与非目标

**目标**：用“preset + key”或“自定义 endpoint + key”创建渠道，按协议维护模型列表，模型默认关闭、启用立即生效；模型广场展示所有启用模型、支持渠道和协议。

**当前非目标/待做**：`channel_models` 还没有 per-model 多协议列，`channel_configs` 仍只有一个 `base_url`/`api_type`/`protocol_family`；OAuth driver 只预留接口；UI 还没有完整的 preset gallery/多协议编辑。

## 2. 当前实现

- 渠道/模型只在应用数据库：`channel_configs`、`channel_models`，写操作走 `ConfigurationTransaction` + router reload。
- 旧 preset 注册表仍提供 `protocol_family`/`routing_profile`；`pkg/upstream` 提供新的 20 个 provider preset，含每协议 endpoint、auth、plan 变体、模型发现。
- 创建渠道时若 `base_url` 为空且 preset 已知，`defaultPresetBaseURL` 按 `api_type`/`protocol_family` 选择 endpoint（OpenAI、Anthropic、DeepSeek、OpenRouter、Google 等）。
- `GET /api/catalog/providers`：只读返回 `pkg/upstream.Presets()` 的 id/name/kind/endpoints/protocols/auth/plans/models/headers/docs，不包含任何 credential。
- `GET /api/provider-presets`（旧形状）现在也带 `endpoints`/`protocols`（当旧 preset id 与 catalog 匹配时）；创建渠道表单选中 preset 后按 `api_type` 自动填 `base_url`，留空时后端仍用 `defaultPresetBaseURL` 兜底。
- 模型广场：`GET /api/model-square` 已有每渠道 `interfaces`、顶层 `protocols`，卡片展示协议 badge、`channel_count`/`available_channel_count`。
- 模型启用/禁用：`PATCH /api/channels/{id}/models/{model}` 与 batch，提交后 reload。

## 3. 对外契约

```text
GET /api/catalog/providers
GET /api/provider-presets
GET /api/channels
POST /api/channels
PATCH /api/channels/{id}
GET /api/channels/{id}/models
PATCH /api/channels/{id}/models/{model}
PATCH /api/channels/{id}/models/batch
POST /api/provider-model-detect
```

`GET /api/catalog/providers` 的 item：

```json
{
  "id": "openai",
  "name": "OpenAI",
  "kind": "vendor",
  "endpoints": {"chat_completions": "https://api.openai.com/v1", "responses": "..."},
  "protocols": ["chat_completions", "responses"],
  "auth": {"mode": "api_key", "header": "Authorization", "scheme": "Bearer"},
  "plans": [{"id": "coding", "name": "Coding Plan", "endpoints": {...}}],
  "models": {"list_path": "/models", "no_list": false}
}
```

## 4. 数据放置

| 数据 | 位置 | 说明 |
| --- | --- | --- |
| preset 定义 | 编译进二进制（`pkg/upstream`） | 只读目录，版本随代码 |
| 渠道/模型/凭据 | DB | 配置事实 |
| 模型发现结果 | DB `channel_models` | 默认 disabled |
| 凭据明文 | 内存 | 请求期解密 |
| 模型广场聚合 | 内存/按请求 | 从 DB 投影 |

## 5. 待做

- [x] 渠道级 per-protocol base URL：复用 `capabilities.endpoints`（无需新表），proxy 按请求协议选择；
- [x] 渠道 API 顶层 `endpoints` 与高级选项里的每协议输入；
- [x] `channel_models.protocols_json`、模型 API `protocols`、channel→target 投影、router 按协议 wire name 已落地；UI 编辑仍待做。
- [ ] `channel_models` 增加 per-model `protocols_json`/`wire_names_json`。
- [x] `GET /api/catalog/providers` 接入创建弹窗的“浏览内置目录”；选中条目填入 endpoint/协议。多协议编辑与 OAuth 仍待做。
- [ ] OAuth driver：`AuthDriver` 实现、`channel_credentials` 表、auth start/callback/status API（当前只预留）。
- [ ] 模型广场卡片直接启用/禁用渠道模型。
- [ ] 清理旧 `internal/upstream` preset 注册表或把它桥接到 `pkg/upstream`。

## 6. 测试

- `internal/monitor/server_catalog_test.go`：catalog 返回 OpenAI/DeepSeek/OpenRouter 的 endpoint/auth/kind；
- `internal/monitor/preset_base_url_test.go`：preset base URL 选择；
- `internal/monitor/model_square_api_test.go`：协议 badge；
- `internal/channel`/`internal/store` 现有测试。
