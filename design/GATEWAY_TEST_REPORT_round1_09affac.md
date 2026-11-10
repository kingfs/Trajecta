# `/data/gateway-test` 环境测试报告

> 日期：2026-11-10  
> 代码：`main` @ `09affac` + 本轮修复  
> 环境：`/data/gateway-test`，独立 compose project `gateway-test`

## 1. 环境与隔离

- 没有触碰 `/data/gateway` 的运行实例（38080/38081）；
- 使用独立 project：`gateway-test`；
- Postgres 17 + 应用容器，端口 `28080/28081`，Postgres `25432`；
- 应用镜像：当前 `main` 的 server/trajecta 静态二进制 + 现有 runtime 基础镜像；
- 上游 mock：`gateway-test` 网络内 `mock-upstream:39099`，提供 chat/messages/responses/stream/models。

Docker 内 Go 依赖下载受网络限制，因此镜像采用“宿主机 Go 缓存构建静态二进制 + runtime 镜像覆盖”的方式；这不是代码问题。

## 2. 验证项与结果

| 项 | 结果 |
| --- | --- |
| 应用启动 / Postgres 迁移 | 通过；`channel_models.protocols_json` 已存在 |
| API token / Monitor JWT 登录 | 通过 |
| 普通 chat 请求 | 通过 |
| per-model chat wire name | 通过；上游收到 `wire-chat` |
| per-model messages wire name | 通过；上游收到 `wire-messages` |
| OpenAI-compatible 渠道的 per-model Messages | 通过 |
| 关闭 per-model chat | 请求被拒绝（`no_supporting_target`） |
| streaming chat / usage | 通过；SSE 正常，trace usage=5 |
| model square protocols | 通过；同时显示 `chat_completions` / `messages` |
| `plan_engine=route` 热切换 | 通过，恢复 legacy 正常 |
| Qwen coding plan key 校验 | 通过；`sk-` 400，`sk-sp-` 200 |
| Qwen Anthropic coding-plan 预设 | 通过；Messages 请求成功 |
| minimal cassette | 通过；prelude 无 events，raw/detail 时间线有 request/response 兜底 |
| XIR findings | 通过；敏感值只出现在 `source=xir` 的 advisory 项，无明文 |
| Playwright 真实 UI 登录/渠道详情 | 通过 |
| UI 协议编辑保存 | 修复后通过；PATCH 200，保留其他协议 |

## 3. 发现并修复的问题

### 3.1 协议编辑器保存是空操作

**现象**：模型卡里的「协议」编辑器能展开、能输入，但点击保存没有任何请求。

**原因**：`ProviderModelRow` 的调用点没有传 `onSaveProtocols` / `savingProtocols`，组件的保存按钮调用 `undefined`。

**修复**：在 `ChannelDetailPage.jsx` 调用点传入：

```jsx
onSaveProtocols={(protocols) => saveModelProtocols.mutate({ model: model.model, protocols })}
savingProtocols={saveModelProtocols.isPending}
```

**验证**：Playwright 真实环境点击保存，捕获到 `PATCH /api/channels/.../models/gateway-test-model` 200。

### 3.2 渠道详情的 `models_usage` 缺少 `protocols`

**现象**：打开编辑器时枚举为空；用户只改一个协议后保存，原有其他协议被丢弃，且被默认 `enabled=false`。

**原因**：`/api/channels/{id}` 的 `models_usage` 在 `enrichChannelItemAnalytics` 中手工构造 `modelChannelItem`，没有带 `ProtocolsJSON`。

**修复**：详情构造和 `enrichModelChannelItemFromRecord` 都设置 `row.Protocols = decodeChannelModelProtocols(record.ProtocolsJSON)`。

**测试**：新增 `TestChannelDetailIncludesModelProtocols`。

### 3.3 新协议条目默认关闭

**现象**：协议原本没有条目时，输入 wire model 后保存会写成 `enabled=false`。

**修复**：编辑器在“无条目 + 输入非空 wire model”时自动置 `enabled=true`；已有条目保持原值，不强行改写。

## 4. 保留的观察项（未在本次修改）

1. **trace meta model 记录 upstream wire name**
   - 这是既有 alias 行为：cassette 记录 post-alias body，`meta.model` 也来自该 body。
   - 结果是渠道详情会出现 `wire-chat` / `wire-messages` 这类 history-only 行。
   - route plan 已分别保存 `requested_model` / `upstream_model`；是否把 `meta.model` 改为客户端模型属于产品语义决策，未在本次环境测试中改动。

2. **禁用协议返回 502**
   - 请求被 router 以 `no_supporting_target` 拒绝，HTTP 状态是 502；这是既有路由语义，是否改为 4xx 需要单独决策。

3. **Docker 构建网络**
   - 当前环境无法从 Docker 内访问 `proxy.golang.org`；测试镜像用宿主机构建二进制，产品 Dockerfile 未改动。

## 5. 最终验证

```bash
go build ./cmd/server ./cmd/trajecta
go test ./...
```

前端：

```bash
vite build
node --test tests-unit/**/*.test.js   # 243 passed
```

真实环境额外检查：

- `go test -race ./...` 曾在 `main` 通过；本次新增改动集中在 monitor 投影和前端，已重新跑 `go test ./...` 与 UI build/单测。
