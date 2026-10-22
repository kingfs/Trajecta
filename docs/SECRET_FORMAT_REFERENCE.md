# 密钥与 Token 格式识别参考（面向状态机实现）

> 本文是一份**格式字典 + 工程实现指南**，用于构建"敏感凭据提取与识别"能力（例如 Trajecta 的语义分析管道、CI 泄露扫描、日志脱敏）。
> 全文只描述**字符串形态**，不提供任何真实凭据；文中所有示例均为明显伪造的占位串。

## 0.1 为什么要单独做一份格式字典

靠一个 `sk-[A-Za-z0-9]{20,}` 正则去抓密钥，会同时踩两个坑：

- **漏报**：`sk-` 只是冰山一角。真实世界里既有强前缀型（`ghp_`、`sk-ant-api03-`、`AIza`），也有纯十六进制型（32 位 hex 的 DeepSeek key、24 位 hex 的极光 AppKey）、还有需要**解码后才能判定**的类型（JWT、MiniMax key、PyPI macaroon、Discord bot token）。
- **误报**：40 位 hex 既可能是 AWS Secret Access Key，也可能是一次 git commit SHA-1；32 位 hex 既可能是 Datadog API Key，也可能是 MD5。没有上下文与校验位，任何正则都无法区分。

因此识别器需要的是**"前缀 + 字符集 + 长度 + 结构 + 校验位 + 上下文关键词"六元组**，而不是单条正则。这正是状态机（DFA）擅长表达、而正则表达吃力的地方。

## 0.2 范围

| 层次 | 覆盖内容 | 本文位置 |
| --- | --- | --- |
| 大模型厂商（重点） | OpenAI / Anthropic / Google / Azure / AWS Bedrock / Mistral / Cohere / xAI / Groq / Together / Replicate / HuggingFace / Perplexity / OpenRouter / Voyage / Deepgram / ElevenLabs；国内 DeepSeek / Moonshot / 智谱 / 阿里百炼 / 百度千帆 / 火山方舟 / 腾讯混元 / 讯飞 / MiniMax / 阶跃 / 硅基流动 等 | 第 3、4 章 |
| 结构性 Token | JWT / JWE / PASETO / Macaroon / PEM 私钥 / JWK / Basic / Bearer / SigV4 / SAS / 预签名 URL / 连接串 / 密码哈希 | 第 2 章 |
| 云与基础设施 | AWS / GCP / Azure / 阿里云 / 腾讯云 / 华为云 / K8s / Vault / Terraform / Docker / CI | 第 5 章 |
| 常见业务系统 | GitHub / GitLab / npm / PyPI / Slack / Discord / Telegram / Stripe / Twilio / SendGrid / Datadog / Sentry / Supabase；国内钉钉 / 飞书 / 企业微信 / 微信支付 / 支付宝 / 短信 / 推送 / 对象存储 / IM / 地图 | 第 6、7 章 |
| 工程落地 | 状态机分层设计、消歧策略、离线校验算法、与 Trajecta 现有代码的衔接 | 第 1、8 章 |

## 0.3 置信度分级

表中每条规则都带一个置信度字母，含义如下。**实现时应按置信度决定默认告警等级**。

| 等级 | 含义 | 可用于 |
| --- | --- | --- |
| **A** | 厂商官方文档**明确写出**了格式（前缀/长度/字符集） | 直接作为强判定，无需上下文 |
| **B** | 官方 SDK / CLI / 官方示例代码中存在**格式校验逻辑或固定样例** | 强判定，建议保留上下文加分 |
| **C** | 开源扫描器规则（gitleaks / trufflehog / Nosey Parker / detect-secrets）或高质量社区证据 | 判定 + 必须依赖上下文关键词或长度，才可提升置信度 |
| **D** | 仅社区观察或推断，无权威来源 | 只做"线索"，不作为独立告警依据 |

## 0.4 本文的证据来源

本文所有规则均可回溯到下列**可复核**的来源（抓取日期：2026-10-14）：

| 来源 | 性质 | 规模 |
| --- | --- | --- |
| `gitleaks/gitleaks` → `config/gitleaks.toml` | 开源扫描器规则（正则 + 熵阈值 + 关键词） | 222 条，见附录 A |
| `praetorian-inc/noseyparker` → `crates/noseyparker/data/default/builtin/rules/*.yml` | 开源扫描器规则（正则 + 正/负样例 + 参考文献） | 189 条，见附录 B |
| `trufflesecurity/trufflehog` → `pkg/detectors/*` | 开源扫描器检测器（前缀 + 关键词 + **在线校验**逻辑） | 907 个检测器，其中 AI/LLM 相关 19 个，见附录 C |
| `Yelp/detect-secrets` → `detect_secrets/plugins/*.py` | 开源扫描器插件（denylist / 熵检测） | 27 个，见附录 D |
| 各厂商官方文档 / SDK / API Reference | 一手证据（用于确认长度、字符集、前缀） | 逐条标注 URL |
| Microsoft Purview **SIT**（Sensitive Information Type）定义文档 | 微软官方的敏感信息类型字典，**每条都标注 `Checksum = Yes/No`** 与字符集/长度 | 覆盖 Google API key、Entra client secret、Azure Storage key 等 |

> 说明：`gitleaks` 的 `entropy` 字段是**候选串的香农熵下限，单位 bit/字符**。理解这一点对设计熵阈值很关键——见 §1.5。

## 0.5 免责与使用注意

- 本文示例全部是**伪造占位串**（例如 `sk-EXAMPLE...`、`AIza` 后接全 `A`）。请勿把任何形似真实密钥的字符串写入代码库或本文档。
- **不要把本文档本身喂给密钥扫描器**：它由大量"看起来像密钥"的模式组成，会被自家工具判定为泄露。建议在仓库中对本文档加 `.gitleaksignore` / 扫描白名单。
- 强度排序：**离线可判定（有校验位/可解码） > 强前缀 + 定长 > 弱前缀 + 上下文关键词 > 纯熵**。默认告警等级应与之同序。

---

## 目录

| 章节 | 内容 |
| --- | --- |
| §0 | 目标、范围、置信度分级、证据来源、免责 |
| §1 | **状态机识别器的设计**：分层管线、前缀树与最长前缀、字符类、上下文窗口、熵、消歧矩阵、告警分级 |
| §2 | **结构性 Token 家族**：JWT/JWE、PASETO、Macaroon、PEM、JWK、Basic/Bearer、SigV4/SAS/预签名 URL、连接串、密码哈希、其它高结构形态 |
| §3 | **大模型厂商（海外）**：OpenAI、Anthropic、Google、Azure、AWS Bedrock、Cohere、xAI、Groq、Together、Fireworks、Replicate、HuggingFace、Perplexity、OpenRouter、Mistral、Voyage 等 + AI 工具链 |
| §4 | **大模型厂商（国内）**：DeepSeek、智谱、阿里百炼、百度千帆、火山方舟、腾讯混元、讯飞星火、MiniMax、Moonshot 等 + 国内共性陷阱 |
| §5 | **云与基础设施**：AWS、GCP、Azure、阿里云、腾讯云、华为云、百度云、火山引擎、K8s、Vault、CI/CD、PaaS |
| §6 | **常见业务系统（研发平台 / IM / SaaS / 支付）**：GitHub、GitLab、npm、PyPI、Slack、Discord、Telegram、Stripe、Twilio、Datadog、Sentry、Supabase、Firebase、Okta、Auth0 等 |
| §7 | **常见业务系统（国内）**：飞书、声网、钉钉、企业微信、微信公众号、微信支付、支付宝、推送、短信、地图 |
| §8 | **校验能力矩阵**：哪些能离线判定、GitHub PAT 的 CRC32 算法、强前缀清单、弱形态清单、在线校验 |
| §9 | **在 Trajecta 中的落地实现**：包结构与规则生成、检测分层与置信度门限、遮蔽与还原、与现有模块的边界、测试策略、性能与维护流程 |
| 附录 A | gitleaks 222 条内置规则全量清单 |
| 附录 B | Nosey Parker 189 条内置规则清单 |
| 附录 C | trufflehog 19 个 AI/LLM 检测器（含前缀与关键词） |
| 附录 D | Yelp detect-secrets 27 个插件清单 |

---

## 1. 状态机识别器的设计

### 1.1 分层管线

单条正则无法同时兼顾"低误报"和"低漏报"。推荐把识别拆成四层，每层只做一件事，后层只处理前层的候选片段：

```
原始文本 (请求体 / 响应体 / header / 日志 / 文件)
   │
   ├─ L0 预筛：关键词命中（Aho-Corasick 多模式匹配，大小写不敏感）
   │       命中 "ghp_" / "sk-ant-api03" / "AKIA" / "openai" / "T3BlbkFJ" ...
   │       —— 目的：避免对全量文本跑上百个 DFA
   │
   ├─ L1 词法：前缀 DFA 定界
   │       识别固定前缀 → 按字符类（base62/base64url/hex/base32/大写字母数字）向前推进
   │       产出候选 span = [start, end) + 命中的 detector 集合
   │
   ├─ L2 结构：长度 / 分隔符 / 分段 / 校验位 / 解码判定
   │       长度不在区间内 → 丢弃
   │       GitHub PAT → 重算 CRC32 校验位，不符 → 丢弃（离线零误报）
   │       JWT → base64url 解码 header，必须是以 "{" 开头的 JSON
   │       Discord token → 第一段 base64 解码后必须是纯数字 user id
   │       Azure storage key → base64 解码后必须是 64 字节
   │
   └─ L3 上下文：窗口内关键词 / header 名 / JSON 字段名打分
           前 80 字符内出现 "api_key" / "Authorization" / "OPENAI_API_KEY" → 加分
           出现在 .git/config、测试 fixture、文档 URL 中 → 减分
   │
   └─ 输出：{detector_id, vendor, span, confidence(0-1), reasons[]}
```

**关键点：L2 的能量最大。** 一个能离线否决的校验位，胜过十组上下文关键词。设计规则表时应优先沉淀"可校验"类型。

### 1.2 前缀树与"最长前缀优先"

多条规则共享前缀时（`sk-` 至少被 OpenAI / Anthropic / DeepSeek / Moonshot / 硅基流动 / 一些聚合网关共用），必须约定消歧顺序：

1. **更长前缀优先**：`sk-ant-api03-` 先于 `sk-ant-` 先于 `sk-`；`github_pat_` 先于 `gh`。
2. **定长优先于变长**：同时匹配 `sk-[a-z0-9]{32}`（DeepSeek）与 `sk-[A-Za-z0-9_-]{20,}`（泛化 OpenAI）时，先按定长规则判定并**吃掉**这些字符，再回退。
3. **校验位优先**：能通过 CRC32 的 GitHub PAT 规则优先于泛化的 `gh[pousr]_[A-Za-z0-9]{36}`。
4. **同前缀、不同长度的处理**：不要用"回溯重试"，而是**一次扫描记录候选终点**，在 L2 用长度集合裁决。

> 实践建议：用 `map[prefix] -> []rule`，前缀按长度降序排列；对每个起点做**多分支并行推进**（同时维护若干活跃状态），而不是分支失败后回退重扫。

### 1.3 字符类定义（必须精确到字符类，而非"字母数字"）

| 名称 | 字符类 | 常见长度 | 说明 |
| --- | --- | --- | --- |
| `base62` | `[0-9A-Za-z]` | 任意 | GitHub 校验位用它，字母表顺序是 `0-9` `A-Z` `a-z`（**注意大小写顺序影响校验计算**） |
| `base62_lower` | `[0-9a-z]` | 32 | DeepSeek、AssemblyAI 等只用小写 |
| `base64url` | `[A-Za-z0-9_-]` | 任意 | JWT 段、Anthropic body、许多随机 key |
| `base64std` | `[A-Za-z0-9+/]` + `=` 填充 | 4 的倍数 | Bedrock `ABSK...`、Azure storage key（88 位含 `==`） |
| `hex_lower` | `[0-9a-f]` | 16/24/32/40/64 | 极易与其他哈希混淆，见 §1.6 |
| `hex_upper` | `[0-9A-F]` | 32/40/64 | 部分 SDK 用大写 |
| `hex_any` | `[0-9a-fA-F]` | — | Shopify `shpat_` 后的 32 位 |
| `base32` | `[A-Z2-7]` | 16 | AWS AccessKeyId 的随机部分（gitleaks: `[A-Z2-7]{16}`） |
| `upper_alnum` | `[A-Z0-9]` | 任意 | Slack `xapp` 段、New Relic `NRAK-` |
| `printable_mixed` | 可打印 ASCII（无空格） | 无约束 | 密码类，必须靠上下文，禁止纯模式告警 |

**边界（terminator）判定**：候选串结束时，必须要求下一个字符**不属于**该字符类，或到达输入末尾。注意 `\b` 词边界与 `-`、`_` 不兼容（`-`、`_` 是 `\w` 之外/之内的边界问题），Nosey Parker 的 JWT 规则就专门注释了这一点：

```
(?: [^a-zA-Z0-9_-] | $ )   # 代替 \b，因为 \b 对 `-` 不友好
```

### 1.4 扫描窗口与"上下文关键词"

关键词窗口建议**前 120 字符 + 后 20 字符**（跨行时截断到同一行优先）。按强度分三档：

| 档位 | 例子 | 加分 |
| --- | --- | --- |
| 强上下文 | `Authorization: Bearer`、`X-Api-Key:`、`api_key=`、`OPENAI_API_KEY`、`ANTHROPIC_API_KEY`、`.azure.com`、`open.bigmodel.cn` | 高 |
| 中上下文 | 变量名含 `secret`/`token`/`key`/`credential`/`password`；文件名 `.env`、`credentials.json`；JSON 字段 `private_key`、`client_secret` | 中 |
| 弱上下文 | 出现厂商名小写串（`deepseek`、`cohere`、`zhipu`）；URL 域名 | 低 |

反向减分（降低告警等级，避免噪声）：

- 出现在 `*.md`、`*_test.go`、`fixtures/`、`examples/`、`docs/` 路径下；
- 值是明显的占位符：`xxx`、`your_api_key`、`REPLACE_ME`、`<...>`、全同字符、`changeme`、`sk_test_`（Stripe 测试态仍应告警但降级）；
- 与真实凭据格式不符的"教学用"样例（如 `AIza` 后接全 `A`）。

### 1.5 熵：什么时候用、怎么用

- 熵是**弱信号**，只能用于"过滤明显不是随机的串"，绝不能作为独立判定依据（`correct-horse-battery-staple` 熵很低但仍是密码；`deadbeef...` 重复模式熵低但可能是真 key）。
- 数学上限（香农熵 bit/字符）决定了阈值的合理区间：

| 字符集 | 符号数 | 每字符熵上限 |
| --- | --- | --- |
| hex | 16 | **4.00** |
| base32 | 32 | 5.00 |
| 大写字母+数字 | 36 | 5.17 |
| base62 | 62 | **5.95** |
| base64url | 64 | 6.00 |

> 因此 `gitleaks` 对 32 位 hex 类规则给 `entropy = 3`（而非 4.5）是合理的：hex 类规则**不可能**达到 4.5。给 hex 类规则设 4.5 阈值 = 100% 漏报。

- 建议策略：
  - 有强前缀 + 定长 → **不判熵**；
  - 有前缀但长度可变（`sk-[A-Za-z0-9_-]{20,}`）→ 熵 ≥ 3.5；
  - 无前缀、纯 hex/base64 的泛化检测 → 熵 ≥ 4.0（hex）/ 4.5（base64），**且必须**有强上下文关键词。

### 1.6 消歧矩阵（同形态不同语义）

这是降低误报的核心资产，建议在规则表里显式编码：

| 形态 | 可能的语义 | 消歧手段 |
| --- | --- | --- |
| 40 位 hex | git commit SHA-1 / AWS Secret Access Key / Datadog APP Key / Sentry token | 上下文（`aws_secret`、`git` diff 中 40 hex 紧跟 `commit`）；AWS Secret 常与 `AKIA...` 同行出现，**配对检测** |
| 32 位 hex | MD5 / DeepSeek key / Datadog API Key / 极光 JPush AppKey / 微信 AppSecret / RAM 子账号密码 | 前缀（`sk-`）+ 上下文（`deepseek`、`datadog`、`jpush`）；32 hex 裸串默认**不告警** |
| 36 位带 4 个 `-` | UUID / Heroku API Key / MessageBird Client ID | 上下文（`heroku`）+ 是否能通过 UUID 版本位校验（true UUID 有版本位，随机 key 不满足 v4 模式） |
| 88 位 base64 结尾 `==` | Azure Storage Account Key | base64 解码后长度必须是 64 字节 |
| `eyJ...` 三段 | JWT | 解码 header 是 JSON；否则可能是普通 base64 数据 |
| 20 位大写字母数字 | AWS AccessKeyId / GCP 旧式 key | 前 4 位 `AKIA`/`ASIA`/`A3T`/`ABIA`/`ACCA` |
| 39 位 `AIza...` | Google API Key（可能同时是 Gemini key、Maps key、Firebase key） | 仅能判定为"Google API Key"，厂商归属需域名/上下文 |

### 1.7 输出与告警分级建议

```
credential_finding = {
  detector_id: "openai.project_key",     # 规则表主键
  vendor:      "OpenAI",
  kind:        "api_key",
  span:        [start, end),
  confidence:  0.99,                      # 0-1
  reasons:     ["prefix=sk-proj-", "length=164", "marker=T3BlbkFJ", "ctx=env_var"],
  verify_hint: "offline:checksum|cannot|online:provider_api"
}
```

默认分级（可按团队策略调整）：

| 情形 | 等级 |
| --- | --- |
| 前缀 + 长度 + **校验位通过**（GitHub PAT） | Critical |
| 强前缀 + 精确长度（`sk-ant-api03-` + 93 + `AA`） | Critical |
| 强前缀 + 长度区间（`sk-proj-`） | High |
| 可解码结构（JWT 解码成功且含 `alg`） | High（JWT 也可能是公开 token，需看 payload 的 `iss`/`role`） |
| 弱形态 + 强上下文（32 hex + `deepseek`） | Medium |
| 纯熵（无前缀无上下文） | Low / 仅统计，不建议单独告警 |

---

## 2. 结构性 Token 家族（与厂商无关的通用形态）

这一类 token 的判定**不依赖厂商前缀**，而是依赖自身结构。它们的误报率天然更低，应优先实现。

### 2.1 JWT / JWS（三段式）—— **可解码，最值得做深度判定**

形态：`base64url(header) . base64url(payload) . base64url(signature)`

| 判别点 | 规则 | 置信度 |
| --- | --- | --- |
| 通用形态 | `\bey[A-Za-z0-9_-]{12,}\.ey[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{10,}={0,2}` | A |
| header 开头 | base64url 的 `{"` 恒为 `ey`（所以规则以 `ey` 起头是**结构必然**，不是启发式） | A |
| 解码校验 | base64url 解码 header/payload **必须**是合法 JSON 对象；失败即否决 | A |
| `alg` 与签名长度对应 | `HS256/384/512` → 签名 43/64/86 字符；`RS256/384/512` → 342/684/1366 字符；`ES256` → 86；`ES512` → 171；`none` → 空（末尾点号） | A |
| JWE | **5 段**（header.encrypted_key.iv.ciphertext.tag），不要误判为 JWT | A |
| 未签名 JWT | `eyJ...eyJ....`（第三段为空）——常见于测试或误配置 | A |

**载荷语义**（高价值，可决定严重级别）：

| 载荷特征 | 身份 | 风险 |
| --- | --- | --- |
| `"iss":"supabase"` + `"role":"anon"` | Supabase anon key | 公开可发布，**低** |
| `"iss":"supabase"` + `"role":"service_role"` | Supabase service_role key | **绕过 RLS，Critical** |
| `"iss":"https://securetoken.google.com/<proj>"` | Firebase ID token | 中 |
| `"iss":"https://accounts.google.com"` + `"azp"` | Google OAuth ID token | 中 |
| `"iss":"https://<tenant>.auth0.com/"` | Auth0 ID/access token | 中 |
| `"iss":"https://sts.windows.net/<tid>/"` + `"appid"` | Azure AD access token | 中 |
| `"iss":"https://.../realms/<realm>"` + `"typ":"Bearer"` | Keycloak access token | 中 |
| `"iss":"<数字>"` + RS256 + 短 `exp` | GitHub App JWT | 高（可换取 installation token） |
| `"alg":"HS256"` 且 payload 含 `"role":"admin"` | 自建系统会话 | 高 |
| 超长 `exp`（如 0 或 10 年后） | 长期凭据 | 高 |

**状态机建议**：JWT 做成"子状态机"——三段式骨架 → base64url 解码（可用无表 SIMD/查表解码）→ 首字节 `{` 判定 → 最小 JSON 扫描提取 `alg`/`iss`/`role`/`exp`。注意 base64url 无填充，解码时要自行补 `=`。

### 2.2 PASETO

形态：`v<版本>.<purpose>.<payload>[...]`，purpose ∈ `local` / `public`。
版本有 `v1`~`v4`。示例形状：`v4.local.<base64url>`。footer 可选，用 `.` 分隔。**前缀即强判定**。

### 2.3 Macaroon（PyPI 等）

PyPI token：`pypi-AgEIcHlwaS5vcmc` + `[A-Za-z0-9_-]{50,1000}`。
`AgEIcHlwaS5vcmc` 是 macaroon 头部的 base64url 片段（内含 `pypi.org`）。固定前缀 + 长 base64url 尾部 → 强判定。

### 2.4 PEM 私钥与"套壳"私钥

| 形态 | 判定 |
| --- | --- |
| `-----BEGIN <TYPE>-----` … `-----END <TYPE>-----` | TYPE 含 `PRIVATE KEY` 即敏感；`CERTIFICATE`/`PUBLIC KEY` 不敏感 |
| 常见 TYPE | `RSA PRIVATE KEY`、`DSA PRIVATE KEY`、`EC PRIVATE KEY`、`OPENSSH PRIVATE KEY`、`PRIVATE KEY`(PKCS#8)、`ENCRYPTED PRIVATE KEY`、`PGP PRIVATE KEY BLOCK`、`SSH2 ENCRYPTED PRIVATE KEY` |
| 主体字符集 | base64 标准表，每行 64 字符（RFC 7468） |
| 旧式头 | `Proc-Type: 4,ENCRYPTED` + `DEK-Info: AES-256-CBC,<hex IV>` |
| 源码内拼接 | 字符串用 `+` 拼接或 `\r\n` 转义，需要先做"去引号 + 去拼接"预归一化 |
| base64 套壳（K8s Secret / 环境变量） | 前缀 `LS0tLS1CRUdJTiBSU0EgUFJJVkFURSBLRVktLS0t`(RSA) / `...REsA`(DSA) / `...RUMy`(EC) / `LS0tLS1CRUdJTiBQUklWQVRFIEtFWS0tLS0t`(PKCS#8) |
| **离线校验** | `OPENSSH PRIVATE KEY` 的 base64 解码结果必须以 `openssh-key-v1\0` 开头 —— 可据此 100% 确认 |

### 2.5 JWK（JSON 私钥材料）

形态：JSON 对象含 `"kty"`。**私钥**判定：RSA/EC 含 `"d"`；oct 含 `"k"`。仅含 `"n"`/`"e"`/`"x"`/`"y"` 的是公钥，不敏感。
典型：`{"kty":"RSA","n":"...","e":"AQAB","d":"...","p":"...","q":"...","dp":"...","dq":"...","qi":"..."}`。
这与 Google 服务账号 JSON 里的 `private_key` 字段是两类东西，注意区分。

### 2.6 HTTP 认证头

| 形态 | 正则要点 | 说明 |
| --- | --- | --- |
| Basic | `(?i)Authorization\s*:\s*Basic\s+([A-Za-z0-9+/]{6,}={0,2})` | **离线校验**：base64 解码必须含 `:`；很多扫描器因为会误伤 `Basic` 之后的任意 base64 而漏报 |
| Bearer | `(?i)Authorization\s*:\s*Bearer\s+([A-Za-z0-9._~+/-]{6,}=*)` | 只说明"是个 bearer token"，厂商归属要靠 token 自身前缀 |
| 其它 header 名 | `X-Api-Key`、`X-Auth-Token`、`Api-Key`、`X-Amz-Security-Token`、`Ocp-Apim-Subscription-Key`、`X-Goog-Api-Key`、`Private-Token`(GitLab)、`Authorization: token <pat>`(GitHub) | 这些 header 名本身是高置信上下文 |

### 2.7 云签名结构（含凭据 ID，可解析出 AccessKeyId）

| 形态 | 结构 | 关注点 |
| --- | --- | --- |
| AWS SigV4 | `AWS4-HMAC-SHA256 Credential=<AKID>/<yyyymmdd>/<region>/<service>/aws4_request, SignedHeaders=..., Signature=<64 hex>` | 可解析出 `AKIA...`，与 Secret 配对 |
| S3 预签名 URL | `X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=<AKID>%2F...&X-Amz-Signature=<64 hex>` | `X-Amz-Security-Token` 存在说明是临时凭证；**SigV4a（`AWS4-ECDSA-P256-SHA256`）的签名是 ECDSA DER 的 hex，长度可变，不要按 64 位硬校验**；旧式 SigV2 是 `AWSAccessKeyId=…&Expires=…&Signature=<urlencoded base64>`，字符类完全不同 |
| GCS 签名 URL | `X-Goog-Algorithm=GOOG4-RSA-SHA256&X-Goog-Credential=...&X-Goog-Signature=<hex>` | 同理 |
| Azure SAS | `sv=..&ss=..&srt=..&sp=..&se=..&sig=<43-44 base64url>` | `sig` 是 HMAC-SHA256 base64url |
| 阿里云 OSS 签名 URL | `OSSAccessKeyId=LTAI...&Expires=..&Signature=<urlencoded base64>` | 同时暴露 AccessKeyId |
| 腾讯云 COS 签名 URL | `q-sign-algorithm=sha1&q-ak=AKID...&q-sign-time=..&q-signature=<40 hex>` | `q-ak` 即 SecretId |
| 百度云 BCE v3 | `Authorization: bce-v3/ALTAK-<ak>/<timestamp>/<signature>` | 前缀 `bce-v3/ALTAK-` 是强判定 |

### 2.8 连接串与 DSN

形态识别：`scheme://user:password@host[:port][/db][?params]` 或 `key=value;` 串联。

| 类型 | scheme / 特征 |
| --- | --- |
| PostgreSQL | `postgres://`、`postgresql://`、`jdbc:postgresql://`、`host=... user=... password=...` |
| MySQL | `mysql://`、`jdbc:mysql://`、`Server=...;Uid=...;Pwd=...;` |
| MongoDB | `mongodb://`、`mongodb+srv://` |
| Redis | `redis://`、`rediss://`、`redis://:password@host` |
| AMQP/RabbitMQ | `amqp://`、`amqps://` |
| Kafka | `sasl.jaas.config=...username="..." password="..."` |
| MSSQL/Oracle | `Server=..;User ID=..;Password=..;`、`(DESCRIPTION=...(SERVICE_NAME=..))` |
| Elasticsearch | `https://user:pass@host:9200` |
| ODBC | `Driver={...};Server=...;Uid=...;Pwd=...;` |

**高价值敏感参数名**（不区分大小写）：`password`、`passwd`、`pwd`、`secret`、`token`、`api_key`、`apikey`、`access_key`、`accesskey`、`credential`、`auth`、`authorization`、`sig`、`signature`、`dsn`、`sas`、`sharedaccesskey`、`private_key`、`client_secret`、`sessiontoken`、`refresh_token`。

> 注意：**不要把整个 URL 当作泄露**。只标注"URL 的 userinfo 或敏感参数中含有凭据"，并给出精确 span。

### 2.9 密码哈希（可作为"疑似凭据"降级信号）

| 形态 | 正则 |
| --- | --- |
| md5crypt | `\$1\$[./A-Za-z0-9]{8}\$[./A-Za-z0-9]{22}` |
| bcrypt | `\$2[abxy]\$\d+\$[./A-Za-z0-9]{53}` |
| sha256crypt | `\$5\$(rounds=\d+\$)?[./A-Za-z0-9]{8,16}\$[./A-Za-z0-9]{43}` |
| sha512crypt | `\$6\$(rounds=\d+\$)?[./A-Za-z0-9]{8,16}\$[./A-Za-z0-9]{86}` |
| Cisco IOS PBKDF2 | `\$8\$[./A-Za-z0-9]{8,16}\$[./A-Za-z0-9]{43}` |
| Argon2 | `\$argon2(id|i|d)\$v=\d+\$m=\d+,t=\d+,p=\d+\$[A-Za-z0-9+/]+\$[A-Za-z0-9+/]+` |
| Kerberos AS-REP | `\$krb5asrep\$23\$[^:]*:[0-9a-f]{32}\$[0-9a-f]{64,}` |
| NTLM | `[0-9a-fA-F]{32}:[0-9a-fA-F]{32}`（LM:NT，常在 `pwdump` 输出中出现） |

哈希不是"可直接使用的凭据"，但**是敏感信息**（可用于离线爆破），建议单独一类 `password_hash`，等级 Medium。

### 2.10 其它高结构形态

| 形态 | 正则 / 判别 | 离线校验 |
| --- | --- | --- |
| age 私钥 | `AGE-SECRET-KEY-1[QPZRY9X8GF2TVDW0S3JN54KHCE6MUA7L]{58}` | bech32 校验和（末 6 字符），可离线验证 |
| WireGuard 私钥 | 44 字符 base64（`[A-Za-z0-9+/]{43}=`） | 解码须为 32 字节 |
| Telegram Bot Token | `\d{5,16}:[A-Za-z0-9_-]{34,35}`（第二部分以 `A` 开头） | 结构强，可离线 |
| Discord Bot Token | `base64(数字) . base64(数字) . [A-Za-z0-9_-]{27,38}` | **第一段 base64 解码必须是纯数字 user id**，可离线强判 |
| Slack Webhook | `https://hooks.slack.com/(services\|workflows\|triggers)/[A-Za-z0-9+/]{43,56}` | 结构强 |
| Slack 登录态 Cookie | `xoxd-` + 长 base64（`xoxd-` 是 `d` cookie 的 token） | 前缀 |
| GitHub App 私钥 | PEM PKCS#1 `RSA PRIVATE KEY` + `"app_id"` 上下文 | PEM 判定 |
| Google 服务账号 JSON | `"type"\s*:\s*"service_account"` 且同对象含 `"private_key"` | 结构强，整体应作为**一个** finding |
| .NET 配置节 | `<add key="...Password..." value="..."/>` | 上下文关键词 |
| .netrc | `machine <host> login <user> password <pass>` | 结构 + 文件路径 |
| Docker config | `"auths": {"<registry>": {"auth": "<base64(user:pass)>"}}` | base64 解码含 `:` |
| Kubernetes Secret | `kind: Secret` + `data:` 下 base64 值 | YAML 结构 |
| PostgreSQL SCRAM | `SCRAM-SHA-256$<iter>:<salt>$<StoredKey>:<ServerKey>` | 结构强 |

---

## 3. 大模型厂商密钥格式（海外）

> 本章是第一优先级。每条都标注置信度与来源。**示例均为伪造占位串**，`A`/`X` 的重复表示"此处是随机字符"，括号内为重复次数。

### 3.1 速查总表

| 厂商/产品 | 凭据类型 | 前缀/首部特征 | 字符集 | 长度 | 结构 | 校验方式 | 上下文关键词 | 置信度 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| OpenAI | 传统 API key | `sk-` | `[A-Za-z0-9]`（**不含** `-`/`_`） | **51** 总长 | `sk-` + 20 + `T3BlbkFJ` + 20 | 水印串 `T3BlbkFJ`（`OpenAI` 的 base64，**不是校验位**） | `Authorization: Bearer`、`OPENAI_API_KEY`、`api.openai.com` | A（gitleaks 正则 + codex 常量） |
| OpenAI | 项目密钥 | `sk-proj-` | `[A-Za-z0-9_-]` | **164**（74/74）或 **132**（58/58） | `sk-proj-`(8) + 段(74\|58) + `T3BlbkFJ` + 段(74\|58) | 无校验位 | `sk-proj-`、`OPENAI_API_KEY` | A（前缀）/ C（段长） |
| OpenAI | 服务账号密钥 | `sk-svcacct-`（旧写法 `sk-service-`、`sk-service-<名称>-`） | `[A-Za-z0-9_-]` | **167**（74/74）或 **135**（58/58） | `sk-svcacct-`(11) + 段 + `T3BlbkFJ` + 段 | 无 | `/v1/organization/projects/{id}/service_accounts` | A（前缀）/ C（段长） |
| OpenAI | 管理密钥 | `sk-admin-` | `[A-Za-z0-9_-]` | **133**（58/58 有样本支持）；74/74 仅被正则放行、**无样本** | `sk-admin-`(9) + 段(58) + `T3BlbkFJ` + 段(58) | 无 | `/v1/organization/admin_api_keys` | A（前缀）/ C（58/58） |
| OpenAI | 组织 ID | `org-`（**连字符**） | base62 | 27（`org-` + 24） | 无 | 非凭据 | `OpenAI-Organization`、`OPENAI_ORG_ID` | C |
| OpenAI | 项目 ID | `proj_`（**下划线**） | base62 | 29（`proj_` + 24） | 无 | 非凭据 | `OpenAI-Project`、`OPENAI_PROJECT_ID` | C |
| OpenAI | Webhook 签名密钥 | `whsec_` | base64 标准表 | 50（前缀 + base64(32 字节)） | 前缀 + base64(32 字节) | 对 `${webhook-id}.${webhook-timestamp}.${rawBody}` 做 HMAC-SHA256 | `OPENAI_WEBHOOK_SECRET`、`webhook-id`（形如 `wh_…`）、`webhook-signature: v1,<base64>` | A（前缀）/D（字符集） |
| OpenAI | Realtime 临时密钥 | `ek_` | 官方示例为小写 hex，但正文又写 `ek_1234`，**自相矛盾** | 未公开（示例值 35） | 前缀 + 不透明串 | 无；短时效 10~7200 秒 | `POST /v1/realtime/client_secrets` | A（前缀）/D（长度与字符集） |
| OpenAI | Codex PAT / Agent JWT | `at-` | 未定义 | 未公开 | `at-` 走 PAT 分支，其余走 JWT 分支 | **源码不做 base64 解码、不看 alg/typ/kid** | `~/.codex/auth.json` | C |
| OpenAI | 会话令牌 | `sess-`（第三方） vs 官方 `sess_`（下划线，且不是 Bearer 凭据） | 未确认 | 未确认 | 未确认 | 未确认 | 仅第三方文档；官方 API 参考的 `sess_` 不是凭据 | D |
| Azure OpenAI | 资源密钥 | 无前缀 | 32 位**小写 hex** | 32 | 无 | 无校验位；**32 hex 与 MD5 不可区分** | Header `api-key`、host `*.openai.azure.com`、`/openai/deployments/…?api-version=` | C（形状）/ A（header 与 endpoint） |
| Anthropic | API key（个人 / 服务账号） | `sk-ant-api03-` 开头，`AA` 结尾 | `[a-zA-Z0-9_-]` | **108** = 13 + 93 + 2 | 前缀 + 93 + `AA` | **无校验位**；改尾 `AA`→`BB` 会被扫描器拒绝 | 官方：`Authorization: Bearer`；`x-api-key` 亦需保留 | A（前缀）/ C（长度与字符集） |
| Anthropic | Admin API key | `sk-ant-admin01-` 开头，`AA` 结尾 | `[a-zA-Z0-9_-]` | **110** = 15 + 93 + 2 | 前缀 + 93 + `AA` | 无校验位 | `x-api-key`、`/v1/organizations/api_keys` | A（前缀）/ C（长度） |
| Anthropic | Enterprise 组织 key | `sk-ant-api01-`（注意是 api**01**） | 未公开 | 未公开 | 前缀 + 不透明串 | 未公开 | claude.ai > Organization settings | A（前缀）/ D（其余） |
| Anthropic | Claude Code OAuth / setup token | `sk-ant-oat01-` | 未公开 | 未公开 | 前缀 + 不透明串 | 未公开 | `Authorization: Bearer`（与 `x-api-key` **互斥**）、`CLAUDE_CODE_OAUTH_TOKEN` | A（前缀）/ D（其余） |
| Anthropic | Workspace key（legacy） | **无独立前缀**（属 `sk-ant-api` 命名空间） | 未公开 | 未公开 | 靠创建上下文而非前缀消歧 | 未公开 | Console/workspace 创建 | D |
| Anthropic | Webhook 签名密钥 | `whsec_` | base64（推断） | 未公开；按 `whsec_`+base64(32 字节) 推为 50 | 前缀 + base64(32 字节) | SDK 做 HMAC 校验 + 5 分钟新鲜度 | `ANTHROPIC_WEBHOOK_SIGNING_KEY` | A（前缀）/ D（总长） |
| Anthropic | 会话 Cookie | `sk-ant-sid01-` | `[A-Za-z0-9_-]` | 可变 | 前缀 + 长 base64url | 仅前缀 | `sessionKey`、`claude.ai` | D |
| Google | API Key（Gemini/AI Studio/Maps/Firebase 通用） | `AIza` | `[A-Za-z0-9_-]` | 39 | `AIza` + 35（常见 `AIzaSy` + 33） | 无校验位 | `x-goog-api-key`、`key=` | C |
| Google | OAuth Client Secret | `GOCSPX-` | `[A-Za-z0-9_-]` | 35 | 前缀 + 28 | 无 | `client_secret` | C |
| Google | OAuth Client ID | 结尾 `.apps.googleusercontent.com` | 数字 + `-` + 字母数字 | ~72 | `<12位数字>-<32位>.apps.googleusercontent.com` | 非秘密（可公开） | — | C |
| Google | 刷新令牌 | `1//` | `[A-Za-z0-9_-]` | 100~200 | 前缀 + base64url | 无 | `refresh_token` | C |
| Google | 访问令牌 | `ya29.` | `[A-Za-z0-9_.-]` | 100+ | 前缀 + base64url | 无 | `access_token` | C |
| Google | 服务账号 JSON | `"type":"service_account"` | JSON | — | 含 `private_key`(PEM)、`private_key_id`(40 hex)、`client_email` | 结构 + PEM | 文件名 `*.json`、`GOOGLE_APPLICATION_CREDENTIALS` | A |
| Azure OpenAI / AI Foundry | 资源密钥 | 无前缀 | hex | 32 | 无 | 需上下文（微软**未**公开格式，32 位来自 trufflehog 与 Purview 以外的观察） | `api-key`、`*.openai.azure.com`、`*.cognitiveservices.azure.com` | C |
| Azure APIM | 订阅密钥 | 无前缀 | `[A-Za-z0-9-]` | 32（**管理员可自定义**） | 无 | 需 header 上下文；长度不可硬约束，否则会漏报自定义密钥 | `Ocp-Apim-Subscription-Key`、`subscription-key` | C |
| Azure Entra ID | 客户端密钥 | 第 4~6 位含 `Q~` | `[a-zA-Z0-9_~.]` | ≤ 40（gitleaks 收窄为 38~41） | 单段；中段有字面 `Q~` | **Purview 标注 `Checksum = Yes` 但算法未公开**；`\dQ~` 疑似特定签发批次产物，只能当加分项 | `client_secret`、`AZURE_CLIENT_SECRET`、邻近 `TenantId`/`AppId` | A（长度/字符集）/ C（`Q~`） |
| AWS Bedrock | 长期 API key | `ABSK` | base64 标准表 | 113~273 | `ABSK` + base64 | 高熵；结构待官方确认 | `ABSK`、`bedrock` | C |
| AWS Bedrock | 短期 API key | `bedrock-api-key-` | `[A-Za-z0-9+/=]` | 可变 | 前缀 + base64（`YmVkcm9jay5hbWF6b25hd3MuY29t` = `bedrock.amazonaws.com`） | 魔法串可离线确认 | `bedrock-api-key-` | C |
| Cohere | API token | 无前缀 | `[A-Za-z0-9]` | 40 | 无 | 熵 ≥ 4 | `cohere`、`CO_API_KEY` | C |
| xAI (Grok) | API key | `xai-` | `[A-Za-z0-9_]` | 前缀后 40~100（两来源冲突：trufflehog 写死 80） | 前缀 + 随机段 | 无 | `xai-`、`XAI_API_KEY` | C |
| Groq | API key | `gsk_` | `[A-Za-z0-9]` | 56 | 前缀 + 52（Nosey Parker 记 50–54） | 无 | `gsk_`、`GROQ_API_KEY` | C |
| Together AI | API key | `tgp_v1_` | `[A-Za-z0-9_-]` | 50 | 前缀 + 43 | 无 | `together`、`TOGETHER_API_KEY` | C |
| Fireworks AI | API key / 项目密钥 | `fw-` / `fpk_` | `[A-Za-z0-9]` | ≥ 30（前缀之后） | 前缀 + 字母数字 | 无 | `fireworks`、`FIREWORKS_API_KEY` | D |
| Replicate | API token | `r8_` | `[0-9A-Za-z_-]` | 40 | 前缀 + 37 | 无 | `r8_`、`REPLICATE_API_TOKEN` | C |
| HuggingFace | 用户访问令牌 | `hf_` | `[a-z]` | 37 | 前缀 + 34 小写字母 | 无 | `hf_`、`HF_TOKEN` | C |
| HuggingFace | 组织令牌 | `api_org_` | `[a-z]` | 42 | 前缀 + 34 小写字母 | 无 | `api_org_` | C |
| Perplexity | API key | `pplx-` | `[A-Za-z0-9]` | 53 | 前缀 + 48 | 无 | `pplx-` | C |
| OpenRouter | API key | `sk-or-v1-` | hex | 73 | 前缀 + 64 hex | 无 | `openrouter`、`OPENROUTER_API_KEY` | C |
| Mistral | API key | 无前缀 | `[A-Za-z0-9]` | 32 | 无 | 必须上下文 | `mistral`、`MISTRAL_API_KEY` | C |
| Voyage AI | API key | `pa-` | `[A-Za-z0-9_-]` | ≥ 27 | 前缀 + 24+ | 无 | `voyage`、`VOYAGE_API_KEY` | C |
| Jina AI | API key | `jina_` | `[A-Za-z0-9]` | 65 | 前缀 + 60 | 无 | `jina_`、`JINA_API_KEY` | C |
| Deepgram | API key | 无前缀 | `[0-9a-z]` | 40 | 无 | 必须上下文 | `deepgram` | C |
| AssemblyAI | API key | 无前缀 | `[0-9a-z]` | 32 | 无 | 必须上下文 | `assemblyai` | C |
| ElevenLabs | API key | 两来源冲突：检测器用**无前缀 32 位 hex**（关键词 `xi-api-key`）；社区观察为 `sk_` + 48 hex | hex | 32（检测器）/ 51（社区） | 单段或 前缀+段 | 无（待官方确认） | `elevenlabs`、`xi-api-key`、`ELEVENLABS_API_KEY` | D |
| Stability AI | API key | `sk-` | `[A-Za-z0-9]` | 48+ | 前缀 + 48 | 与 OpenAI 传统 key 冲突，需上下文 | `stability`、`STABILITY_API_KEY` | D |
| Databricks | PAT | `dapi` | hex | 37~39 | `dapi` + 32 hex + 可选 `-<数字>` | 无 | `databricks`、`dapi` | C |
| LangSmith | API key | `lsv2_pt_` / `lsv2_sk_` | `[A-Za-z0-9_]` | 可变 | 前缀 + 长串 | 无 | `LANGSMITH_API_KEY` | C |
| Langfuse | 公/私钥 | `pk-lf-` / `sk-lf-` | UUID | 42 | 前缀 + UUID v4 | UUID 版本位 | `langfuse` | C |
| Weights & Biases | API key | 两代并存：旧版**无前缀 40 位 hex**；新版 `wandb_v1_` + 长串 | hex / `[A-Za-z0-9_]` | 40 / 可变 | 单段或 前缀+段 | 无 | `wandb`、`WANDB_API_KEY` | D |

### 3.2 OpenAI —— 最有研究价值的一类

OpenAI 的 key 有一个**罕见的可离线判定特征**：key 正文里嵌着固定 base64 片段 **`T3BlbkFJ`**（即字符串 `OpenAI` 的 base64 编码的头部）。trufflehog 的检测器甚至直接把 `T3BlbkFJ` 当作**唯一关键词**来做预筛（而不是 `sk-`）。

各类形态（`A` 表示随机段；`T3BlbkFJ` 固定 8 字符）：

| 类型 | 形态 | 总长 | 段是否允许 `-`/`_` |
| --- | --- | --- | --- |
| 传统 | `sk-` + `A{20}` + `T3BlbkFJ` + `A{20}` | **51** | **不允许**（只字母数字） |
| 项目 | `sk-proj-` + `A{74}` + `T3BlbkFJ` + `A{74}` | **164** | 允许 |
| 项目（短） | `sk-proj-` + `A{58}` + `T3BlbkFJ` + `A{58}` | **132** | 允许 |
| 项目（混长） | 58/74 与 74/58 混搭（gitleaks 正则允许） | **148** | 允许 |
| 服务账号 | `sk-svcacct-` + 段 + `T3BlbkFJ` + 段 | **167**（74/74）/ **135**（58/58） | 允许 |
| 管理 | `sk-admin-` + `A{58}` + `T3BlbkFJ` + `A{58}` | **133** | 允许 |

状态机要点：

1. **预筛用 `T3BlbkFJ` 而不是 `sk-`**。这能把候选量降低几个数量级，且天然避开 DeepSeek/Moonshot 等同用 `sk-` 的厂商。
   ⚠️ **但它会自然出现在任何含 "OpenAI" 字样的 base64 数据里**（典型如 X.509 证书的 base64 DER）→ **单独匹配水印必然误报**，必须同时满足 `sk-` 前缀与分段长度。
2. `sk-proj-` / `sk-svcacct-` / `sk-admin-` 是**更长前缀**，必须在 trie 中优先于 `sk-`。命名空间一旦出现就**不得回退**到 legacy 分支：`sk-proj-` 后接 20 位短段应判为"非密钥"，而不是当成长度 28 的 legacy key。
3. **legacy 与 proj 家族的关键分叉是段字符集**：legacy 的 20 位段**只允许字母数字**，出现 `-`/`_` 即判定不是 legacy key。这条规则比长度更硬。
4. **段长 74/58 是 gitleaks 自加的约束，不是全生态共识**：trufflehog 的同族规则 `(?:proj|svcacct|service)-[A-Za-z0-9_-]+T3BlbkFJ[A-Za-z0-9_-]+` **不限段长**，其负样本左右段各 22 位、总长仅 60，说明真实世界存在更短形态。因此 74/58 应作为**收紧用的高置信子集**，不能作为判负条件。
5. `sk-admin-` 的 **74/74 形态没有任何样本支持**（只有 gitleaks 正则放行，其生成器源码注释却写 "Total length: 133"）→ 只实现 58/58。
6. **直接照搬 codex 的互斥规则来降误报**：OpenAI 官方 codex 的凭据代理把 `T3BlbkFJ` 定义为 `credential_watermark`、`OPENAI_API_KEY_MIN_LEN = 51`，并**显式排除 `sk-ant-`、`sk-or-` 前缀**。

伪造示例形状：`sk-EXAMPLE000000000000T3BlbkFJFAKE0000000000000`（左 20 位、右 20 位，明显占位）。

组织/项目标识（`org-` + 24 base62 = 27 字符、`proj_` + 24 base62 = 29 字符）不是密钥，但**常与密钥出现在同一份配置文件里**，可作为强上下文证据。
⚠️ 分隔符方向容易记反：官方源码里组织 ID 用**连字符** `org-`、项目 ID 用**下划线** `proj_`（网上常见的 `org_` 与 `proj-` 写法与官方源码不符）。两者厂商文档从未定义格式，仅由 SDK 录制用例支撑，置信度 C。

### 3.3 Anthropic

| 类型 | 形态 | 总长 |
| --- | --- | --- |
| 控制台 API key（个人 / 服务账号） | `sk-ant-api03-` + `[A-Za-z0-9_-]{93}` + `AA` | **108** |
| Admin API key | `sk-ant-admin01-` + `[A-Za-z0-9_-]{93}` + `AA` | **110** |
| Enterprise 组织 key | `sk-ant-api01-` + 不透明串 | 未公开 |
| Claude Code OAuth | `sk-ant-oat01-` + 不透明串 | 未公开 |
| Workspace key（legacy） | 无独立前缀（属 `sk-ant-api` 命名空间） | 未公开 |
| Webhook 签名密钥 | `whsec_` + base64(32 字节) | 按推算 50 |

两个独立来源互相印证：gitleaks 写作 `sk-ant-api03-[a-zA-Z0-9_\-]{93}AA`，Nosey Parker 写作 `sk-ant-api[0-9]{2}-[a-zA-Z0-9_-]{95}`（95 = 93 + `AA` 两字符）。Nosey Parker 的 `api[0-9]{2}` 泛化更稳妥——**建议按 `api\d{2}` 实现，而不是写死 `api03`**，否则企业版的 `api01` 会漏报。

状态机要点：
- 这是**"前缀 + 定长 + 固定结尾"三条件齐备**的理想目标：误报率极低，不需要任何上下文；
- `admin01` 与 `api03` 共享 `sk-ant-` 前缀且**前缀等长**，trie 必须继续比较到分支字符才能区分；
- 尾部 `AA` **不是校验位**，只是固定结尾（gitleaks 有 `AA`→`BB` 的 false-positive 样本，可据此证伪）；
- 字符集 `[a-zA-Z0-9_-]` 来自**扫描器正则而非厂商文档**（官方只给前缀与结尾），注意它不等价于 `[\w-]`；
- 认证方式官方原文是"sent as a bearer token in the `Authorization` header"，但 **`x-api-key` 仍要保留在上下文关键词里**（trufflehog 用它发验证请求）。

### 3.4 Google / Gemini / Vertex AI

Google 生态里，"LLM key" 与"云 API key"是同一种东西：`AIza` 开头的 API key 既可能用于 Gemini（AI Studio），也可能用于 Maps、Firebase、YouTube Data API。**仅凭字符串无法判定具体产品**，只能判定"这是一把 Google API Key"，产品归属要靠请求域名/header。

| 类型 | 形态 | 说明 |
| --- | --- | --- |
| API Key | `AIza` + `[A-Za-z0-9_-]{35}`（共 39） | **不要把 `AIzaSy` 硬编码成前缀**：它只是常见样本的巧合，trufflehog 的 `AIzaSy[A-Za-z0-9_-]{33}` 写法有漏报风险，gitleaks / Nosey Parker 的 `AIza[…]{35}` 才是安全写法 |
| API Key（新版 `AQ.` 前缀） | `AQ.` + `[A-Za-z0-9_.\-]+` | Google 已在部分账号改用 `AQ.` 前缀（社区多处报告，**无官方格式文档**，长度/分段未确认）。若只实现 `AIza` 会对这批新 key **完全漏报** |
| OAuth Client Secret | `GOCSPX-` + `[A-Za-z0-9_-]{28}` | 共 35 |
| OAuth Client ID | `<12位数字>-<32位字母数字>.apps.googleusercontent.com` | 非秘密 |
| Access Token | `ya29.` + 长 base64url | 短时效 |
| Refresh Token | `1//` + 长 base64url | 长期凭据，**高价值** |
| 服务账号 JSON | `{"type":"service_account","private_key_id":"<40hex>","private_key":"-----BEGIN PRIVATE KEY-----\n…","client_email":"…@….iam.gserviceaccount.com","token_uri":"https://oauth2.googleapis.com/token"}` | 应作为**单个** finding，而不是把 PEM 单独再报一次 |

状态机要点：
- Gemini 的调用头是 `x-goog-api-key: <AIza…>`（AI Studio）或 `Authorization: Bearer <OAuth>`（Vertex AI）。前者是强上下文；
- 服务账号 JSON 的判定不要用正则跨行匹配整个 JSON（大对象会爆回溯），而是**先用 `"type"` + `"service_account"` 触发，再在 JSON 对象边界内查找 `private_key`**；
- `refresh_token` 的 `1//` 前缀容易与普通的 `1//comment`（C++ 注释）混淆 → 必须同时要求后续是长 base64url。

### 3.5 Microsoft Azure

| 类型 | 形态 | 关键 |
| --- | --- | --- |
| Azure OpenAI / AI Services 资源密钥 | 32 位小写 hex | 必须靠 `*.openai.azure.com` / `*.cognitiveservices.azure.com` 或 header `api-key` |
| APIM 订阅密钥 | 32 位 hex | header `Ocp-Apim-Subscription-Key` |
| Entra ID 客户端密钥 | 含 `~` 的约 40 字符串 | 三段式，含 `~` |
| Storage / Cosmos Account Key | 88 位 base64 且以 `==` 结尾 | **离线校验**：base64 解码后必须恰为 64 字节 |
| SAS Token | `sv=…&ss=…&srt=…&sp=…&se=…&sig=<43~44 base64url>` | `sig` 是 HMAC-SHA256 |

状态机要点：
- 32 hex 是**最危险的弱形态**（与 MD5、Datadog API Key、DeepSeek key 同形）。Azure 密钥**必须**依赖上下文，否则一律不告警；
- 88 位 base64 + `==` 是唯一能离线判定的 Azure 形态，优先实现。

### 3.6 AWS Bedrock

| 类型 | 形态 |
| --- | --- |
| 长期 Bedrock API key | `ABSK` + `[A-Za-z0-9+/]{109,269}` + 可选 `{0,2}` 个 `=` |
| 短期 Bedrock API key（bearer） | `bedrock-api-key-` + base64(预签名 URL) |
| Claude Platform on AWS 长期 key | `AEAA` + base64（仅见社区研究，未确认） |
| Claude Platform on AWS 短期 key | `aws-external-anthropic-api-key-` + base64 |
| 常规访问 | SigV4 的 `AKIA…`/`ASIA…` + Secret（见 §5.1） |

**短期 Bedrock token 的精确构造**（来自 AWS 官方 token generator 源码，本文中判据最硬的一条）：

```python
DEFAULT_HOST = "bedrock.amazonaws.com"
AUTH_PREFIX  = "bedrock-api-key-"
TOKEN_VERSION = "&Version=1"
TOKEN_DURATION = 43200          # 12 小时
# token = AUTH_PREFIX + base64(presigned_url_without_scheme + TOKEN_VERSION)
# presigned_url 的 service=bedrock、params={"Action": "CallWithBearerToken"}
```

展开后 base64 的明文恒为：

```
bedrock.amazonaws.com/?Action=CallWithBearerToken&X-Amz-Algorithm=AWS4-HMAC-SHA256
  &X-Amz-Credential=<AKID>/<date>/<region>/bedrock/aws4_request
  &X-Amz-Date=…&X-Amz-Expires=43200&X-Amz-SignedHeaders=host
  &X-Amz-Signature=<64 hex>[&X-Amz-Security-Token=…]&Version=1
```

状态机要点：
- `bedrock-api-key-YmVkcm9jay5hbWF6b25hd3MuY29t` 中的 `YmVkcm9jay5hbWF6b25hd3MuY29t` 就是 `bedrock.amazonaws.com` 的 base64 → 仅凭这 16 字符前缀即可把误报压到接近零，**不需要任何上下文**；
- 若 base64 载荷中出现 `X-Amz-Security-Token`，说明用的是 STS 临时凭证；
- `ABSK` 的长度区间很宽（113~273），且 **AWS 官方文档并未写明该前缀**（只有 gitleaks 与社区研究支撑，C 级）→ 只适合"前缀 + 大范围长度 + 高熵"的弱判定；
- ⚠️ 据社区研究，`ABSK` 的 base64 载荷解码后可读出 AWS 账号 ID 与 IAM 用户名（形如 `BedrockAPIKey-xxxx`）→ 可作为**额外归因信息**，但不要作为判定依据。

### 3.7 其余海外模型厂商：哪些有强前缀，哪些没有

**有强前缀（可直接判定）**：

| 前缀 | 厂商 | 备注 |
| --- | --- | --- |
| `gsk_` | Groq | + 50~54 位 |
| `xai-` | xAI | 官方示例总长 84（`xai-` + 80）；**另有 `xai-token-`（Management key）与 `xai-client-secret.`（临时密钥，作为 `sec-websocket-protocol` 协议值传递，不走 Authorization）** → trie 必须支持这三条并列分支 |
| `r8_` | Replicate | + 37 位 |
| `pplx-` | Perplexity | + 48 位 |
| `sk-or-v1-` | OpenRouter | + 64 位**小写 hex** |
| `hf_` / `api_org_` | HuggingFace | + 34 位（trufflehog 收 `[A-Za-z0-9]`、noseyparker 收 `[a-zA-Z]`，**取并集**） |
| `jina_` | Jina AI | + 60 位 |
| `pa-` | Voyage AI | + 24 位以上 |
| `dapi` | Databricks | + 32 位**小写 hex** + 可选 `-<数字>` 后缀 |
| `pk-lf-` / `sk-lf-` | Langfuse | + UUID（8-4-4-4-12，含 4 个 `-`） |
| `lsv2_pt_` / `lsv2_sk_` | LangSmith | `lsv2_(pt\|sk)_` + **32 hex** + `_` + **10 hex**；另有 legacy `ls__` 前缀 |
| `csk-` | Cerebras | + 24 位以上。⚠️ 与 Kong Konnect PAT `kpat_` 混淆时靠前缀区分 |
| `esct_` | Anyscale | + 20 位以上；**也可能是无前缀裸串** |
| `fw_` | Fireworks AI | + 20 位以上（前缀为 A 级）；同族另有项目密钥 `fpk_` |
| `ak-` / `as-` | Modal | Token ID / Token Secret；**成对时可拼成 `ak-….as-…` 直接作 Bearer** |
| `rpa_` | RunPod | **现行 key 的前缀（A 级）**；旧版 key 是无前缀的大写 UUID 形（见下表） |
| `b10_` | Baseten | **厂商公布了正则**：`b10_` + 8 字符 + `.` + 32 字符 → 最易精确实现的一家 |
| `sk_car_` | Cartesia | + 字母数字 |
| `opk_` | OpenPipe | + 32~80 位 |
| `pl_` | PromptLayer | + 32~64 位 hex。⚠️ `pl_` 过于通用，必须加长度约束 |
| `nk-` | Nomic | + 40 位以上 |
| `key_` | Runway ML | + 48~64 位。⚠️ 极通用，必须加长度约束 |
| `tl_` | Traceloop | + 32~80 位 |
| `flp_` | FriendliAI | + 32 位以上 |
| `sk-helicone-` | Helicone | **官方占位符给出精确布局**：4 组 7 位小写字母数字、组间以 `-` 连接 → 多段前缀，trie 要能跨越 `-` |
| `sk_` | Novita AI | + 32 位以上（是下划线 `sk_`，不是 `sk-`） |
| `lobehub-` | LobeHub | + 32 位以上 |
| `lf_` | Langflow | + 32~80 位。⚠️ 需长度约束 |
| `(app\|dataset)-` | Dify | **恰好 32 位**。⚠️ 前缀极通用，长度必须精确 |
| `pk-` | Portkey | **组织/团队级 API key 有 `pk-` 前缀**（A 级）；虚拟 key 疑为 `sk-`（未验证） |
| `sk-` | LiteLLM proxy | 官方明写 master key "must start with `sk-`" → 自建网关会大量产生 `sk-` 形态，**这是国内/自建场景误报的主要来源之一** |
| `AAAA` | Nebius | + 60 位以上（D 级，社区观察） |

**没有前缀、只能靠上下文的**：

| 厂商 | 形态 | 必需上下文 |
| --- | --- | --- |
| Cohere | 40 位 `[A-Za-z0-9]`，熵 ≥ 4 | `cohere` / `CO_API_KEY` |
| Mistral | **恰好 32 位** `[A-Za-z0-9]` | `mistral` / `MISTRAL_API_KEY`（否则与 MD5 无法区分） |
| Together AI（现行） | **64 位小写 hex** | `together`（官方文档只写 `your_api_key`，未公布格式） |
| Together AI（legacy，已废弃） | 未公布 | — |
| Deepgram | **40 位小写 hex** | `deepgram` |
| AssemblyAI | **32 位小写 hex** | `assemblyai` |
| Stability AI | `sk-` + `[A-Za-z0-9]`（**不含 `-`/`_`**） | `stability` / `STABILITY_API_KEY`（与 OpenAI legacy 都是 `sk-` 字母数字，**必须靠长度与上下文区分**） |
| ElevenLabs（现行） | `sk_` + 小写 hex（v2 探测器口径；厂商未公布格式） | `elevenlabs` / `xi-api-key` |
| ElevenLabs（旧版） | 无前缀 **32 位 hex** | 同上 |
| AI21 | `[A-Za-z0-9]{32,64}` | `ai21` |
| Aleph Alpha | `[A-Za-z0-9_-]{40,}` | `aleph` |
| Baseten（旧 key） | `[A-Za-z0-9._-]{40,}`（官方明说旧 key **不含** `b10_`） | `baseten` |
| Comet ML | `[A-Za-z0-9]{32,100}` | `comet_api_key`。⚠️ **Experiment key 是同形误报陷阱**，必须靠变量名区分 |
| Arize | `[A-Za-z0-9]{40,80}` | `arize`（官方只说明"仅创建时展示一次"） |
| DeepInfra | `[A-Za-z0-9]{40,80}` | `deepinfra` |
| Writer | `[A-Za-z0-9]{40,128}` | `writer`（官方示例只有 `<your-api-key>`，**必须赋值锚点**） |
| Inflection / Lambda Labs / Lepton AI / OctoAI / Reka / SambaNova | `[A-Za-z0-9_\-]{32,}` 量级 | 各自厂商名 / 环境变量名（都必须赋值锚点） |
| IBM watsonx | `[A-Za-z0-9_-]{44}`（IBM Cloud IAM API key，含 `-`/`_`） | `watsonx` |
| Weights & Biases（v2） | `wandb_v1_` + 27 alnum + `_` + 49 位 | `WANDB_API_KEY`（官方探测器带注释与测试夹具） |
| Weights & Biases（legacy） | 40 位**小写 hex** | `wandb` |
| Oracle OCI | Auth Token 无前缀；**指纹是非密钥但极强的上下文**：16 组 2 位小写 hex 以 `:` 连接（`aa:bb:cc:…`） | `ocid1.`、`oci`、`oraclecloud` |
| Snowflake Cortex | 程序化令牌未公布；REST 认证用 **JWT**；账号标识形如 `[A-Za-z]{7}-[A-Za-z0-9]{5}` | `snowflake` |
| Hyperbolic | **key 本身是 JWT**（`eyJ…` 三段） | `hyperbolic`（复用 §2.1 的 JWT 子状态机） |
| Cartesia / PlayHT / Speechmatics 的临时 token | 未验证 | — |

> ⚠️ **Together AI 有两个互不兼容的形态**：现行的 64 位小写 hex 与已废弃的 legacy key；此外 trufflehog 曾提案 `tgp_v1_` 前缀形态。**凡来源冲突的一律两条都收**，用长度与字符集区分。

> 这一类是**漏报的主要来源**：没有前缀就没有可靠的预筛关键词，只能把"变量名/环境变量名/JSON 字段名"也纳入预筛索引（例如把 `MISTRAL_API_KEY` 作为一个可匹配的模式）。

### 3.8 AI 工具链与数据平台

| 产品 | 形态 | 置信度 |
| --- | --- | --- |
| LangSmith | `lsv2_pt_…`（个人 token）、`lsv2_sk_…`（服务 key） | C |
| Langfuse | `pk-lf-<uuid>`、`sk-lf-<uuid>` | C |
| Weights & Biases | `wandb_v1_…` | D |
| Helicone | `sk-helicone-…`（自建/托管两种） | D |
| Portkey | 无公开固定格式 | D |
| LiteLLM | 自建，master key 常用 `sk-` + 自定义串 | D |
| Databricks | `dapi<32hex>[-<digit>]` + workspace 域名 `*.cloud.databricks.com` / `*.azuredatabricks.net` | C |
| Snowflake | 账号标识 `<org>-<account>`；PAT/密钥对无统一前缀 | D |
| IBM watsonx | API key 为 44 位 base64 形态（`SharedAccessKey` 同形） | D |
| Ollama / vLLM / LM Studio | **默认无鉴权**，无 key 格式 | A（"无"本身是事实） |

### 3.9 本章的已知盲区

以下目标**没有查到权威的固定格式**，实现时应标为 D 级或直接不做，避免引入误报：

- **临时/会话类令牌**：ElevenLabs single-use token、Cartesia access token、Deepgram 短时 Token、AssemblyAI Streaming token、Nebius AI Cloud 临时 token、Snowflake Programmatic Access Token、Oracle OCI session/security token、Portkey 虚拟 key、PlayHT 与 Speechmatics 的全部凭据——官方均未公布格式。
- **Databricks service principal OAuth secret**、**Arize Phoenix（自托管）**、**Portkey 虚拟 key**：未查证。
- **AI21 / Aleph Alpha / Baseten（旧 key）/ Comet / Inflection / Lambda / Lepton / OctoAI / Reka / SambaNova / Writer**：只有"长度区间 + 必须上下文"的弱规则，本质上是**变量名匹配**而非格式匹配。

它们的共同特征是：**key 就是一段无前缀的高熵随机串**。对这类目标，唯一可行的策略是"高熵 + 变量名/header 名上下文"，而不是字符串形态。

> 补充：本节的海外厂商形态来自 `pleno-dlp`（618 个检测器）、`trufflehog`（907 个检测器）、`gitleaks`（222 条规则）与 `Nosey Parker`（189 条规则）的交叉比对。**凡两处以上冲突的（xAI、Fireworks、HuggingFace、ElevenLabs、Together、W&B、RunPod），本文一律保留全部形态并标注来源与冲突**——规则表带来源与版本，才能在厂商轮换 key 格式时快速定位漏报原因。

## 4. 大模型厂商密钥格式（国内）

> 国内厂商是开源扫描器覆盖最薄弱的一块。**已核实**：gitleaks（222 条规则）、Nosey Parker（189 条规则）、Yelp detect-secrets（27 个插件）对本章 6 家厂商（DeepSeek / Moonshot / 智谱 / 阿里百炼 / 百度千帆 / 讯飞）**全部零覆盖**；trufflehog（907 个检测器）只覆盖 DeepSeek 与阿里云，且没有任何一家国内厂商在 key 内部嵌入了校验位。
> 因此本章置信度整体低于第 3 章，**凡属扫描器或社区来源的都已标注，凡未获官方确认的都写进"未知与待验证"**。

### 4.1 速查总表

| 厂商/产品 | 凭据类型 | 前缀/首部特征 | 字符集 | 长度 | 结构 | 校验 | 上下文关键词 | 置信度 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| DeepSeek | 平台 API Key | `sk-` | `[a-z0-9]`（有实现放宽为 `[A-Za-z0-9]`） | 3 + 32 = **35**（另有实现允许本体 ≥32） | 单段 | 无校验位 | `DEEPSEEK_API_KEY`、`deepseek`、`api.deepseek.com`、`deepseek-chat` | C |
| Moonshot / Kimi | 平台 API Key | `sk-` | **未知** | **未知** | 单段 | 无 | `MOONSHOT_API_KEY`、`api.moonshot.cn`、`api.moonshot.ai`、`platform.kimi.com` | D（仅前缀 C） |
| 智谱 AI / BigModel / GLM | Open API Key | 无字母前缀，形如 `<id>.<secret>` | 第 1 段 `[a-f0-9]`，第 2 段 `[A-Za-z0-9]` | 32 + 1 + 16 = **49**（分段长度为社区提案值） | **单个 `.` 分隔两段** | 无校验位；可做结构自检 | `ZHIPUAI_API_KEY`、`ZHIPU_API_KEY`、`bigmodel`、`glm`、`zhipu`、`open.bigmodel.cn`、`api.z.ai` | A（结构）/ C（分段长度） |
| 智谱 AI | 派生 JWT（由 `id.secret` 现场签发） | `eyJ` | base64url | 不定长 | 三段 JWT；**header 含 `alg:HS256` + `sign_type:SIGN`** | JWT 结构自校验 + 厂商指纹 | `open.bigmodel.cn`、`api.z.ai` | A |
| 阿里云百炼 | 通用（永久）API Key | `sk-` | `[A-Za-z0-9]`（有实现收窄为 `[a-f0-9]`） | 3 + 32 = 35（本体长度官方未公布） | 单段 | 无 | `DASHSCOPE_API_KEY`、`dashscope`、`qwen`、`dashscope.aliyuncs.com`、`X-DashScope-API-Key` | A（前缀）/ C（长度） |
| 阿里云百炼 | Coding / Token Plan 专属 Key | `sk-sp-` | 未知 | 未知 | 单段 | 无 | `sk-sp-`、`coding.dashscope.aliyuncs.com` | A（前缀） |
| 阿里云百炼 | 临时 API Key（TTL ≤ 1800s） | `st-` | 未知 | 未知 | 单段 | 无 | `st-`、`expire_in_seconds`、`expires_at` | A（前缀） |
| 阿里云（RAM） | AccessKey ID | `LTAI`（大小写敏感） | `[A-Za-z0-9]` | **三个来源冲突**：24 / 21–25 / 16–24；官方仅给掩码 | 单段 | 无 | `LTAI`、`aliyun`、`AccessKeyId`、`ALIBABA_CLOUD_ACCESS_KEY_ID` | A（前缀）/ C（长度） |
| 阿里云（RAM） | AccessKey Secret | 无 | `[a-z0-9]`（gitleaks）/ `[a-zA-Z0-9]`（trufflehog） | 30 | 单段 | 无；**必须与 `LTAI…` 成对** | `AccessKeySecret`、`ALIBABA_CLOUD_ACCESS_KEY_SECRET` | C |
| 百度智能云千帆 | API Key | `bce-v3/ALTAK-` | base62 | 分段长度未确认 | `bce-v3` **/** `ALTAK-<id>` **/** `<secret>` | 无校验位；**双固定 `/` 可确定性解析** | `QIANFAN_API_KEY`、`qianfan.baidubce.com`、`bce-v3`、`ALTAK-` | A（结构）/ C（长度） |
| 百度智能云（IAM） | 经典 AK / SK | 官方未公布 | 未公布 | 未公布 | 单段 | 无 | `AccessKeyId`、`BCE_AK`、`secret_key` | D |
| 讯飞星火 | APIKey / APISecret | 无前缀 | `[a-f0-9]`（示例值，非格式承诺） | **32**（官方原文"均为32位字符串"） | 单段 | 无；**与 MD5 同形** | `api_key`、`APISecret`、`SPARKAI_API_KEY`、`app_id`、`xf-yun.com` | A（长度）/ B（字符集） |
| 讯飞星火 | APPID | 无前缀 | 未公布（官方示例为短数字 `12345`） | 未公布 | 单段 | 无 | `app_id`、`APPID`、`SPARKAI_APP_ID` | D |
| 讯飞星火 | WebSocket `authorization`（派生） | base64 文本，**解码后以 `api_key="` 开头** | base64 | 不定长 | 解码后为 `api_key="…", algorithm="hmac-sha256", headers="host date request-line", signature="…"` | **可离线结构校验**（必须同时含 `algorithm="hmac-sha256"` 与 `api_key="`） | `authorization`、`hmac-sha256`、`host date request-line`、`spark-api.xf-yun.com` | A |
| MiniMax | 新版 API key / Coding Plan key | `sk-api-` / `sk-cp-` | 未知 | 未知 | 单段 | 无 | `MINIMAX_API_KEY`、`MINIMAX_CODING_API_KEY`、`minimaxi.com` | C |
| MiniMax | 旧版 api_key | **是 JWT**（`eyJ…` 三段） | base64url | 不定长 | payload 含 `GroupID`/`SubjectID` | JWT 解码 | `group_id`、`GroupId` | C |
| 硅基流动 / 阶跃 / 零一万物 / 百川 / 商汤 / 魔搭 / 火山方舟 API Key | API key | 多为 `sk-` 或未知 | 未知 | 未知 | — | 无 | 平台域名 + 环境变量名 | D |
| 华为云 ModelArts / 盘古 | AK / SK | 无前缀 | 字母数字 | 20 / 40（社区） | 单段 | 无 | `HW_ACCESS_KEY`、`modelarts`、`pangu` | D |

### 4.2 DeepSeek —— 国内唯一被主流扫描器覆盖的厂商

trufflehog 检测器：`detectors.PrefixRegex([]string{"deepseek"}) + \b(sk-[a-z0-9]{32})\b`。
pleno-dlp 检测器：`\b(sk-[A-Za-z0-9]{32,})\b` + 关键词 `deepseek`。

**两个实现互相矛盾**：一个严格 32 位且仅小写，另一个 ≥32 位且含大写。DeepSeek 官方文档只写"申请 API key"，**从未公布密钥格式**，所以两条规则都只是社区证据。

状态机要点：
- `sk-` + 32 位小写串与大量自建系统同形 → **必须**依赖上下文关键词（`deepseek`、`DEEPSEEK_API_KEY`、`api.deepseek.com`、模型名 `deepseek-chat`/`deepseek-reasoner`）；
- 不能单独产出 DeepSeek finding，而应落入"`sk-` 通用候选池"，由上下文决定归属；
- 在线校验端点：`GET https://api.deepseek.com/user/balance`。

### 4.3 智谱 AI（BigModel / GLM）—— 结构最清晰，且**不是 `sk-` 前缀**

**主密钥**：官方文档与官方 SDK 明确采用 `<id>.<secret>` 的结构（A 级）：

```
[a-f0-9]{32} \. [A-Za-z0-9]{16}
```

- 第 1 段（id）32 位小写 hex，参与 JWT payload；
- 第 2 段（secret）16 位字母数字，作 HMAC 密钥；
- 总长 49；**有且仅有一个 `.`**。

⚠️ 分段长度 32/16 来自 trufflehog issue 的社区提案，**官方文档并未写出这两个数字**（C 级）。实现时建议"结构强约束 + 长度松区间"。

状态机要点：
- "前长后短、前 hex 后 base62"的不对称性判别力很强（自然文本里少有 `32 位 hex + . + 16 位 alnum`）；
- 误报来源是"文件名/版本号"类文本（如 `<32hex>.v1.2`）→ 必须强制两段长度精确、且第二段纯字母数字；
- ⚠️ **不要把智谱 key 写成 `sk-` 前缀**。大量第三方工具用 `sk-...` 当占位符，据此写规则会**全部漏报**；
- 强上下文：`bigmodel`、`glm`、`zhipu`、`open.bigmodel.cn`、`api.z.ai`。

**派生 JWT（高价值指纹）**：调用前 SDK 会用 `id.secret` 现签一个 JWT。其 header 为：

```json
{"alg": "HS256", "sign_type": "SIGN"}
```

因此"JWT header 同时含 `HS256` 与 `sign_type:SIGN`"是**该厂商的强指纹**（A 级，来自官方 SDK 源码）——如果你的状态机已经实现了 JWT 子状态机，加上这一条即可把智谱与其它所有 JWT 签发方区分开。

### 4.4 阿里云百炼（DashScope）—— **三个前缀，必须最长匹配优先**

官方错误码文档明确"阿里云百炼的 API Key 以 `sk-` 开头"，并另外出现两种专用前缀。三套前缀必须**按长度降序**匹配：

| 前缀 | 用途 | 可用信息 |
| --- | --- | --- |
| `sk-sp-` | Coding Plan / Token Plan 团队版专属 | 仅前缀（A）；长度未知 |
| `sk-` | 通用（永久）API Key | 前缀 A；本体 32 位（pleno-dlp，C） |
| `st-` | 临时 API Key（TTL ≤ 1800s） | 仅前缀（A） |

状态机陷阱：如果 trie 里先匹配 `sk-`，`sk-sp-…` 会被错误吞掉，导致"Coding Plan 专属 Key 被误判为通用 Key"。**必须最长前缀优先**。

> 另有一条 trufflehog issue 主张"百炼 key 是无 `sk-` 前缀的纯 32 位 hex"，与官方文档（明确 `sk-` 前缀）**直接冲突，不可采信**。

### 4.5 百度智能云千帆 —— 唯一带**两个固定分隔符**的国内格式

官方两个来源（认证鉴权文档的掩码示例、API Key 管理接口的解码响应示例）确认结构：

```
Authorization: Bearer bce-v3/ALTAK-<20 位 AccessKeyId>/<签名>
```

- 固定首部 `bce-v3`，固定 `ALTAK-`（注意是字母 **L**，不要与阿里云 `LTAI` 混淆）；
- 两处 `/` 分隔三段；
- 官方唯一"完整"示例中的 AK 段为 20 字符；另有解码示例给出 40 字符且含非 hex 字母 `j`，**高度疑似文档合成串** → 分段长度一律按 C 级处理，**只锚定 `bce-v3/ALTAK-` 与两处 `/`**，两段用宽松 base62 区间。

状态机要点：这是本章**唯一可以"锚点即判定"**的格式（强锚点 `bce-v3/ALTAK-`，无需关键词辅助）。同时注意：百度把 AccessKeyId 暴露在签名头里，**即便签名已过期，AccessKeyId 本身也是敏感信息**。

百度 IAM 的经典 AK/SK **格式完全未知**（官方只说明用途），置信度 D，不建议进 DFA。

### 4.6 讯飞星火 —— 三元组 + 一个可离线校验的派生串

| 凭据 | 官方可用信息 |
| --- | --- |
| APPID | 官方示例是 `12345` 这类短数字串，**无格式承诺**（D） |
| APIKey | 官方原文"均为32位字符串"（**长度 A**）；文档示例值是 32 位小写 hex，但那是示例值而非格式承诺（字符集 B/C） |
| APISecret | 同上 |

**派生鉴权串（A 级，可离线判定）**：WebSocket 的 `authorization` 是一个 base64 文本，解码后为：

```
api_key="<APIKey>", algorithm="hmac-sha256", headers="host date request-line", signature="<base64 签名>"
```

状态机要点：
- 32 位 hex 与 MD5/UUID 去连字符形式完全同形，**任何单串匹配都是高误报**；
- 正确做法是**组合规则**：同一片段内出现 `app_id` 且（`api_key` 或 `apisecret`）才成组上报；APPID 单独出现不上报；
- 唯一能离线判定的形态是上面那个 base64 鉴权串（要求解码后同时含 `algorithm="hmac-sha256"` 与 `api_key="`）。

### 4.7 MiniMax、Moonshot 与其余

| 厂商 | 关键点 |
| --- | --- |
| **MiniMax** | 新版 key 前缀 `sk-api-`（标准 API）与 `sk-cp-`（Coding Plan）；**两种不可互用**，工具需按前缀分流（C）。旧版鉴权用 `group_id` + **JWT 形态的 api_key**——若状态机已实现 JWT 子状态机，这类 key 会被自动捕获，再靠 payload 的 `GroupID`/`SubjectID` 归类为 MiniMax。 |
| **Moonshot / Kimi** | **只能确认 `sk-` 前缀**（来自开源实现的前置校验与官方环境变量名 `MOONSHOT_API_KEY`）。网络流传的"本体 48 位"说法**未找到任何官方、SDK 或扫描器来源**，本文不采信（D）。建议并入"`sk-` 通用候选池"，用 `MOONSHOT_API_KEY`、`api.moonshot.cn`、`api.moonshot.ai`、`platform.kimi.com` 消歧。 |
| **硅基流动 SiliconFlow** | 使用 `sk-` 前缀（D）。与 DeepSeek 的 `sk-` + 32 小写不同，其 key 更长且含大小写混合，**长度是最主要区分手段**。 |
| **阶跃星辰 / 零一万物 / 百川 / 商汤 / 昆仑万维 / 面壁 / Gitee AI / 302.AI / AiHubMix** | 均未查到权威格式，且多数为无前缀高熵串 → 一律 D 级，只做上下文驱动的线索。 |
| **火山引擎方舟 API Key（新版）** | 方舟同时支持 AK/SK 签名（见 §5.4，`AKLT…` 为 A 级）与 API Key 方式；**后者格式未查到权威来源**（D）。 |
| **华为云 ModelArts / 盘古** | 走华为云统一 AK/SK 体系（AK 20 位、SK 40 位，社区来源，D），上下文 `HW_ACCESS_KEY`/`HW_SECRET_KEY`/`modelarts`。 |
| **魔搭 ModelScope** | SDK token 形态未公开；依赖平台域名上下文（D）。 |

### 4.8 国内厂商的共性陷阱（状态机设计者必读）

1. **`sk-` 前缀被滥用**。DeepSeek、Moonshot、硅基流动、DashScope、阶跃、大量聚合网关全用 `sk-`，且 DashScope 内部还有 `sk-` / `sk-sp-` 两级。**前缀本身已失去区分能力**，必须靠"长度 + 字符集 + 域名/环境变量名"联合判定，并严格执行最长前缀优先。
2. **纯 hex 泛滥**。讯飞三元组、智谱第一段、微信 AppSecret、极光 AppKey、高德 Key 都是纯 hex，与 MD5/SHA 片段同形。**没有上下文的纯 hex 不应告警。**
3. **没有任何一家在 key 内嵌校验位**。本文核查的 6 家厂商（DeepSeek / Moonshot / 智谱 / DashScope / 千帆 / 讯飞）**全部没有** CRC32 / Luhn / 校验和类算法。因此国内场景**不能指望"离线证伪"**，只能靠：
   - 结构锚点（`bce-v3/ALTAK-`、`sk-sp-`、`cli_`）；
   - 可解码结构（智谱 JWT、讯飞 base64 鉴权串、MiniMax JWT）；
   - 上下文组合（变量名成组出现）。
4. **文档缺失是常态**。多数厂商只在控制台展示 key，不公布格式。规则表必须支持 **D 级"上下文规则"**：匹配的不是 key 本身，而是 `变量名 + 高熵值` 的组合。
5. **同一厂商多种鉴权并存**（AK/SK 签名 + API Key + 临时 Key + 兼容模式），必须按"厂商 → 多凭据类型"建模。
6. **分隔符驱动的状态机比"字符类游程"更贴合国内形态**：智谱的 `<id>.<secret>`、百度的 `bce-v3/ALTAK-…/…`、腾讯的 `AKID…` 都说明这一点。
7. **扫描器缺口即是漏报风险**：本章 6 家厂商在三大主流扫描器中零覆盖，意味着"照抄开源规则"必然漏掉这些厂商。规则表必须自带厂商维度的覆盖清单。

### 4.9 未知与待验证（明确列出，避免实现时过度自信）

- **DeepSeek 精确长度与字符集**：两个开源 detector 互相矛盾，官方文档未公布 → C。
- **Moonshot 长度与字符集**：完全未知；社区"48 位"说法无来源 → D。Kimi Code 订阅 Key 与 Moonshot 平台 Key 是否同格式亦未知。
- **智谱两段长度 32/16**：仅社区提案 → C。新版是否存在单段 `sk-` 形式**无任何官方依据**，本文不采信。
- **DashScope `sk-` 本体长度/字符集**：官方只确认前缀；`sk-sp-` / `st-` 的后续形态完全未知 → C/D。
- **阿里云 AccessKey ID 长度**：gitleaks 24 位、trufflehog 21–25 位、pleno-dlp 16–24 位，官方仅给掩码 → 建议 `LTAI` + 16..24 宽松区间；Secret 的 30 位与大小写规则同样只有扫描器来源 → C。
- **百度千帆分段长度**：官方"完整"示例疑似合成串 → C；IAM 经典 AK/SK 格式完全未知 → D。
- **讯飞 APPID 长度/字符集**：官方未公布 → D；APIKey/APISecret 的 hex 字符集只有示例值支撑 → B/C。
- **是否存在校验位**：未发现任何一家使用校验位（既无官方说明也无扫描器实现）。

---

## 5. 云与基础设施凭据

### 5.1 AWS

| 凭据 | 形态 | 判定 |
| --- | --- | --- |
| Access Key ID | `(A3T[A-Z0-9]\|AKIA\|ASIA\|ABIA\|ACCA)[A-Z0-9]{16}` | 前 4 位是关键：`AKIA`=长期用户、`ASIA`=临时(STS)、`A3T`/`ABIA`/`ACCA` 为其它类型；后 16 位**建议用 `[A-Z0-9]`**（gitleaks 收窄为 base32 `[A-Z2-7]`，但 AWS 官方只公布了前缀表、未声明后 16 位字符集，用 base32 有漏报风险） |
| Secret Access Key | `[A-Za-z0-9/+=]{40}` | **纯 40 位 base64 字符集**，与 git SHA-1（40 hex）不同：SHA-1 只含 `0-9a-f` |
| Session Token | 长 base64（数百至数千字符），常见以 `FwoGZXIvYXdzE` 开头 | 需与 `AKIA`/`ASIA` 配对；单独出现时置信度低 |
| Account ID | 12 位数字（`[0-9]{4}-?[0-9]{4}-?[0-9]{4}`） | 非秘密，但是强上下文（说明这段配置是真的 AWS 配置） |
| MWS Auth Token | `amzn\.mws\.[0-9a-f]{8}-[0-9a-f]{4}-...` | 强前缀 |
| ARN | `arn:(aws\|aws-cn\|aws-us-gov):<service>:<region>:<account>:<resource>` | 非秘密，用作上下文 |
| AppSync API Key | `da2-[a-z0-9]{26}` | 强前缀 |

状态机要点：
- **配对检测**是 AWS 的核心策略：`AKIA…`（20 位）+ 40 位 base64 Secret 出现在同一文件/同一请求时，两者都提升为 Critical；单独出现时降级。
- 判别 40 位串是 Secret 还是 git SHA：**看字符集**。`[A-Za-z0-9/+=]` 含大写/`/`/`+`/`=` 才可能是 Secret；纯 `[0-9a-f]` 一律当 SHA/哈希处理。

### 5.2 Google Cloud

与 §3.4 同源，这里补充云侧凭据：

| 凭据 | 形态 |
| --- | --- |
| 服务账号 JSON | `{"type":"service_account","private_key_id":"<40hex>","private_key":"-----BEGIN PRIVATE KEY-----\n…","client_email":"…@….iam.gserviceaccount.com","client_id":"<21位数字>","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token"}` |
| 服务账号 PEM | JSON 内嵌的 PKCS#8 `-----BEGIN PRIVATE KEY-----`；**绝不能**把 JSON 里的 PEM 再单独报一次 |
| GCS 桶域名 | `<bucket>.storage.googleapis.com`（非秘密，上下文） |
| Workload Identity | 无静态密钥；只需识别 `GOOGLE_APPLICATION_CREDENTIALS` 指向的文件 |

### 5.3 Azure（云侧补充）

| 凭据 | 形态 |
| --- | --- |
| Storage Account Key | 88 位 base64 且以 `==` 结尾（base64 解码恰为 64 字节）。**Purview 标注 `Checksum = Yes` 但算法未公开** → 只实现"长度 + `==` + 解码后 64 字节"三重离线判定 |
| Storage 连接串 | `DefaultEndpointsProtocol=https;AccountName=<name>;AccountKey=<88 base64>;EndpointSuffix=core.windows.net` |
| Service SAS | 必需参数 `sv` + `sig`，另有 `sp`+`se`/`st`；`sr ∈ {b,bv,bs,c,d,f,s}`；`sp` 是 `racwdxltmeop` 的固定顺序子序列；`sig` 为 **base64**（HMAC-SHA256，约 44 字符，URL 编码后 44~46）——与 AWS 的 64 位 hex 签名形成鲜明对照 |
| App Configuration 连接串 | `https://<name>.azconfig.io; Id=<4>-<2>-<2>:<18~22 位 base64>; Secret=<36~50 位 base64>=` |
| Service Bus / Event Hub | `Endpoint=sb://…;SharedAccessKeyName=…;SharedAccessKey=<44 位 base64>=` |
| Cosmos DB | `AccountEndpoint=https://….documents.azure.com:443/;AccountKey=<88 base64>==` |
| DevOps PAT | 含 `AZDO` 的 76+4 位串 `[a-zA-Z0-9]{76}AZDO[a-zA-Z0-9]{4}`；或 52 位小写 hex 配 `ADO_PAT` 上下文 |
| SAS Token | `sv=…&ss=…&srt=…&sp=…&se=…&sig=<43~44 位 base64url>` |

### 5.4 国内云厂商

| 厂商 | 凭据 | 形态 | 置信度 |
| --- | --- | --- | --- |
| 阿里云 | AccessKeyId | `LTAI` + `[a-z0-9]{20}`（共 24） | C |
| 阿里云 | AccessKeySecret | 30 位 `[a-z0-9]`，**必须**有 `alibaba`/`aliyun` 上下文 | C |
| 阿里云 | OSS 签名 URL | `OSSAccessKeyId=LTAI…&Expires=…&Signature=<urlencoded base64>` | B |
| 腾讯云 | SecretId | `AKID` + 32 位字母数字（共 36） | A |
| 腾讯云 | SecretKey | 32 位字母数字（必须上下文） | C |
| 腾讯云 | COS 签名 URL | `q-sign-algorithm=sha1&q-ak=AKID…&q-sign-time=…&q-signature=<40hex>` | B |
| 华为云 | AK / SK | 20 位 / 40 位字母数字（必须上下文） | D |
| 百度智能云 | AccessKeyId / SecretKey | AK 20 位 / SK 32 位；签名头 `bce-v3/ALTAK-<AK>/<sig>` | A（头部）/ C（本体） |
| 火山引擎 | AccessKeyId | `AKLT[A-Za-z0-9\-_]+`，总长 20~32 | A |
| 火山引擎 | SecretAccessKey | 无固定前缀 | D |
| 七牛云 | AK / SK | 40 位 / 40 位字母数字；上传凭证 `uptoken` 是 base64url 的 JSON+HMAC | D |
| 又拍云 | 操作员密码 | 32 位 hex | D |
| MinIO | Access Key / Secret Key | 自定义（默认 `minioadmin`） | D |

> **"云厂商前缀四件套"**：`AKIA`(AWS) / `LTAI`(阿里云) / `AKID`(腾讯云) / `AKLT`(火山引擎)。这四个前缀应当放在同一个 trie 分支下做**并行前缀匹配**，命中后统一进入"AK/SK 配对"逻辑。

### 5.5 容器与密钥管理

| 凭据 | 形态 | 备注 |
| --- | --- | --- |
| Kubernetes ServiceAccount JWT | `eyJ…` 三段 | 解码 payload 含 `"iss":"kubernetes/serviceaccount"`、`"kubernetes.io/serviceaccount/namespace"`、`"kubernetes.io/serviceaccount/service-account.name"` → **JWT 子状态机可直接归类** |
| K8s Bootstrap Token | `[a-z0-9]{6}\.[a-z0-9]{16}` | 需 `token`/`bootstrap` 上下文 |
| kubeconfig | `client-key-data: <base64 PEM>`、`client-certificate-data:`、`token:` | base64 解码后是 PEM → 用 §2.4 的套壳前缀判定 |
| Kubernetes Secret YAML | `kind: Secret` + `data:` 下的 base64 值 | 结构性规则 |
| Vault（≥1.10） | `hvs.` + `[A-Za-z0-9]{24,130}`（服务）、`hvb.` + `[A-Za-z0-9_-]{24,500}`（批量）、`hvr.` + 24~130（恢复） | 新旧前缀并存 |
| Vault（<1.10） | `s.` / `b.` / `r.` + 24~500 | **必须有 `vault`/`token`/`hashicorp` 上下文**，否则 `s.` 前缀毫无区分度 |
| Vault Unseal Key | 44 位 base64，配 `unseal` 上下文 | — |
| WireGuard | `PrivateKey = [A-Za-z0-9+/]{43}=`、`PresharedKey = …` | 解码须为 32 字节 |
| Helm/Kustomize secrets | 无独立格式 | 同上，走 YAML/base64 路径 |

### 5.6 CI/CD 与制品库

| 凭据 | 形态 |
| --- | --- |
| JFrog Artifactory API Key | `AKCp[A-Za-z0-9]{69}`（熵 ≥ 4.5）；新版 token 为 `cmVmdGtu…` 开头的 base64 |
| JFrog 旧式 | 上下文 `artifactory` + 73 位 `[a-z0-9]` |
| Terraform Cloud | `[a-z0-9]{14}.atlasv1.[a-z0-9\-_=]{60,70}` |
| Jenkins | `[0-9a-f]{32,36}` 或 UUID，配 `jenkins` 上下文；安装密码出现在 `Please use the following password to proceed to installation:` 之后 |
| TeamCity | `eyJ0eXAiOiAiVENWMiJ9.<36>.<48>` —— **本质是 JWT**（header 解出 `{"typ": "TCV2"}`） |
| CircleCI | 40 位 hex（个人 token），需上下文 |
| Docker Hub PAT | `dckr_pat_[a-zA-Z0-9_-]{27}` |
| Harbor | 无统一前缀（自建），依赖 URL/header |
| Dependency-Track | `odt_[A-Za-z0-9]{32,255}` |
| CodeClimate | `[a-f0-9]{64}`，配 `CODECLIMATE_REPO_TOKEN`/`CC_TEST_REPORTER_ID` |
| Doppler | `dp.ct.` / `dp.pt.` / `dp.st.` / `dp.sa.` / `dp.scim.` / `dp.audit.` + 40~44 位字母数字 |
| Sauce Labs | UUID，配 `sauce` 上下文 |

### 5.7 其它 PaaS

| 凭据 | 形态 |
| --- | --- |
| DigitalOcean | `dop_v1_[a-f0-9]{64}`（PAT）、`doo_v1_[a-f0-9]{64}`（OAuth）、`dor_v1_[a-f0-9]{64}`（refresh） |
| Heroku | 旧版：UUID（配 `heroku` 上下文）；新版：`HRKU-AA[0-9A-Za-z_-]{58}` |
| Cloudflare | API Token 40 位 `[a-z0-9_-]`；Global API Key 37 位 `[a-f0-9]`；Origin CA Key `v1.0-[a-f0-9]{24}-[a-f0-9]{146}` |
| Netlify | **`nfp_` + 40 位字母数字**（Personal Access Token，强前缀） |
| Vercel | 24 位字母数字（需 `vercel[_\-]?token` 上下文） |
| Fly.io | **`fm1_` / `fm2_` + 50 位以上**（强前缀） |
| Render | **`rnd_` + 14 位字母数字**（强前缀） |
| Railway | UUID（需 `railway` 上下文） |
| ngrok | `2[A-Za-z0-9]{20,40}_[A-Za-z0-9]{20,40}`（需 `ngrok` 上下文） |
| Tailscale | **`tskey-<类型>-<6~32>_<16~96>`**（如 `tskey-auth-…`、`tskey-api-…`，强前缀） |
| PlanetScale | **`pscale_oauth_` / `pscale_tkn_` + 32~64 位**（强前缀） |
| Neon | **`neon_` + 32~80 位**（强前缀） |
| Upstash Redis | 无前缀，靠 `UPSTASH_REDIS_REST_TOKEN` 环境变量名 + 50~128 位字母数字 |
| Turso | 无前缀，靠 `turso` 上下文 |

---

## 6. 常见业务系统 Token（研发平台 / IM / SaaS / 支付）

### 6.1 研发平台

#### GitHub

| 类型 | 前缀 | 正文 | 总长 | 校验 |
| --- | --- | --- | --- | --- |
| Personal Access Token (classic) | `ghp_` | 30 随机 + 6 校验 | 40 | **CRC32 + base62**（§8.2） |
| OAuth Access Token | `gho_` | 同上 | 40 | 同上 |
| App 用户/安装令牌 | `ghu_` / `ghs_` | 同上 | 40 | 同上 |
| Refresh Token | `ghr_` | **来源冲突**：gitleaks 记 36，Nosey Parker 记 76 | 40 / 80 | 待确认 |
| 细粒度 PAT | `github_pat_` | 22 + `_` + 59 | 86 | 无 CRC32 |
| App JWT | `eyJ…` 三段 | RS256，`iss` 为 app_id | 可变 | JWT 解码 |
| Webhook Secret | 任意 | 与仓库配置中的值比对 | — | 无 |

> **`ghr_` 长度两个权威开源规则不一致**（gitleaks `{36}` vs Nosey Parker `{76}`）。实现时应两者都接受并标注来源冲突——这正是"规则表要带来源与版本"的价值。

#### GitLab

| 类型 | 前缀 | 载荷长度 |
| --- | --- | --- |
| Personal Access Token | `glpat-` | 20（旧）/ 27~300 + `.` + 9（路由化新格式） |
| OAuth Application Secret | `gloas-` | 64 |
| Deploy Token | `gldt-` | 20 |
| Runner Authentication Token | `glrt-` | 20（旧）/ `t<数字>_` + 27~300 + `.` + 9（路由化） |
| Runner Registration Token | `GR1348941` | 20 |
| Pipeline Trigger Token | `glptt-` | 40 hex |
| CI/CD Job Token | `glcbt-` | `[0-9a-zA-Z]{1,5}_` + 20 |
| Feature Flag Client Token | `glffct-` | 20 |
| Feed Token | `glft-` | 20 |
| Incoming Mail Token | `glimt-` | 25 |
| Kubernetes Agent Token | `glagent-` | 50 |
| SCIM Token | `glsoat-` | 20 |
| Session Cookie | `_gitlab_session=` | 32 位 `[0-9a-z]` |

**路由化 token 的校验后缀**：新格式形如 `<payload>.<2 位><7 位>`，社区实现认为是 **base32 编码的 CRC32**（7 个 base32 字符恰好容纳 32 位）。⚠️ 该算法**不在 GitLab 官方文档中**（官方 token 总览页只列前缀），因此标为 C 级——可用作加分项，不要用作唯一判据。

#### 其它

| 平台 | 形态 |
| --- | --- |
| Bitbucket App Password | `ATBB[a-zA-Z0-9]{32}` |
| Atlassian Cloud API Token | `ATATT3xFfGF0` + `[a-zA-Z0-9_-]{20,200}` + `=[0-9A-F]{8}` —— **魔法前缀 + 结尾 hex 校验**，可离线强判 |
| Gitee | 40 位 hex（需上下文） |
| Gitalk | clientID 20 hex + clientSecret 40 hex（成对出现） |

### 6.2 包管理与制品库

| 平台 | 形态 | 长度/字符集 |
| --- | --- | --- |
| npm | `npm_` + `[A-Za-z0-9]{36}` | 40 |
| PyPI | `pypi-AgEIcHlwaS5vcmc` + `[a-zA-Z0-9_-]{50,1000}` | macaroon |
| NuGet | `oy2` + `[a-z0-9]{43}` | 46 |
| crates.io | `cio` + `[a-zA-Z0-9]{32}` | 35 |
| RubyGems | `rubygems_` + `[a-f0-9]{48}` | 57 |
| Docker Hub | `dckr_pat_` + `[a-zA-Z0-9_-]{27}` | 36 |

**前缀冲突提醒**：`pypi-` 后的 `AgEIcHlwaS5vcmc` 是 macaroon 头部的固定 base64；`cio` 与普通英文词首冲突（需边界 + 长度），`oy2`/`npm_`/`dckr_pat_` 则相对安全。

### 6.3 IM 与协作

| 平台 | 形态 |
| --- | --- |
| Slack Bot Token | `xoxb-<10~12 位数字>-<10~14 位数字>-<23~25 位字母数字>`；旧格式 `xoxb-<10~13 位数字>-<24 位字母数字>` |
| Slack User Token | `xoxp-<12 位数字>x3-<32 位 hex>` |
| Slack App Token | `xapp-<12 位数字>-<24 位字母数字或其他>` |
| Slack 配置令牌 | `xoxe.xox[bp]-<数字>-<163~166 位大写字母数字>`、`xoxe-<数字>-<146 位大写字母数字>` |
| Slack Webhook | `https://hooks.slack.com/services/T<8 位>/B<8~12 位>/<24 位>` |
| Discord Bot Token | `base64(user_id).base64(timestamp).<27~38 位>` → **第一段解码必须是纯数字** |
| Discord Client Secret | 32 位 `[a-z0-9=_\-]`（配 `discord` 上下文） |
| Microsoft Teams Webhook | `https://<tenant>.webhook.office.com/webhookb2/<GUID>@<GUID>/IncomingWebhook/<hex>/<GUID>` |
| Telegram Bot Token | `<5~16 位数字>:AA[a-zA-Z0-9_-]{32,33}` |
| Zoom | JWT（`typ: JWT`，`iss` 为 API Key） |
| Notion | `ntn_` + 11 位数字 + 35 位字母数字；旧版 `secret_` + 43 位 |
| Airtable | `pat` + 14 位字母数字 + `.` + 64 位 hex；旧版 `key` + 14 位 |
| Figma | `figd_` + 40 位 `[A-Za-z0-9_-]`（Nosey Parker 用 UUID 变体 `[0-9a-f]{4}-…` 匹配） |
| Linear | `lin_api_` + `[a-z0-9]{40}` |
| Dropbox | 短时效 `sl.[a-z0-9\-=_]{135}`；长时效 `[a-z0-9]{11}AAAAAAAAAA[a-z0-9\-_=]{43}` —— **中间的 10 个 `A` 是显式标记** |
| Salesforce | `00<13 位字母数字>!<96 位>` —— `!` 分隔符非常独特 |
| HubSpot | 旧版 UUID；新版 `pat-<region>-<uuid>` |
| Postman | `PMAK-<24 位 hex>-<34 位 hex>` |
| Mapbox | `pk.` / `sk.` / `tk.` + 两段 base64ish（公钥/私钥/临时） |
| Kagi | `[a-zA-Z0-9_-]{11}\.[a-zA-Z0-9_-]{43}`（**必须**有 `kagi` 上下文） |

### 6.4 支付、短信与邮件

| 平台 | 形态 |
| --- | --- |
| Stripe | `(sk\|rk)_(test\|live\|prod)_[a-zA-Z0-9]{10,99}`；Webhook Secret `whsec_<base64>` |
| Square | `EAAA…` 或 `sq0atp-<22 位>`（访问令牌）、`sq0csp-<43 位>`（OAuth Secret） |
| PayPal | `access_token$<env>$<hex>`（Braintree 风格） |
| Plaid | `access-(sandbox\|development\|production)-<UUID>`（强前缀）；client_id 24 位、secret 30 位（需上下文） |
| Razorpay | `rzp_(test\|live)_[A-Za-z0-9]{14}` |
| Twilio | Account SID `AC[0-9a-f]{32}`；API Key `SK[0-9a-f]{32}`（大小写两种写法都有）；Auth Token 32 位 hex |
| SendGrid | `SG.<22 位>` + `.` + `<43 位>` |
| Mailgun | `key-<32 位 hex>`、`pubkey-<32 位 hex>`、签名密钥 `<32>-<8>-<8>` |
| Postmark | UUID + `postmark` 上下文 |
| Mailchimp | `<32 位 hex>-us<1~3 位数字>` —— **`-usN` 后缀是判别关键** |
| Resend | `re_` + 长串（需上下文） |
| Amazon SES SMTP | 用户名 = AccessKeyId，密码 = 44 位 base64（需上下文） |

### 6.5 可观测、安全与身份

| 平台 | 形态 |
| --- | --- |
| Datadog | API Key 32 位 hex；APP Key 40 位 hex；均需 `datadog` 上下文 |
| New Relic | `NRAK-<27 位小写字母数字>`（User API Key）、`NRAA-<27 位 hex>`、`NRII-`/`NRIQ-` 32 位、`NRRA-<42 位 hex>`、`px-api-<UUID>`、`px-dep-<UUID>`；License Key：`[a-z0-9]{6}[a-f0-9]{30}nral`（**结尾固定 `nral`**，可离线强判） |
| Sentry | DSN `https://<32 位 hex>@<host>/<project id>`（**结构极强，可离线判定**）；Access Token 64 位 hex（需上下文）；Org Token `sntrys_<base64>_<43 位 base64>`；User Token `sntryu_<64 位 hex>` |
| Intercom | `dG9rOg` + base64（`dG9rOg` 是 `tok:` 的 base64，**魔法前缀**） |
| Grafana | API Key `eyJrIjoi…`（base64 of `{"k":`）；Service Account `glsa_<32 位>_<8 位 hex>`；Cloud `glc_eyJrIjoi…` |
| Dynatrace | `dt0<1 位字母><2 位数字>.<24 位大写字母数字>.<64 位大写字母数字>` |
| Splunk | JWT（`iss: splunk`）或 HEC token（UUID，需上下文） |
| Elastic | 与 API key 体系混用，多为 base64 `id:key` |
| Okta | `00[a-z0-9_-]{39}[a-z0-9_]`（共 42），**必须**有 `okta`/`ssws` 上下文 |
| Auth0 | Client Secret 64 位字母数字；Client ID 32 位；需 `AUTH0_CLIENT_SECRET` 上下文 |
| Clerk | `sk_test_` / `sk_live_` + base64（需上下文） |
| Doppler | 见 §5.6 |
| LaunchDarkly | `api-` / `sdk-` + UUID（需上下文） |
| PagerDuty | `u+` / `pdus_` + 20 位，或 20 位字母数字（需上下文） |
| PostHog / Segment | Segment 公钥 API `sgp_<64 位字母数字>`（**与 Sourcegraph 前缀冲突**） |
| Algolia | 32 位小写 hex（需上下文） |

### 6.6 数据库与 BaaS

| 平台 | 形态 |
| --- | --- |
| Supabase | `sbp_` + 40 位 hex（Personal Access Token）；anon/service_role key 是 **JWT**（见 §2.1，靠 payload 的 `role` 区分） |
| Firebase | API Key = Google `AIza…`；FCM 旧版服务端密钥 `AAAA…`；服务账号 = Google 服务账号 JSON |
| MongoDB Atlas | 公钥 8 位小写字母数字 / 私钥 UUID（需上下文） |
| PlanetScale | **`pscale_oauth_` / `pscale_tkn_`** + 32~64 位字母数字（强前缀） |
| Neon | **`neon_`** + 32~80 位字母数字（强前缀） |
| Turso | 无前缀，`[A-Za-z0-9._-]{40,}` + `turso` 上下文 |
| Upstash Redis | 无前缀，靠环境变量名 `UPSTASH_REDIS_REST_TOKEN` + 50~128 位 |
| Pinecone | **`pcsk_`** + `[A-Za-z0-9_]{40,}`（强前缀） |
| Chroma Cloud | **`ck-`** + `[A-Za-z0-9]{40,}`（强前缀） |
| Milvus / Zilliz | **`db_`** + `[A-Za-z0-9_\-]{32,}`（强前缀，但 `db_` 较通用，需长度约束） |
| Weaviate | 64 位 `[A-Za-z0-9_\-]` + `weaviate.cloud` 域名上下文 |
| Qdrant | `[A-Za-z0-9_-]{40,256}` + `qdrant` 上下文 |
| Typesense | 32 位字母数字 + `typesense` 上下文 |
| Meilisearch | `[A-Za-z0-9_\-]{32,}` + `meilisearch`/`meili` 上下文 |
| ClickHouse Cloud | `[A-Za-z0-9]{32}` 或 `chc_` 前缀，+ `clickhouse.cloud` 上下文 |
| Hasura | 64 位字母数字 + `hasura` + `admin secret` 上下文 |
| MongoDB Atlas | 公钥 8 位小写字母 + 私钥 UUID，需 `atlas`/`mongodb` 上下文 |

### 6.7 **前缀冲突专题**（状态机必须显式处理）

真实世界里同一个前缀被多家使用，这是误报的主要来源：

| 前缀 | 冲突方 | 消歧手段 |
| --- | --- | --- |
| `sgp_` | Segment（64 位 `[a-zA-Z0-9]`） vs Sourcegraph（40 位 hex，可含 `_` 分段） | **长度 + 字符集**：64 vs 40/56 |
| `sk-` | OpenAI / Anthropic / DeepSeek / Moonshot / Stability / 硅基流动 / LiteLLM / 无数自建系统 | **前缀更长优先 + 长度 + 字符集 + 魔法串** |
| `sk_live_` / `sk_test_` | Stripe / Clerk / 其它支付 | 后续长度与字符集；域名上下文 |
| `gh` | `ghp_`/`gho_`/`ghu_`/`ghs_`/`ghr_`/`gh` 作为普通词 | 必须按下划线后缀分派 |
| `xox` | Slack 全家族 | 第 4 个字符（`b`/`p`/`a`/`r`/`s`/`e`）分派 |
| `dapi` | Databricks PAT | 后续必须是 32 位 hex |
| `AK` | `AKIA`(AWS) / `AKID`(腾讯) / `AKLT`(火山) / `AKCp`(Artifactory) | 第 3~4 位 |
| `s.` / `b.` / `r.` | Vault 旧前缀 vs 英文缩写/文件名 | **必须**有 `vault` 上下文 |
| `cio` | crates.io vs 普通词（如 "scion"） | 词边界 + 精确长度 35 |
| `AAAA` | **Nebius**（`AAAA` + 60 位以上） vs **FCM 旧版服务端密钥**（`AAAA<7 位>:APA91b<134~200 位>`） | 看 `:` 与 `APA91b` 标记；有即 FCM |
| `pat` | Airtable（`pat<14 位>.<64 hex>`）、HubSpot（`pat-<na1-><36 位>`）、GitHub 细粒度 PAT（`github_pat_`） | 分隔符 + 长度 |
| `sk_` | Novita AI（`sk_`+32 位以上） vs ElevenLabs 社区形态（`sk_`+48 hex） vs OpenAI 兼容自建网关 | 字符集（hex vs 字母数字）+ 上下文 |
| `key_` | Runway ML（`key_`+48~64）vs 无数自建系统 | **必须**加长度约束 + 上下文 |
| `pl_` | PromptLayer（`pl_`+32~64 hex） | 必须加长度约束；否则与普通标识符冲突 |
| `lf_` | Langflow（`lf_`+32~80） | 同上 |
| `db_` | Milvus/Zilliz（`db_`+32 位以上） | 同上 |
| `fw_` / `fw-` | Fireworks AI | **同一厂商两个来源写法不同，两个都要收** |
| `xai-` | trufflehog 写死 80 位（含 `_`）、pleno-dlp 写 40~100 位（不含 `_`） | 取并集 `xai-[A-Za-z0-9_]{40,100}`，宁可多收 |
| `key-` | Mailgun（`key-`+32 hex） | 长度 + hex 字符集 |

---

## 7. 常见业务系统 Token（国内）

> ⚠️ 本章是国内覆盖最薄弱的部分。开源扫描器（gitleaks / trufflehog / Nosey Parker / detect-secrets）**没有任何一条**钉钉、企业微信、微信支付、支付宝的规则；`pleno-dlp` 618 个检测器里也只覆盖了飞书（`lark`）与声网（`agoraio`）。
> 因此：**已核实**（有开源检测器或官方文档佐证）与**社区观察**（无权威来源）必须分开对待。前者可直接实现，后者只应作为上下文驱动的线索。

### 7.1 已核实（有开源检测器佐证）

| 平台 | 凭据 | 形态 | 来源 |
| --- | --- | --- | --- |
| 飞书 / Lark | App ID | `cli_` + `[a-f0-9]{16}`（共 20） | `pleno-dlp` lark 检测器 |
| 飞书 / Lark | App Secret | 32 位字母数字 | 同上 |
| 飞书 / Lark | 上下文关键词 | `(lark\|feishu)[_-]?(app[_-]?)?(id\|secret\|token\|key)` | 同上 |
| 声网 Agora | App ID / App Certificate | 32 位小写 hex | `pleno-dlp` agoraio 检测器 |
| 声网 Agora | 上下文关键词 | `agora`（**必需**：32 hex 与 MD5 同形） | 同上 |
| 微信支付（间接） | 商户私钥 | `-----BEGIN PRIVATE KEY-----` PEM | §2.4 |
| 推送（FCM） | 旧版服务端密钥 | `AAAA[A-Za-z0-9_-]{7}:APA91b[A-Za-z0-9_-]{134,200}` | `pleno-dlp` firebasecloudmessaging |
| 推送（APNs） | 私钥 | `.p8` 文件（PEM PKCS#8），配 10 位 Key ID + 10 位 Team ID | `pleno-dlp` apns（无正则，按文件判定） |

**飞书 tenant/user access token**：官方返回形如 `t-` / `u-` 开头 + 长 base64url 的 token（会话级，短时效）。可作为"运行时 token"单独一类，等级低于 App Secret。

### 7.2 社区观察（无权威来源，仅作线索）

以下形态来自广泛流传的实践经验，**本文未找到官方文档或开源检测器佐证**，一律标 D 级：

| 平台 | 凭据 | 常见形态（待验证） |
| --- | --- | --- |
| 钉钉 | AppKey | `ding` + 16 位小写字母数字（共 20） |
| 钉钉 | AppSecret | 64 位字母数字 |
| 钉钉 | 自定义机器人 Webhook | `https://oapi.dingtalk.com/robot/send?access_token=<64 位 hex>` |
| 钉钉 | 加签密钥 | `SEC` + 64 位字母数字 |
| 企业微信 | CorpID | `ww` + 16 位字母数字 |
| 企业微信 | CorpSecret | 43 位字母数字 |
| 企业微信 | 群机器人 Webhook | `https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=<UUID>` |
| 微信公众号 | AppID | `wx` + 16 位 hex（共 18） |
| 微信公众号/小程序 | AppSecret | 32 位 hex |
| 微信支付 | APIv2 密钥 / APIv3 密钥 | 各 32 位字母数字 |
| 微信支付 | 商户号 | 8~10 位数字 |
| 微信支付 | 证书序列号 | 40 位大写 hex（**与 git SHA-1 同形**） |
| 小程序 | session_key | base64 24 位（会话级） |
| 支付宝 | app_id | 16 位数字 |
| 支付宝 | 应用私钥/公钥 | PEM（RSA2，2048 位） |
| 支付宝 | AES 密钥 | base64（16/24/32 字节） |
| 高德地图 | Key | 32 位 hex |
| 百度地图 | AK | 32 位字母数字 |
| 腾讯地图 | Key | 多段 `-` 分隔 |
| 极光 JPush | AppKey / MasterSecret | 各 24 位 hex（**与 MongoDB ObjectId 同形**！） |
| 友盟 | AppKey | 24 位 hex |
| 腾讯云短信 | SdkAppId | 10 位数字，通常以 `14` 开头 |
| 七牛云 | AK / SK | 各 40 位 base64url 字符 |
| 华为/小米/OPPO/vivo 推送 | AppSecret | 无公开固定格式 |

### 7.3 国内业务系统的识别策略（重要）

由于格式信息稀缺，**对国内业务系统不应走"形态优先"路线**，而应走"上下文优先"路线：

1. **变量名/配置键就是最强的判据**
   `AppSecret`、`app_secret`、`AppKey`、`corpsecret`、`mch_id`、`api_v3_key`、`APISecret`、`MasterSecret`、`access_token`、`aesKey`、`privateKey`、`serial_no`。
   建议把这些键名直接编译进 Aho-Corasick 预筛表——命中后，**其右侧同行的任意 16~64 位高熵值**都提升为候选。

2. **组合规则优于单值规则**
   例如"同一片段内同时出现 `APPID` + `APIKey` + `APISecret`（讯飞星火）"、"同时出现 `mchid` + `apiKey` + `serial_no`（微信支付）"、"同时出现 `corpId` + `corpSecret` + `agentId`（企业微信）"。这类组合规则的误报率远低于任何单值正则。

3. **URL 域名是可靠的归属信号**
   `oapi.dingtalk.com`、`open.feishu.cn`、`open.larksuite.com`、`qyapi.weixin.qq.com`、`api.weixin.qq.com`、`openapi.alipay.com`、`restapi.amap.com`、`api.map.baidu.com`、`api.jpush.cn`、`open.bigmodel.cn`、`dashscope.aliyuncs.com`、`api.moonshot.cn`、`ark.cn-beijing.volces.com`。请求体里出现这些域名时，同请求中的高熵值应显著加分。

4. **hex 类一律不单独告警**
   24 位 hex（JPush/友盟）、32 位 hex（微信 AppSecret/高德/Agora/百度地图）、40 位 hex（证书序列号）在没有任何上下文时，与哈希、ID、随机串无法区分。**这是国内场景下误报的第一大来源。**

5. **可离线判定的少数例外**
   - `cli_` + 16 hex（飞书 App ID）—— 前缀足够独特；
   - `ding` + 16 位（钉钉 AppKey）—— 前缀足够独特（但需注意英文 `ding` 单词，须加长度/字符集约束）；
   - `ww` + 16 位（企业微信 CorpID）—— 前缀较短，须限长度；
   - `wx` + 16 hex（公众号 AppID）—— 同上；
   - `SEC` + 64 位（钉钉加签）—— 须限长度；
   - PEM 私钥（微信支付/支付宝）—— 结构判定。

---

## 8. 校验能力矩阵：哪些能离线判定，哪些必须靠上下文

状态机的价值上限，取决于规则里有多少"可离线否决"的判据。下表把常见凭据按**可判定性**排序，这也是建议的实现优先级。

### 8.1 第一优先级：离线可判定（零/极低误报，先做这些）

| 凭据 | 判据 | 说明 |
| --- | --- | --- |
| GitHub PAT（`ghp_`/`gho_`/`ghu_`/`ghs_`/`ghr_`） | **CRC32 校验位** | 见 §8.2，可 100% 离线证伪 |
| OpenSSH 私钥 | base64 解码后以 `openssh-key-v1\0` 开头 | 100% |
| PEM 私钥 | `-----BEGIN … PRIVATE KEY-----` 配对 | 100% |
| Azure Storage Account Key | base64 解码后恰为 64 字节（88 字符含 `==`） | 高 |
| WireGuard 私钥 | base64 解码后恰为 32 字节 | 高 |
| JWT | header/payload 必须解码为合法 JSON；`alg` 与签名长度必须一致 | 高 |
| JWE | 5 段，header 解码为 JSON 且含 `enc` | 高 |
| Discord Bot Token | 第一段 base64 解码须为纯数字 user id | 高 |
| Basic 认证 | base64 解码须含 `:` | 高 |
| AWS SigV4 / 预签名 URL | `Credential=` / `X-Amz-Credential=` 内的 `AKIA…` 结构与 `Signature=<64hex>` 并存 | 高 |
| age 私钥 | bech32 校验和 | 高 |
| JWK 私钥 | JSON 含 `kty` 且含 `d`（或 oct 含 `k`） | 高 |
| Google 服务账号 JSON | `"type":"service_account"` 与 `"private_key"` 同对象 | 高 |
| PowerShell `ConvertTo-SecureString` | `01000000d08c9ddf0115d1118c7a00c04fc297eb` 头部 | 高 |
| 中国居民身份证号 | 18 位结构 + GB/T 2260 省级前缀 + 合法历法生日 + ISO 7064 MOD 11-2 校验位 | 高（三者同时成立） |
| PyPI token | 固定前缀 `pypi-AgEIcHlwaS5vcmc` + macaroon 结构 | 高（前缀） |
| Bedrock API key | `ABSK…` / `bedrock-api-key-YmVkcm9jay5hbWF6b25hd3MuY29t`（后者 base64 解码即 `bedrock.amazonaws.com`） | 高（魔法串） |
| OpenAI key | 串内含固定 base64 片段 `T3BlbkFJ`（`OpenAI` 的 base64） | 高（魔法串） |
| Anthropic key | `sk-ant-api03-` + 93 字符 + 结尾 `AA` | 高 |
| TOTP/恢复码 | 结构弱，一般不纳入 | — |

身份证号（GB 11643，`pkg/secretmask` 的 `pii.chinese_id`）是 18 位：6 位行政区划 + 8 位出生日期（`YYYYMMDD`）+ 3 位顺序码 + 1 位校验位（`0-9` 或 `X`）。校验位是 ISO 7064 MOD 11-2：前 17 位按权重 `7 9 10 5 8 4 2 1 6 3 7 9 10 5 8 4 2` 加权求和，`sum mod 11` 查 `10X98765432`。只用校验位能排除约 10/11 的随机 18 位数字；再要求省份码属于 GB/T 2260 的省级前缀（11–15、21–23、31–37、41–46、50–54、61–65、71、81、82）、生日是真实日历日（含闰年），剩余误报约为万分之一量级。1999 年之前的 15 位旧号没有校验位，不在覆盖范围内。手机号（`pii.cn_mobile`）没有可重算的校验位，按 §8.4 处理：11 位与 `1[3-9]x` 已分配号段（55 个）只是候选检查，上下文命中才改写。

### 8.2 GitHub PAT 的 CRC32 校验位（已核实，实现即可用）

GitHub 官方博客说明：新格式 token 的**最后 6 位是 32 位 CRC32 校验和的 Base62 编码**，前缀标识类型，中间是 30 位随机部分。等价实现（来自 `docker/docker-agent` 的测试辅助代码，已核对）：

```go
const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// body 是前缀与校验位之间的 30 个 base62 字符
func checksumSuffix(body string) string {
    checksum := uint64(crc32.ChecksumIEEE([]byte(body))) // CRC32-IEEE，只对 body 计算
    var suffix [6]byte
    for i := 5; i >= 0; i-- {                             // 从低位往高位填，等价于左侧补零
        suffix[i] = alphabet[checksum%62]
        checksum /= 62
    }
    return string(suffix[:])
}
```

要点：
- 校验对象是 **30 位 body**，**不含** `ghp_` 前缀，也**不含**校验位自身；
- 字母表顺序是 `0-9` → `A-Z` → `a-z`（顺序敏感，写反则全部校验失败）；
- 长度：`ghp_`(4) + 30 + 6 = **40** 字符。

因此完整判定为：`^(gh[pousr])_([0-9A-Za-z]{30})([0-9A-Za-z]{6})$` 且 `suffix == checksumSuffix(body)`。

细粒度 PAT（`github_pat_`）**不使用**该 CRC32 方案：形态为 `github_pat_` + 22 字符 + `_` + 59 字符，只能靠前缀 + 精确长度判定。总长是 **93**：前缀 `github_pat_` 本身 11 个字符（含末尾下划线），与 gitleaks 记作 `github_pat_\w{82}` 一致；本文早先写作 86，是把前缀误记为 4 个字符。

### 8.3 第二优先级：强前缀 + 精确长度（无需上下文）

| 类型 | 形态 |
| --- | --- |
| Anthropic | `sk-ant-api03-` + `[A-Za-z0-9_-]{93}` + `AA`；管理密钥 `sk-ant-admin01-` 同构 |
| Google API Key | `AIza` + `[A-Za-z0-9_-]{35}`（共 39；`AIzaSy` 是其常见子前缀） |
| Google OAuth Client Secret | `GOCSPX-` + 28 字符 |
| HuggingFace | `hf_` + 34 字母 / `api_org_` + 34 字母 |
| Perplexity | `pplx-` + `[A-Za-z0-9]{48}` |
| OpenRouter | `sk-or-v1-` + `[0-9a-f]{64}` |
| xAI | `xai-` + `[0-9A-Za-z_]{80}` |
| Groq | `gsk_` + `[A-Za-z0-9]{52}`（Nosey Parker 记为 50–54） |
| Replicate | `r8_` + `[0-9A-Za-z_-]{37}` |
| DeepSeek | `sk-` + `[a-z0-9]{32}` |
| Jina | `jina_` + `[A-Za-z0-9]{60}` |
| npm | `npm_` + `[a-z0-9]{36}` |
| Shopify | `shpat_`/`shpca_`/`shppa_`/`shpss_` + `[0-9a-fA-F]{32}` |
| DigitalOcean | `dop_v1_`/`doo_v1_`/`dor_v1_` + `[0-9a-f]{64}` |
| HashiCorp Vault | `hvs.` + `[\w-]{90,120}`；`hvb.` + 138+；旧版 `s.` + 24 |
| Cloudflare Origin CA | `v1.0-` + `[0-9a-f]{24}` + `-` + `[0-9a-f]{146}` |
| Postman | `PMAK-` + `[0-9a-f]{24}` + `-` + `[0-9a-f]{34}` |
| Notion | `ntn_` + 11 数字 + 35 字母数字 |
| Linear | `lin_api_` + `[a-z0-9]{40}` |
| LangSmith | `lsv2_pt_` / `lsv2_sk_` + … |
| Langfuse | `pk-lf-<uuid>` / `sk-lf-<uuid>` |
| Grafana | `glsa_` + `[A-Za-z0-9]{32}` + `_` + `[0-9a-f]{8}`；`glc_` + base64 |
| New Relic | `NRAK-` + 27 位大写字母数字 |
| Okta | `00` + `[\w=-]{40}`（需上下文 `okta`） |
| Slack | `xoxb-`/`xoxp-`/`xoxa-`/`xoxr-`/`xoxs-`/`xapp-`（各型长度见附录 A） |
| Terraform Cloud | `[a-z0-9]{14}.atlasv1.[a-z0-9\-_=]{60,70}` |
| Heroku（新版） | `HRKU-AA` + `[0-9A-Za-z_-]{58}` |
| 阿里云 AccessKeyId | `LTAI` + `[a-z0-9]{20}`（共 24） |
| 腾讯云 SecretId | `AKID` + 32 字符 |
| 火山引擎 AccessKeyId | `AKLT[A-Za-z0-9\-_]{16,28}`，总长 20–32（**官方 API Reference 明确**） |
| 百度云 AK | `ALTAK…`（出现在 `bce-v3/ALTAK-<ak>/<ts>/<sig>` 头中） |
| 钉钉 | AppKey `ding` + 16–18 字符；AppSecret 64 字符 |
| 飞书/Lark | `cli_` + 16 位字母数字；app_secret 32 位 |
| 企业微信 | corpid `ww` + 16 位；corpsecret 43 字符 |
| 微信公众号 | AppID `wx` + 16 位 hex；AppSecret 32 位 hex |
| 支付宝 | app_id 16 位数字（**弱，必须配上下文**） |

### 8.4 第三优先级：弱形态，必须依赖上下文

这些"裸形态"极易与哈希/UUID/随机 ID 混淆，**没有上下文就不应告警**：

- 32 位 hex（MD5、Datadog API Key、DeepSeek、JPush AppKey、微信 AppSecret、AssemblyAI key、Kong key…）
- 40 位 hex（git SHA-1、AWS Secret Access Key、Datadog APP Key、Sentry token、GitLab PTT `glptt-`）
- 64 位 hex（Sentry access token、Discord client secret、Datadog/Elastic token、HMAC 摘要）
- 24 位 hex（JPush AppKey/MasterSecret、MongoDB ObjectId！）
- 36 位 UUID（Heroku API Key、MessageBird Client ID、Azure AD `client_id`）
- 44 位 base64（32 字节密钥：WireGuard、Service Bus `SharedAccessKey`、AES-256 key）
- 88 位 base64（Azure Storage/Cosmos Account Key、Service Bus 部分密钥）
- 中国大陆手机号（11 位，`1[3-9]x` 已分配号段）——号段只收窄候选，不能证明；没有 `手机号`/`phone` 一类上下文时不改写
- 任意长度高熵字符串（密码）

对第三类，建议的判定式是：`上下文关键词命中 且 长度/字符集匹配 且 熵 ≥ 阈值`，三者同时满足才告警。

### 8.5 在线校验（可选增强，不要放进热路径）

trufflehog 的做法值得借鉴：拿到候选后调用厂商 API 验证有效性（例如 `POST https://api.openai.com/v1/models`）。若要引入：

- 只在**人工触发的审计/复核**流程里调用，绝不放在请求代理的热路径；
- 用最小权限的读接口，避免产生计费（例如"列出模型"而不是"发起推理"）；
- 结果是三态：`valid` / `invalid` / `indeterminate`（网络失败、限流必须记为 indeterminate，不得当作 invalid）。
- 典型可验证端点：OpenAI `/v1/models`、Anthropic `/v1/models`、Groq `/openai/v1/models`、智谱 `/api/paas/v4/models`、GitHub `/user`、Slack `auth.test`、Stripe `/v1/balance`。

---

## 9. 在 Trajecta 中的落地实现

### 9.1 现状与差距

在 `pkg/secretmask` 之前，凭据识别发生在两处：

| 位置 | 现状 | 局限 |
| --- | --- | --- |
| `internal/analyzer/analyzer.go` 的 `credentialPatterns` | 7 条正则：OpenAI `sk-[A-Za-z0-9_-]{20,}`、AWS `AKIA[0-9A-Z]{16}`、GitHub `gh[pousr]_[A-Za-z0-9_]{20,}`、私钥块、Bearer、DB 连接串、Cookie | 无长度约束、无校验位、无上下文、无厂商细分；`sk-` 一条会同时误伤/漏判多家 |
| `internal/redaction/url.go` | URL 查询参数与 userinfo 脱敏 + `metadataSecretPatterns`（Bearer / `key=value` / 服务账号 JSON） | 只覆盖 URL 与元数据文本，不做正文扫描；用正则整体替换而非精确 span |

这两处仍是审计与 URL 脱敏的路径；转发前的识别与改写已由 `pkg/secretmask` 接管（§9.2–§9.8）。上述差距集中在三点：**规则表示力不足（缺长度/校验/上下文）**、**覆盖面窄（缺国内厂商与结构性 token）**、**输出无置信度与 span**。

### 9.2 包结构与规则生成

能力全部落在 `pkg/secretmask`，没有 YAML 规则文件，也不在运行时构造正则：

| 文件 | 作用 |
| --- | --- |
| `rules.rl` | Ragel 源，词法模式（字符类、前缀、结构形状）的唯一事实源 |
| `rule.go` | 规则元数据表，每条规则一项：`Kind`、`Grade`、`Strength`、`MinLen`、`MaxLen`、`Validator`、`CandidateCheck`、`Keywords`、`Prefixes`、`PrefixSkip`、`Literals` |
| `rules_ragel.go` | 由 `rules.rl` 生成的 Go DFA，33425 行 / 371351 字节（约 363 KiB）的状态表，已提交；不读、不手改，一切改动回到 `rules.rl` 与 `rule.go` |
| `validators.go` | 离线结构校验，按字符串 `jwt`、`anthropic_key`、`github_pat`、`jwe`、`bech32`、`chinese_id` 命名 |
| `mobile.go` | 手机号号段表的候选检查（`cn_mobile_prefix`）与渲染辅助；不构成校验器 |
| `context.go` | 全局凭据词表与 96 字节上下文窗口 |
| `finding.go` | `Finding`、`Confidence`、`Reason` 与打分权重 |
| `vault.go` | 假值的渲染与反查 |
| `restore.go` | `Vault.Restore`、`Vault.RestoreInPlace` 与流式 `Restorer` |
| `scan.go` | `collector` 与 `Detector` API（`AppendFindings` / `Scan`） |

生成入口是 `task generate:secretmask`，实际执行 `ragel-go -G2 -o rules_ragel.go rules.rl`，随后 gofmt 一次。两个容易踩的点：

- **宿主语言由 Ragel 二进制名决定，不是命令行开关。** `ragel-go` 产出 Go；本机上的通用 `ragel` 二进制产出 C，二者不可互换。
- **Ragel 7.1 已移除 `-Z`**，因此 `ragel -Z` 与 `ragel-go -Z` 都直接报 `-Z is an invalid argument`。`-G2` 是 goto 驱动的表风格，在试过的几种风格里实测最快。

状态机与 `rule.go` 必须覆盖同一套 `RuleID`，`TestRuleMetadataComplete` 校验这一点。日常构建不需要 Ragel，生成文件已在仓库中。

匹配语义由 scanner 的 `|* ... *|` 决定：**最长匹配优先，等长时先声明者优先**。`rules.rl` 中规则顺序因此具有语义，文件注释也写明了这一点——`T3BlbkFJ` 系形态必须排在 `sk-` 通用池之前，否则同一段 `sk-` 文本会先被通用池吃掉。

长度上下界**不写进状态机**，而是在 `scan.go` 的 `collector.emit` 里用原生代码判断。固定重复 `{n}` 交给状态机在宽字符类上展开，状态表会按组合数膨胀：实测把一条 `b64u{24} '.' b62u{6} '.' b62u+` 形态的候选规则加进去，生成代码从 25238 行涨到 144434 行（5.7 倍），生成时间同步变长；同一形态改用「`+` 加原生长度检查」后回到 25238 行量级。原生长度检查是承重设计，不是风格偏好。

数字类形状（身份证号、手机号、银行卡）走**粗筛 + 原生分类**，而不是每条一段 `[0-9]{n}`：状态机里只有一条 `digit_run = [0-9]+ [Xx]?;`，命中后由 `scan.go` 的 `emitDigits` 按 `digitClasses` 逐类比较长度、跑校验器或候选检查，第一类通过即采用。这样新增一类数字只改 `digitClasses` 与该规则元数据，不增加状态、不需要重新生成，也不会产生第二遍扫描。手机号即按此加入：`rules.rl` 未改、生成表仍是 33425 行，改动只有规则元数据、`mobile.go` 与渲染器。

### 9.3 检测分层、置信度与覆盖边界

`Detector.AppendFindings` 依次走四层，越往后越贵，只有前一层放行的候选才进入下一层：

1. **L1 词法**：编译好的 DFA 单遍扫描，最长匹配，不回溯。
2. **L2 结构校验**：`validators.go`。`jwt` 要求三段 base64url 全部解码成功、头部是命名了算法的 JSON 对象、载荷是 JSON 对象、签名解码长度与该算法一致（`HS256` 32 字节、`ES512` 132 字节等）；`anthropic_key` 要求结尾是已发布 Anthropic 各 key 家族共有的 `AA`；`github_pat` 重算 30 位 body 的 CRC32-IEEE 并比对末 6 位 base62；`jwe` 要求五段式头部解码为含 `enc` 的 JSON 对象；`bech32` 验证 age 私钥的 BIP-173 校验和；`chinese_id` 验证 GB/T 2260 省级前缀、历法生日（含闰年，年份 1800–2099）与 ISO 7064 MOD 11-2 校验位。带校验器的规则只上报通过校验的匹配，且通过本身就足以认定。手机号的 `cn_mobile_prefix` 则只是**候选检查**：检查 11 位数字与 `1[3-9]x` 号段是否已分配，通过不提分，因此手机号能否改写由 L3 决定。

**「足以认定」是校验器的准入线，比「看起来像」高得多。** 通过校验器的 finding 会被自动改写，所以只缩小范围而不构成证明的判据不能写进这里：例如「base64 解码后正好 64 字节」会接受转录里每一个 64 字节块（Azure Storage Key、Cosmos Key、Service Bus Key 与普通哈希、nonce 共用该形状），因此它只是 §8.4 的弱形态，不是校验器。
3. **L3 上下文**：在匹配位置前后各 96 字节窗口内搜索凭据词汇，来源是 `context.go` 的固定全局词表，加上该规则自己的 `Keywords`。`ContextWindow` 可以按规则收窄窗口（手机号用 24 字节，因为标签必须紧邻号码）；`SkipContext` 让已经由校验器认定的规则跳过这一层；`WordBounded` 在原生代码里拒绝两侧紧邻 ASCII 字母或数字的匹配。
4. **L4 置信度门限**：按下面的权重打分并分档。

打分权重（`finding.go`）：Strong 70 分、Anchored 35 分、Generic 10 分；规则定长且长度吻合加 10 分；窗口内出现凭据词汇加 40 分；校验器通过则分数抬到不低于 70 分。总分封顶 100。分档为 High ≥ 70、Medium ≥ 45、Low < 45。`Finding.Maskable()` 只放行 Medium 及以上自动改写；Low 仍然上报，让审计能看到看见了什么、以及故意没动什么。

规则集共 77 条：Strong 35、Anchored 24、Generic 18。范围是结构形状（JWT、JWE、PEM 头、Bearer）与本文档第 3、4 章记录的 LLM 厂商前缀，外加 `sk-` / `sk_` 两个通用池，再加 **§8.1 里锚点足够窄的那几条**：AWS AccessKeyId、GitHub 经典 PAT（CRC32）与细粒度 PAT、PyPI token、age 私钥、PowerShell `ConvertTo-SecureString`，以及**个人数据**里的中国居民身份证号与手机号。身份证号按 Generic 计分（18 位数字本身不证明什么），校验器通过即认定；手机号同样按 Generic 计分，靠号段收窄候选、靠上下文进入 Medium；15 位旧号没有校验位，不覆盖。

**刻意不覆盖 §8.4 的弱形态。** 32/40/64 位 hex、UUID、无锚点的 44/88 位 base64 是哈希、对象 ID 与密钥共用的形状，而通过校验器会把分数抬到可改写档，所以这类规则一旦加入就会改写转录里每一个同形块。它们要成为可改写的，需要先有「上下文必需」这一档语义——当前打法是上下文加分而非必需。手机号是唯一进入规则集的弱形态：它有公开号段可把候选空间收窄，而 `手机号`/`phone` 这类词不像 `token` 那样遍布代码，因此 Generic+定长 20 分加 40 分上下文刚好越过 Medium，裸数字仍只上报。**DSN 与 Cookie 也暂不覆盖**：只有一部分是秘密，而 vault 的替换粒度是整个命中跨度，整段改写会改变连接目标、破坏结构，需要子跨度机制。`internal/analyzer` 保留这些正则用于事后上报，那里可以有误报，自动改写不能。

引擎不做的事同样属于当前事实：不做熵估计、不支持 look-behind，也只解码校验器真正需要的那部分。被编码、派生、截断、改大小写或重新格式化过的凭据识别不到；需要真值才能完成的操作（HMAC 签名、AWS SigV4、签发 JWT）在只看到假值的地方无法进行；客户端工具与本地 Responses runtime 拿到的是还原后的真值，只有上游托管的工具直接看到假值（§9.4）。

实测（`-count=3` 稳定值）：约 73 KB 载荷的词法扫描约 1.4–1.5 ms，约 51–53 MB/s（加入身份证规则前为 1.32 ms / 57 MB/s；生成表未因手机号规则变化）。身份证规则每 64 KB 命中一条时约 231 MB/s、命中约 3500 条时约 151 MB/s（`SkipContext`）；手机号命中一条时约 211 MB/s，密集时约 21 MB/s（`BenchmarkScanDenseMobileNumbers`，24 字节窗口），因为上下文扫描是它唯一的判据、不能跳过。精度用仓库自身度量：`git ls-files` 的 1,122 个文本文件（约 20 MB，另加本地 `config/config.dev.yaml`）共 78 处命中、25 个文件——除 2 处真实 key 外全部是测试夹具、文档示例与已知的 Low 误报（`ek_` 标识符、`go.sum` 的 `LTAI` 形态、`17179869184` 常量、HTML 快照里的 11 位数字），Low 不改写；身份证 13 处（全 High），手机号 29 处（12 处 Medium 全在两个测试文件的样例里，其余 Low）。干净检出上为 76 处 / 24 个文件。

### 9.4 遮蔽与还原

遮蔽是开关控制的能力：YAML 的 `secret_masking.enabled`，或环境变量 `TRAJECTA_SECRET_MASKING_ENABLED`，**默认开启**（省略该键即开启，显式 `false` 关闭）。它改变模型实际看到的内容，属于语义变更而非日志细节，所以每个被改写的值都落一条 `secret.masked` 事件、每次还原落一条 `secret.restored`，运维可以从 cassette 直接看出它是否在生效。

假值必须同时满足三条性质：

- **确定性**：假值是 `(seed, 规则名, 真值)` 的纯函数，同一个凭据永远渲染成同一个假值。多轮对话的一致性依赖它，上游 prompt cache 的前缀稳定性也依赖它。
- **与真值等长**：代理把上游返回的 `Content-Length` 原样透传给客户端，而请求体长度由 net/http 从实际交给它的字节切片推导（`internal/proxy/handler.go`）。长度一变，请求与响应两侧都会静默错位。因此这条是承重约束。
- **保留形状**：保留真值的前缀、全部非字母数字字符与分段字母表（整段小写 hex 仍取小写 hex），按形状校验的下游工具仍接受假值。

身份证号与手机号是这三条的例外：逐字节替换会破坏它们的结构，下游按结构校验的工具会拒绝假值。身份证号由 `renderChineseID` 用同一份密钥流重新渲染一个合法号码（真实省份前缀、1950–2009 年的生日、重算的 ISO 7064 校验位）；手机号由 `renderCNMobile` 保留真值的三位号段、重排后 8 位。等长与确定性不变，但**身份证生日与手机号后 8 位都不保留真值**——它们是最直接指向个人的部分，代价是依赖这些字段交叉核对的流程会对不上（例如用末四位展示或校验手机号）。

映射只存在于内存（`vault.go` 的 `Vault`）：默认用 `crypto/rand` 播种，每个进程不同；上限 4096 条，超出按最旧优先淘汰。从线路上读到的假值离开这个进程就无从还原。遮蔽是幂等的，vault 已认识的假值会被跳过。

PEM 规则只上报、不改写：它只匹配 `-----BEGIN ... PRIVATE KEY-----` 头行，头行不含密钥材料，而真正承载密钥的 base64 正文没有任何规则覆盖；改写头行既保护不了私钥，又会破坏文件语法。`policySkipMask` 因此**只对 `structural.pem_private_key` 这一条规则 ID** 返回 true——早先它按 `KindPrivateKey` 判断，把同属该 Kind 的 age 私钥一起豁免了，而 age 私钥是整条命中就是要保护的完整秘密，已修正为按规则 ID 豁免。

请求体走拆分：cassette 记录**原始字节**，上游只收到假值。这是有意选择——replay 按请求字节匹配，人读 trace 也需要看到真实请求。

响应路径把假值还原成真值。体是流式的，扫描单遍且单调：只放出已判定的字节，判定不了的留在缓冲区等下次读取。

厂商把一条消息拆成许多 JSON delta 并在 token 边界切断字符串，所以模型回显的假值会带 JSON 信封分段到达。一段至少 8 字节且与某条假值开头吻合的 run 会开一条链，跳过中间的信封字节等后续 run 补齐；整条凑齐后各 run 原地改写，凑不齐则原样放回。**要么整条还原、要么完全不动**，因此「长得像假值」不会改写它所在的文本。两段之间最多 512 字节间隔，超过即放弃。

链开着时，**一整条假值的优先级高于链**。否则两种错都会发生：厂商在链的窗口里重复同一个值时，那段字节会被当信封跳过而原样交给客户端；截断的一份后面跟着完整的一份时，后者的开头恰好落在链期待的下一个偏移上，会被当成链的续接，把两份拼成前半真值、后半假值的怪值。因此扫描每到一个可能起头假值的字节，先判断它能否凑成完整的一条；能就改写它并放弃该链（链的字节原样放行）。流式下这个判断可能要等更多字节，此时扫描等待而不是先消费该字节——字节判过就不再回看。

链从第一段开始跟，所以被切成小于 8 字节的片（逐 token 下发）不会被重组，客户端会拿到拼接后的假值——功能上失败，但没有改写无关字节。

还原器装在现有 `UsageSniffer` **之内**而不是之外，因为 sniffer 会把读到的字节写进 cassette。两者等长，所以还原是对调用方缓冲区的原地覆盖。

审计事件 `secret.masked` 与 `secret.restored` 追加到 cassette prelude 事件：前者记录规则、厂商、kind、偏移、长度、分数、置信度与原因，后者记录一次响应里的还原条数；两者都不携带凭据字节。

实测吞吐（`-count=3`）：109 KB 载荷遮蔽约 200 MB/s（热态）、189 MB/s（冷态，含渲染假值）；还原约 295 MB/s，流式还原约 302 MB/s。「整条假值优先于链」的逐字节判定是相对不做它时约 13% 的还原开销。

### 9.5 与现有模块的对接

`pkg/secretmask` 与既有模块目前的边界如下。已经对接的部分描述事实；尚未对接的部分说明为什么不接。

| 模块 | 现状 |
| --- | --- |
| `internal/proxy` | 已对接。请求侧把命中值换成保形假值再发往上游，cassette 仍记录原始体；响应侧在 `UsageSniffer` 之内还原。见 §9.4 |
| `internal/analyzer` 的 `credentialPatterns` | **未替换，仍在使用**。7 条正则分别是 OpenAI `sk-`、AWS `AKIA`、GitHub `gh[pousr]_`、私钥块头行、Bearer、DB 连接串、Cookie；表外的「凭据化 URL」检查走 `url.Parse` 的 userinfo，不是正则。其中 4 条（OpenAI `sk-`、AWS、GitHub、Bearer）已由 `pkg/secretmask` 覆盖并会改写，私钥块只覆盖头行且只上报不改写（§9.4），DB 连接串与 Cookie 连同一并的 URL 检查**刻意不覆盖**：它们只有一部分是秘密，而 vault 的替换粒度是整个命中跨度（§9.3）。两者并存的分工是 analyzer 负责审计侧 findings（可以有误报），secretmask 负责转发侧改写（不能有误报） |
| `internal/analyzer` 的 `PIIDetector` | 已对接。它不重复维护一套 PII 模式，而是对 observation 的节点文本调用 `secretmask.Scan`，只保留 `Kind.PII()` 且 `Finding.Maskable()` 的命中，再把命中值从 evidence 里替换成 `[redacted]`；entry 与 model 两种 exchange scope 都运行。只报可改写档保证审计与改写口径一致：手机号的裸 11 位数字不入账，带上下文的才报。因此审计能覆盖「遮蔽关闭时写入的 trace」与「重分析」，而不只是遮蔽当次的替换记录 |
| `internal/redaction/url.go` | **未改动**。它按 URL 查询参数名与 userinfo 脱敏，依赖字段名而非值，与按字节 span 识别凭据是两件不同的事 |
| `pkg/observe` | 未新增节点类型。命中与还原目前只写 cassette prelude 事件 `secret.masked` / `secret.restored` |
| CLI（`cmd/trajecta`） | 未提供 `secrets scan` 子命令。`Detector` 与 `Vault` 是导出 API，可离线调用 |
| Monitor | 未新增过滤器。事件的属性含 `rule`、`vendor`、`kind`、`score`、`confidence`、`reasons`，可在 trace 事件列表直接读到 |

### 9.6 测试策略

- **正样本**：对每条规则准备"显然伪造但格式合法"的样例。带校验位的规则，其样例的校验位由**本包之外的实现**算出并硬编码进测试（GitHub 的用 `zlib.crc32`，age 的用一份 BIP-173 实现），而不是调用被测函数生成——否则校验器写错时会自我一致地通过。`TestBech32Checksum` 另外用 BIP-173 公布的向量直接验证校验和原语。`TestGitHubPATChecksumCoversTheBody` 逐字节改动 30 位 body 并要求全部被拒，证明校验位确实覆盖整个 body 而不只是长度。
- **负样本（必须有，否则误报会失控）**：
  - git commit SHA-1（40 hex）与 SHA-256（64 hex）；
  - UUID v4；
  - MD5/SHA 校验和；
  - base64 编码的图片片段；
  - 单词里恰好含前缀（如 `sk-` 开头的英文短语、`ghp` 作为变量名片段）；
  - 明显占位符（`YOUR_API_KEY`、`<token>`、`changeme`、全 `A` 串）；
  - 身份证号：校验位错误的 18 位数字、13 月/2 月 30 日/非闰年 2 月 29 日、非法省份码、1800 年前与 2099 年后的生日、夹在 19 位时间戳或更长的字母数字串里的 18 位片段。
  - 手机号：未分配号段（140/154/174/194）、10 位与 12 位数字、`8613800138000` 这类 13 位连写、裸 11 位数字（只报 Low、不改写）。
- **模糊测试**：`go test -fuzz=FuzzScan`，断言（a）不 panic、（b）扫描时间与输入长度近似线性（用 `testing.AllocsPerRun` 或运行时长粗测）、（c）对任意输入 `Scan(x)` 的结果 span 必须落在 `[0,len(x)]` 内。
- **回归基线**：用附录 A–D 的规则集做"覆盖率测试"——统计有多少条上游规则能被当前引擎表达；新增规则时该数字应单调不减。

### 9.7 性能预算（经验值）

实测值见 §9.3 与 §9.4，这里只记两条与规模有关的性质。

| 输入规模 | 现状 |
| --- | --- |
| 1 KB ~ 1 MB 请求/响应体 | 每次请求全量扫描。DFA 无回溯，耗时与输入长度近似线性，77 条规则下约 51–53 MB/s |
| 10 MB 以上 | **没有**关键词预筛或分段跳过：`Detector` 会扫描整个缓冲区。这是有意为之的取舍——先保证语义正确，规模优化留到出现真实需求时再做 |
| 规则数 | 规则越多，DFA 状态数与生成代码越大，而扫描循环仍是按输入字节推进，与规则数无关。这也是长度上下界放在原生代码里判断的原因（§9.2 的状态数对比） |

### 9.8 规则维护流程

1. 从厂商官方文档确认形状 → 改 `pkg/secretmask/rules.rl` 的模式，并在 `pkg/secretmask/rule.go` 补齐该规则的元数据（`Name`、`Vendor`、`Kind`、`Grade`、`Strength`、`MinLen`/`MaxLen`、`Keywords`，需要时还有 `Validator`、`CandidateCheck`、`Prefixes`、`PrefixSkip`、`Literals`、`WordBounded`、`SkipContext`）。数字类形状不需要新写 `rules.rl` 模式，只要把规则 ID 加进 `scan.go` 的 `digitClasses`，有号段一类不可证明的判据时再加一个 `CandidateCheck`；
2. 在 `pkg/secretmask/secretmask_test.go` 的 `positiveSamples` 加一条"显然伪造但格式合法"的样例——`TestRuleMetadataComplete` 要求每条规则都有样例，缺一条就会失败；
3. 跑 `task generate:secretmask` 重新生成 `rules_ragel.go`，并把 `rules.rl`、`rule.go` 与生成文件一起提交；
4. 跑 `go test ./pkg/secretmask/`，其中 `TestFakeKeepsShapeAndLength` 会验证新规则的假值仍然等长、保留前缀与标点；
5. 在 `CHANGELOG.md` 记录规则增删（便于追溯漏报来源）。

---


# 附录

以下四份清单由脚本从上游规则库**直接提取**（而非人工转写），可直接作为规则表的数据源与"覆盖率基线"：

- 提取方式：直接用脚本解析上游规则库源文件（转储日期 2026-10-14），未经人工转写
- 用途一：作为状态机规则的**候选池**——凡上游已有规则而本文未覆盖的形态，都是潜在的漏报点；
- 用途二：作为**回归基线**——统计当前实现能表达多少条上游规则，新增规则时该比例应单调不减。

> 使用提醒：附录 A/B 的正则来自第三方项目（MIT / Apache-2.0 等），引用时请遵守其许可证并保留出处。

---

# 附录 A：gitleaks 内置规则全量清单

来源：`gitleaks/gitleaks` 仓库 `config/gitleaks.toml`（master 分支，抓取于 2026-10-14）。
共 222 条规则。`entropy` 为 gitleaks 要求的最低香农熵（每位字符），空表示该规则不设熵阈值。
正则列过长者已截断，完整正则请对照上游文件；`\\b`、`\\x60` 等转义保持原样。

| 规则 id | 含义 | 正则（截断） | entropy | keywords |
| --- | --- | --- | --- | --- |
| `1password-secret-key` | Uncovered a possible 1Password secret key, potentially compromising access to secrets in vaults. | `\bA3-[A-Z0-9]{6}-(?:(?:[A-Z0-9]{11})\|(?:[A-Z0-9]{6}-[A-Z0-9]{5}))-[A-Z0-9]{5}-[A-Z0-9]{5}-[A-Z0-9]{5}\b` | 3.8 | a3- |
| `1password-service-account-token` | Uncovered a possible 1Password service account token, potentially compromising access to secrets in vaults. | `ops_eyJ[a-zA-Z0-9+/]{250,}={0,3}` | 4 | ops_ |
| `adafruit-api-key` | Identified a potential Adafruit API Key, which could lead to unauthorized access to Adafruit services and sensitive dat… | `(?i)[\w.-]{0,50}?(?:adafruit)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9_-]{32})(?:[\x60'"\s;]\|\\…` |  | adafruit |
| `adobe-client-id` | Detected a pattern that resembles an Adobe OAuth Web Client ID, posing a risk of compromised Adobe integrations and dat… | `(?i)[\w.-]{0,50}?(?:adobe)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-f0-9]{32})(?:[\x60'"\s;]\|\\[nr]\…` | 2 | adobe |
| `adobe-client-secret` | Discovered a potential Adobe Client Secret, which, if exposed, could allow unauthorized Adobe service access and data m… | `\b(p8e-(?i)[a-z0-9]{32})(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | p8e- |
| `age-secret-key` | Discovered a potential Age encryption tool secret key, risking data decryption and unauthorized access to sensitive inf… | `AGE-SECRET-KEY-1[QPZRY9X8GF2TVDW0S3JN54KHCE6MUA7L]{58}` |  | age-secret-key-1 |
| `airtable-api-key` | Uncovered a possible Airtable API Key, potentially compromising database access and leading to data leakage or alterati… | `(?i)[\w.-]{0,50}?(?:airtable)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{17})(?:[\x60'"\s;]\|\\[n…` |  | airtable |
| `airtable-personnal-access-token` | Uncovered a possible Airtable Personal AccessToken, potentially compromising database access and leading to data leakag… | `\b(pat[[:alnum:]]{14}\.[a-f0-9]{64})\b` |  | airtable |
| `algolia-api-key` | Identified an Algolia API Key, which could result in unauthorized search operations and data exposure on Algolia-manage… | `(?i)[\w.-]{0,50}?(?:algolia)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{32})(?:[\x60'"\s;]\|\\[nr…` |  | algolia |
| `alibaba-access-key-id` | Detected an Alibaba Cloud AccessKey ID, posing a risk of unauthorized cloud resource access and potential data compromi… | `\b(LTAI(?i)[a-z0-9]{20})(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | ltai |
| `alibaba-secret-key` | Discovered a potential Alibaba Cloud Secret Key, potentially allowing unauthorized operations and data access within Al… | `(?i)[\w.-]{0,50}?(?:alibaba)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{30})(?:[\x60'"\s;]\|\\[nr…` | 2 | alibaba |
| `anthropic-admin-api-key` | Detected an Anthropic Admin API Key, risking unauthorized access to administrative functions and sensitive AI model con… | `\b(sk-ant-admin01-[a-zA-Z0-9_\-]{93}AA)(?:[\x60'"\s;]\|\\[nr]\|$)` |  | sk-ant-admin01 |
| `anthropic-api-key` | Identified an Anthropic API Key, which may compromise AI assistant integrations and expose sensitive data to unauthoriz… | `\b(sk-ant-api03-[a-zA-Z0-9_\-]{93}AA)(?:[\x60'"\s;]\|\\[nr]\|$)` |  | sk-ant-api03 |
| `artifactory-api-key` | Detected an Artifactory api key, posing a risk unauthorized access to the central repository. | `\bAKCp[A-Za-z0-9]{69}\b` | 4.5 | akcp |
| `artifactory-reference-token` | Detected an Artifactory reference token, posing a risk of impersonation and unauthorized access to the central reposito… | `\bcmVmd[A-Za-z0-9]{59}\b` | 4.5 | cmvmd |
| `asana-client-id` | Discovered a potential Asana Client ID, risking unauthorized access to Asana projects and sensitive task information. | `(?i)[\w.-]{0,50}?(?:asana)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([0-9]{16})(?:[\x60'"\s;]\|\\[nr]\|$)` |  | asana |
| `asana-client-secret` | Identified an Asana Client Secret, which could lead to compromised project management integrity and unauthorized access. | `(?i)[\w.-]{0,50}?(?:asana)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{32})(?:[\x60'"\s;]\|\\[nr]\…` |  | asana |
| `atlassian-api-token` | Detected an Atlassian API token, posing a threat to project management and collaboration tool security and data confide… | `(?i)[\w.-]{0,50}?(?:(?-i:ATLASSIAN\|[Aa]tlassian)\|(?-i:CONFLUENCE\|[Cc]onfluence)\|(?-i:JIRA\|[Jj]ira))(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}…` | 3.5 | atlassian, confluence, jira, atatt3 |
| `authress-service-client-access-key` | Uncovered a possible Authress Service Client Access Key, which may compromise access control services and sensitive dat… | `\b((?:sc\|ext\|scauth\|authress)_(?i)[a-z0-9]{5,30}\.[a-z0-9]{4,6}\.(?-i:acc)[_-][a-z0-9-]{10,32}\.[a-z0-9+/_=-]{30,120})(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | sc_, ext_, scauth_, authress_ |
| `aws-access-token` | Identified a pattern that may indicate AWS credentials, risking unauthorized cloud resource access and data breaches on… | `\b((?:A3T[A-Z0-9]\|AKIA\|ASIA\|ABIA\|ACCA)[A-Z2-7]{16})\b` | 3 | a3t, akia, asia, abia, acca |
| `aws-amazon-bedrock-api-key-long-lived` | Identified a pattern that may indicate long-lived Amazon Bedrock API keys, risking unauthorized Amazon Bedrock usage | `\b(ABSK[A-Za-z0-9+/]{109,269}={0,2})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | absk |
| `aws-amazon-bedrock-api-key-short-lived` | Identified a pattern that may indicate short-lived Amazon Bedrock API keys, risking unauthorized Amazon Bedrock usage | `bedrock-api-key-YmVkcm9jay5hbWF6b25hd3MuY29t` | 3 | bedrock-api-key- |
| `azure-ad-client-secret` | Azure AD Client Secret | `(?:^\|[\\'"\x60\s>=:(,)])([a-zA-Z0-9_~.]{3}\dQ~[a-zA-Z0-9_~.-]{31,34})(?:$\|[\\'"\x60\s<),])` | 3 | q~ |
| `beamer-api-token` | Detected a Beamer API token, potentially compromising content management and exposing sensitive notifications and updat… | `(?i)[\w.-]{0,50}?(?:beamer)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(b_[a-z0-9=_\-]{44})(?:[\x60'"\s;]\|…` |  | beamer |
| `bitbucket-client-id` | Discovered a potential Bitbucket Client ID, risking unauthorized repository access and potential codebase exposure. | `(?i)[\w.-]{0,50}?(?:bitbucket)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{32})(?:[\x60'"\s;]\|\\[…` |  | bitbucket |
| `bitbucket-client-secret` | Discovered a potential Bitbucket Client Secret, posing a risk of compromised code repositories and unauthorized access. | `(?i)[\w.-]{0,50}?(?:bitbucket)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9=_\-]{64})(?:[\x60'"\s;]\…` |  | bitbucket |
| `bittrex-access-key` | Identified a Bittrex Access Key, which could lead to unauthorized access to cryptocurrency trading accounts and financi… | `(?i)[\w.-]{0,50}?(?:bittrex)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{32})(?:[\x60'"\s;]\|\\[nr…` |  | bittrex |
| `bittrex-secret-key` | Detected a Bittrex Secret Key, potentially compromising cryptocurrency transactions and financial security. | `(?i)[\w.-]{0,50}?(?:bittrex)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{32})(?:[\x60'"\s;]\|\\[nr…` |  | bittrex |
| `cisco-meraki-api-key` | Cisco Meraki is a cloud-managed IT solution that provides networking, security, and device management through an easy-t… | `[\w.-]{0,50}?(?i:[\w.-]{0,50}?(?:(?-i:[Mm]eraki\|MERAKI))(?:[ \t\w.-]{0,20})[\s'"]{0,3})(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([0-9…` | 3 | meraki |
| `clickhouse-cloud-api-secret-key` | Identified a pattern that may indicate clickhouse cloud API secret key, risking unauthorized clickhouse cloud api acces… | `\b(4b1d[A-Za-z0-9]{38})\b` | 3 | 4b1d |
| `clojars-api-token` | Uncovered a possible Clojars API token, risking unauthorized access to Clojure libraries and potential code manipulatio… | `(?i)CLOJARS_[a-z0-9]{60}` | 2 | clojars_ |
| `cloudflare-api-key` | Detected a Cloudflare API Key, potentially compromising cloud application deployments and operational security. | `(?i)[\w.-]{0,50}?(?:cloudflare)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9_-]{40})(?:[\x60'"\s;]\|…` | 2 | cloudflare |
| `cloudflare-global-api-key` | Detected a Cloudflare Global API Key, potentially compromising cloud application deployments and operational security. | `(?i)[\w.-]{0,50}?(?:cloudflare)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-f0-9]{37})(?:[\x60'"\s;]\|\\…` | 2 | cloudflare |
| `cloudflare-origin-ca-key` | Detected a Cloudflare Origin CA Key, potentially compromising cloud application deployments and operational security. | `\b(v1\.0-[a-f0-9]{24}-[a-f0-9]{146})(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | cloudflare, v1.0- |
| `codecov-access-token` | Found a pattern resembling a Codecov Access Token, posing a risk of unauthorized access to code coverage reports and se… | `(?i)[\w.-]{0,50}?(?:codecov)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{32})(?:[\x60'"\s;]\|\\[nr…` |  | codecov |
| `cohere-api-token` | Identified a Cohere Token, posing a risk of unauthorized access to AI services and data manipulation. | `[\w.-]{0,50}?(?i:[\w.-]{0,50}?(?:cohere\|CO_API_KEY)(?:[ \t\w.-]{0,20})[\s'"]{0,3})(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-zA-Z0-…` | 4 | cohere, co_api_key |
| `coinbase-access-token` | Detected a Coinbase Access Token, posing a risk of unauthorized access to cryptocurrency accounts and financial transac… | `(?i)[\w.-]{0,50}?(?:coinbase)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9_-]{64})(?:[\x60'"\s;]\|\\…` |  | coinbase |
| `confluent-access-token` | Identified a Confluent Access Token, which could compromise access to streaming data platforms and sensitive data flow. | `(?i)[\w.-]{0,50}?(?:confluent)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{16})(?:[\x60'"\s;]\|\\[…` |  | confluent |
| `confluent-secret-key` | Found a Confluent Secret Key, potentially risking unauthorized operations and data access within Confluent services. | `(?i)[\w.-]{0,50}?(?:confluent)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{64})(?:[\x60'"\s;]\|\\[…` |  | confluent |
| `contentful-delivery-api-token` | Discovered a Contentful delivery API token, posing a risk to content management systems and data integrity. | `(?i)[\w.-]{0,50}?(?:contentful)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9=_\-]{43})(?:[\x60'"\s;]…` |  | contentful |
| `curl-auth-header` | Discovered a potential authorization token provided in a curl command header, which could compromise the curl accessed … | `\bcurl\b(?:.*?\|.*?(?:[\r\n]{1,2}.*?){1,5})[ \t\n\r](?:-H\|--header)(?:=\|[ \t]{0,5})(?:"(?i)(?:Authorization:[ \t]{0,5}(?:Basic[ \t]([a-z0-9+/]{8,}=…` | 2.75 | curl |
| `curl-auth-user` | Discovered a potential basic authorization token provided in a curl command, which could compromise the curl accessed r… | `\bcurl\b(?:.*\|.*(?:[\r\n]{1,2}.*){1,5})[ \t\n\r](?:-u\|--user)(?:=\|[ \t]{0,5})("(:[^"]{3,}\|[^:"]{3,}:\|[^:"]{3,}:[^"]{3,})"\|'([^:']{3,}:[^']{3,})…` | 2 | curl |
| `databricks-api-token` | Uncovered a Databricks API token, which may compromise big data analytics platforms and sensitive data processing. | `\b(dapi[a-f0-9]{32}(?:-\d)?)(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | dapi |
| `datadog-access-token` | Detected a Datadog Access Token, potentially risking monitoring and analytics data exposure and manipulation. | `(?i)[\w.-]{0,50}?(?:datadog)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{40})(?:[\x60'"\s;]\|\\[nr…` |  | datadog |
| `defined-networking-api-token` | Identified a Defined Networking API token, which could lead to unauthorized network operations and data breaches. | `(?i)[\w.-]{0,50}?(?:dnkey)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(dnkey-[a-z0-9=_\-]{26}-[a-z0-9=_\-]{…` |  | dnkey |
| `digitalocean-access-token` | Found a DigitalOcean OAuth Access Token, risking unauthorized cloud resource access and data compromise. | `\b(doo_v1_[a-f0-9]{64})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | doo_v1_ |
| `digitalocean-pat` | Discovered a DigitalOcean Personal Access Token, posing a threat to cloud infrastructure security and data privacy. | `\b(dop_v1_[a-f0-9]{64})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | dop_v1_ |
| `digitalocean-refresh-token` | Uncovered a DigitalOcean OAuth Refresh Token, which could allow prolonged unauthorized access and resource manipulation. | `(?i)\b(dor_v1_[a-f0-9]{64})(?:[\x60'"\s;]\|\\[nr]\|$)` |  | dor_v1_ |
| `discord-api-token` | Detected a Discord API key, potentially compromising communication channels and user data privacy on Discord. | `(?i)[\w.-]{0,50}?(?:discord)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-f0-9]{64})(?:[\x60'"\s;]\|\\[nr…` |  | discord |
| `discord-client-id` | Identified a Discord client ID, which may lead to unauthorized integrations and data exposure in Discord applications. | `(?i)[\w.-]{0,50}?(?:discord)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([0-9]{18})(?:[\x60'"\s;]\|\\[nr]\|…` | 2 | discord |
| `discord-client-secret` | Discovered a potential Discord client secret, risking compromised Discord bot integrations and data leaks. | `(?i)[\w.-]{0,50}?(?:discord)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9=_\-]{32})(?:[\x60'"\s;]\|\…` | 2 | discord |
| `doppler-api-token` | Discovered a Doppler API token, posing a risk to environment and secrets management security. | `dp\.pt\.(?i)[a-z0-9]{43}` | 2 | dp.pt. |
| `droneci-access-token` | Detected a Droneci Access Token, potentially compromising continuous integration and deployment workflows. | `(?i)[\w.-]{0,50}?(?:droneci)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{32})(?:[\x60'"\s;]\|\\[nr…` |  | droneci |
| `dropbox-api-token` | Identified a Dropbox API secret, which could lead to unauthorized file access and data breaches in Dropbox storage. | `(?i)[\w.-]{0,50}?(?:dropbox)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{15})(?:[\x60'"\s;]\|\\[nr…` |  | dropbox |
| `dropbox-long-lived-api-token` | Found a Dropbox long-lived API token, risking prolonged unauthorized access to cloud storage and sensitive data. | `(?i)[\w.-]{0,50}?(?:dropbox)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{11}(AAAAAAAAAA)[a-z0-9\-_…` |  | dropbox |
| `dropbox-short-lived-api-token` | Discovered a Dropbox short-lived API token, posing a risk of temporary but potentially harmful data access and manipula… | `(?i)[\w.-]{0,50}?(?:dropbox)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(sl\.[a-z0-9\-=_]{135})(?:[\x60'"\s…` |  | dropbox |
| `duffel-api-token` | Uncovered a Duffel API token, which may compromise travel platform integrations and sensitive customer data. | `duffel_(?:test\|live)_(?i)[a-z0-9_\-=]{43}` | 2 | duffel_ |
| `dynatrace-api-token` | Detected a Dynatrace API token, potentially risking application performance monitoring and data exposure. | `dt0c01\.(?i)[a-z0-9]{24}\.[a-z0-9]{64}` | 4 | dt0c01. |
| `easypost-api-token` | Identified an EasyPost API token, which could lead to unauthorized postal and shipment service access and data exposure. | `\bEZAK(?i)[a-z0-9]{54}\b` | 2 | ezak |
| `easypost-test-api-token` | Detected an EasyPost test API token, risking exposure of test environments and potentially sensitive shipment data. | `\bEZTK(?i)[a-z0-9]{54}\b` | 2 | eztk |
| `etsy-access-token` | Found an Etsy Access Token, potentially compromising Etsy shop management and customer data. | `(?i)[\w.-]{0,50}?(?:(?-i:ETSY\|[Ee]tsy))(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{24})(?:[\x60'…` | 3 | etsy |
| `facebook-access-token` | Discovered a Facebook Access Token, posing a risk of unauthorized access to Facebook accounts and personal data exposur… | `(?i)\b(\d{15,16}(\\|\|%)[0-9a-z\-_]{27,40})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | facebook |
| `facebook-page-access-token` | Discovered a Facebook Page Access Token, posing a risk of unauthorized access to Facebook accounts and personal data ex… | `\b(EAA[MC](?i)[a-z0-9]{100,})(?:[\x60'"\s;]\|\\[nr]\|$)` | 4 | eaam, eaac |
| `facebook-secret` | Discovered a Facebook Application secret, posing a risk of unauthorized access to Facebook accounts and personal data e… | `(?i)[\w.-]{0,50}?(?:facebook)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-f0-9]{32})(?:[\x60'"\s;]\|\\[n…` | 3 | facebook |
| `fastly-api-token` | Uncovered a Fastly API key, which may compromise CDN and edge cloud services, leading to content delivery and security … | `(?i)[\w.-]{0,50}?(?:fastly)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9=_\-]{32})(?:[\x60'"\s;]\|\\…` |  | fastly |
| `finicity-api-token` | Detected a Finicity API token, potentially risking financial data access and unauthorized financial operations. | `(?i)[\w.-]{0,50}?(?:finicity)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-f0-9]{32})(?:[\x60'"\s;]\|\\[n…` |  | finicity |
| `finicity-client-secret` | Identified a Finicity Client Secret, which could lead to compromised financial service integrations and data breaches. | `(?i)[\w.-]{0,50}?(?:finicity)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{20})(?:[\x60'"\s;]\|\\[n…` |  | finicity |
| `finnhub-access-token` | Found a Finnhub Access Token, risking unauthorized access to financial market data and analytics. | `(?i)[\w.-]{0,50}?(?:finnhub)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{20})(?:[\x60'"\s;]\|\\[nr…` |  | finnhub |
| `flickr-access-token` | Discovered a Flickr Access Token, posing a risk of unauthorized photo management and potential data leakage. | `(?i)[\w.-]{0,50}?(?:flickr)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{32})(?:[\x60'"\s;]\|\\[nr]…` |  | flickr |
| `flutterwave-encryption-key` | Uncovered a Flutterwave Encryption Key, which may compromise payment processing and sensitive financial information. | `FLWSECK_TEST-(?i)[a-h0-9]{12}` | 2 | flwseck_test |
| `flutterwave-public-key` | Detected a Finicity Public Key, potentially exposing public cryptographic operations and integrations. | `FLWPUBK_TEST-(?i)[a-h0-9]{32}-X` | 2 | flwpubk_test |
| `flutterwave-secret-key` | Identified a Flutterwave Secret Key, risking unauthorized financial transactions and data breaches. | `FLWSECK_TEST-(?i)[a-h0-9]{32}-X` | 2 | flwseck_test |
| `flyio-access-token` | Uncovered a Fly.io API key | `\b((?:fo1_[\w-]{43}\|fm1[ar]_[a-zA-Z0-9+\/]{100,}={0,3}\|fm2_[a-zA-Z0-9+\/]{100,}={0,3}))(?:[\x60'"\s;]\|\\[nr]\|$)` | 4 | fo1_, fm1, fm2_ |
| `frameio-api-token` | Found a Frame.io API token, potentially compromising video collaboration and project management. | `fio-u-(?i)[a-z0-9\-_=]{64}` |  | fio-u- |
| `freemius-secret-key` | Detected a Freemius secret key, potentially exposing sensitive information. | `(?i)["']secret_key["']\s*=>\s*["'](sk_[\S]{29})["']` |  | secret_key |
| `freshbooks-access-token` | Discovered a Freshbooks Access Token, posing a risk to accounting software access and sensitive financial data exposure. | `(?i)[\w.-]{0,50}?(?:freshbooks)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{64})(?:[\x60'"\s;]\|\\…` |  | freshbooks |
| `gcp-api-key` | Uncovered a GCP API key, which could lead to unauthorized access to Google Cloud services and data breaches. | `\b(AIza[\w-]{35})(?:[\x60'"\s;]\|\\[nr]\|$)` | 4 | aiza |
| `generic-api-key` | Detected a Generic API Key, potentially exposing access to various services and sensitive operations. | `(?i)[\w.-]{0,50}?(?:access\|auth\|(?-i:[Aa]pi\|API)\|credential\|creds\|key\|passw(?:or)?d\|secret\|token)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,…` | 3.5 | access, api, auth, key, credential, creds, passwd, password… |
| `github-app-token` | Identified a GitHub App Token, which may compromise GitHub application integrations and source code security. | `(?:ghu\|ghs)_[0-9a-zA-Z]{36}` | 3 | ghu_, ghs_ |
| `github-fine-grained-pat` | Found a GitHub Fine-Grained Personal Access Token, risking unauthorized repository access and code manipulation. | `github_pat_\w{82}` | 3 | github_pat_ |
| `github-oauth` | Discovered a GitHub OAuth Access Token, posing a risk of compromised GitHub account integrations and data leaks. | `gho_[0-9a-zA-Z]{36}` | 3 | gho_ |
| `github-pat` | Uncovered a GitHub Personal Access Token, potentially leading to unauthorized repository access and sensitive content e… | `ghp_[0-9a-zA-Z]{36}` | 3 | ghp_ |
| `github-refresh-token` | Detected a GitHub Refresh Token, which could allow prolonged unauthorized access to GitHub services. | `ghr_[0-9a-zA-Z]{36}` | 3 | ghr_ |
| `gitlab-cicd-job-token` | Identified a GitLab CI/CD Job Token, potential access to projects and some APIs on behalf of a user while the CI job is… | `glcbt-[0-9a-zA-Z]{1,5}_[0-9a-zA-Z_-]{20}` | 3 | glcbt- |
| `gitlab-deploy-token` | Identified a GitLab Deploy Token, risking access to repositories, packages and containers with write access. | `gldt-[0-9a-zA-Z_\-]{20}` | 3 | gldt- |
| `gitlab-feature-flag-client-token` | Identified a GitLab feature flag client token, risks exposing user lists and features flags used by an application. | `glffct-[0-9a-zA-Z_\-]{20}` | 3 | glffct- |
| `gitlab-feed-token` | Identified a GitLab feed token, risking exposure of user data. | `glft-[0-9a-zA-Z_\-]{20}` | 3 | glft- |
| `gitlab-incoming-mail-token` | Identified a GitLab incoming mail token, risking manipulation of data sent by mail. | `glimt-[0-9a-zA-Z_\-]{25}` | 3 | glimt- |
| `gitlab-kubernetes-agent-token` | Identified a GitLab Kubernetes Agent token, risking access to repos and registry of projects connected via agent. | `glagent-[0-9a-zA-Z_\-]{50}` | 3 | glagent- |
| `gitlab-oauth-app-secret` | Identified a GitLab OIDC Application Secret, risking access to apps using GitLab as authentication provider. | `gloas-[0-9a-zA-Z_\-]{64}` | 3 | gloas- |
| `gitlab-pat` | Identified a GitLab Personal Access Token, risking unauthorized access to GitLab repositories and codebase exposure. | `glpat-[\w-]{20}` | 3 | glpat- |
| `gitlab-pat-routable` | Identified a GitLab Personal Access Token (routable), risking unauthorized access to GitLab repositories and codebase e… | `\bglpat-[0-9a-zA-Z_-]{27,300}\.[0-9a-z]{2}[0-9a-z]{7}\b` | 4 | glpat- |
| `gitlab-ptt` | Found a GitLab Pipeline Trigger Token, potentially compromising continuous integration workflows and project security. | `glptt-[0-9a-f]{40}` | 3 | glptt- |
| `gitlab-rrt` | Discovered a GitLab Runner Registration Token, posing a risk to CI/CD pipeline integrity and unauthorized access. | `GR1348941[\w-]{20}` | 3 | gr1348941 |
| `gitlab-runner-authentication-token` | Discovered a GitLab Runner Authentication Token, posing a risk to CI/CD pipeline integrity and unauthorized access. | `glrt-[0-9a-zA-Z_\-]{20}` | 3 | glrt- |
| `gitlab-runner-authentication-token-routable` | Discovered a GitLab Runner Authentication Token (Routable), posing a risk to CI/CD pipeline integrity and unauthorized … | `\bglrt-t\d_[0-9a-zA-Z_\-]{27,300}\.[0-9a-z]{2}[0-9a-z]{7}\b` | 4 | glrt- |
| `gitlab-scim-token` | Discovered a GitLab SCIM Token, posing a risk to unauthorized access for a organization or instance. | `glsoat-[0-9a-zA-Z_\-]{20}` | 3 | glsoat- |
| `gitlab-session-cookie` | Discovered a GitLab Session Cookie, posing a risk to unauthorized access to a user account. | `_gitlab_session=[0-9a-z]{32}` | 3 | _gitlab_session= |
| `gitter-access-token` | Uncovered a Gitter Access Token, which may lead to unauthorized access to chat and communication services. | `(?i)[\w.-]{0,50}?(?:gitter)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9_-]{40})(?:[\x60'"\s;]\|\\[n…` |  | gitter |
| `gocardless-api-token` | Detected a GoCardless API token, potentially risking unauthorized direct debit payment operations and financial data ex… | `(?i)[\w.-]{0,50}?(?:gocardless)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(live_(?i)[a-z0-9\-_=]{40})(?:[\…` |  | live_, gocardless |
| `grafana-api-key` | Identified a Grafana API key, which could compromise monitoring dashboards and sensitive data analytics. | `(?i)\b(eyJrIjoi[A-Za-z0-9]{70,400}={0,3})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | eyjrijoi |
| `grafana-cloud-api-token` | Found a Grafana cloud API token, risking unauthorized access to cloud-based monitoring services and data exposure. | `(?i)\b(glc_[A-Za-z0-9+/]{32,400}={0,3})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | glc_ |
| `grafana-service-account-token` | Discovered a Grafana service account token, posing a risk of compromised monitoring services and data integrity. | `(?i)\b(glsa_[A-Za-z0-9]{32}_[A-Fa-f0-9]{8})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | glsa_ |
| `harness-api-key` | Identified a Harness Access Token (PAT or SAT), risking unauthorized access to a Harness account. | `(?:pat\|sat)\.[a-zA-Z0-9_-]{22}\.[a-zA-Z0-9]{24}\.[a-zA-Z0-9]{20}` |  | pat., sat. |
| `hashicorp-tf-api-token` | Uncovered a HashiCorp Terraform user/org API token, which may lead to unauthorized infrastructure management and securi… | `(?i)[a-z0-9]{14}\.(?-i:atlasv1)\.[a-z0-9\-_=]{60,70}` | 3.5 | atlasv1 |
| `hashicorp-tf-password` | Identified a HashiCorp Terraform password field, risking unauthorized infrastructure configuration and security breache… | `(?i)[\w.-]{0,50}?(?:administrator_login_password\|password)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}("[a…` | 2 | administrator_login_password, password |
| `heroku-api-key` | Detected a Heroku API Key, potentially compromising cloud application deployments and operational security. | `(?i)[\w.-]{0,50}?(?:heroku)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}…` |  | heroku |
| `heroku-api-key-v2` | Detected a Heroku API Key, potentially compromising cloud application deployments and operational security. | `\b((HRKU-AA[0-9a-zA-Z_-]{58}))(?:[\x60'"\s;]\|\\[nr]\|$)` | 4 | hrku-aa |
| `hubspot-api-key` | Found a HubSpot API Token, posing a risk to CRM data integrity and unauthorized marketing operations. | `(?i)[\w.-]{0,50}?(?:hubspot)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4…` |  | hubspot |
| `huggingface-access-token` | Discovered a Hugging Face Access token, which could lead to unauthorized access to AI models and sensitive data. | `\b(hf_(?i:[a-z]{34}))(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | hf_ |
| `huggingface-organization-api-token` | Uncovered a Hugging Face Organization API token, potentially compromising AI organization accounts and associated data. | `\b(api_org_(?i:[a-z]{34}))(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | api_org_ |
| `infracost-api-token` | Detected an Infracost API Token, risking unauthorized access to cloud cost estimation tools and financial data. | `\b(ico-[a-zA-Z0-9]{32})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | ico- |
| `intercom-api-key` | Identified an Intercom API Token, which could compromise customer communication channels and data privacy. | `(?i)[\w.-]{0,50}?(?:intercom)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9=_\-]{60})(?:[\x60'"\s;]\|…` |  | intercom |
| `intra42-client-secret` | Found a Intra42 client secret, which could lead to unauthorized access to the 42School API and sensitive data. | `\b(s-s4t2(?:ud\|af)-(?i)[abcdef0123456789]{64})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | intra, s-s4t2ud-, s-s4t2af- |
| `jfrog-api-key` | Found a JFrog API Key, posing a risk of unauthorized access to software artifact repositories and build pipelines. | `(?i)[\w.-]{0,50}?(?:jfrog\|artifactory\|bintray\|xray)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]…` |  | jfrog, artifactory, bintray, xray |
| `jfrog-identity-token` | Discovered a JFrog Identity Token, potentially compromising access to JFrog services and sensitive software artifacts. | `(?i)[\w.-]{0,50}?(?:jfrog\|artifactory\|bintray\|xray)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]…` |  | jfrog, artifactory, bintray, xray |
| `jwt` | Uncovered a JSON Web Token, which may lead to unauthorized access to web applications and sensitive user data. | `\b(ey[a-zA-Z0-9]{17,}\.ey[a-zA-Z0-9\/\\_-]{17,}\.(?:[a-zA-Z0-9\/\\_-]{10,}={0,2})?)(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | ey |
| `jwt-base64` | Detected a Base64-encoded JSON Web Token, posing a risk of exposing encoded authentication and data exchange informatio… | `\bZXlK(?:(?P<alg>aGJHY2lPaU)\|(?P<apu>aGNIVWlPaU)\|(?P<apv>aGNIWWlPaU)\|(?P<aud>aGRXUWlPaU)\|(?P<b64>aU5qUWlP)\|(?P<crit>amNtbDBJanBi)\|(?P<cty>amRIa…` | 2 | zxlk |
| `kraken-access-token` | Identified a Kraken Access Token, potentially compromising cryptocurrency trading accounts and financial security. | `(?i)[\w.-]{0,50}?(?:kraken)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9\/=_\+\-]{80,90})(?:[\x60'"\…` |  | kraken |
| `kubernetes-secret-yaml` | Possible Kubernetes Secret detected, posing a risk of leaking credentials/tokens from your deployments | `(?i)(?:\bkind:[ \t]*["']?\bsecret\b["']?(?s:.){0,200}?\bdata:(?s:.){0,100}?\s+([\w.-]+:(?:[ \t]*(?:\\|\|>[-+]?)\s+)?[ \t]*(?:["']?[a-z0-9+/]{10,}={0,…` |  | secret |
| `kucoin-access-token` | Found a Kucoin Access Token, risking unauthorized access to cryptocurrency exchange services and transactions. | `(?i)[\w.-]{0,50}?(?:kucoin)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-f0-9]{24})(?:[\x60'"\s;]\|\\[nr]…` |  | kucoin |
| `kucoin-secret-key` | Discovered a Kucoin Secret Key, which could lead to compromised cryptocurrency operations and financial data breaches. | `(?i)[\w.-]{0,50}?(?:kucoin)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}…` |  | kucoin |
| `launchdarkly-access-token` | Uncovered a Launchdarkly Access Token, potentially compromising feature flag management and application functionality. | `(?i)[\w.-]{0,50}?(?:launchdarkly)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9=_\-]{40})(?:[\x60'"\s…` |  | launchdarkly |
| `linear-api-key` | Detected a Linear API Token, posing a risk to project management tools and sensitive task data. | `lin_api_(?i)[a-z0-9]{40}` | 2 | lin_api_ |
| `linear-client-secret` | Identified a Linear Client Secret, which may compromise secure integrations and sensitive project management data. | `(?i)[\w.-]{0,50}?(?:linear)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-f0-9]{32})(?:[\x60'"\s;]\|\\[nr]…` | 2 | linear |
| `linkedin-client-id` | Found a LinkedIn Client ID, risking unauthorized access to LinkedIn integrations and professional data exposure. | `(?i)[\w.-]{0,50}?(?:linked[_-]?in)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{14})(?:[\x60'"\s;]\…` | 2 | linkedin, linked_in, linked-in |
| `linkedin-client-secret` | Discovered a LinkedIn Client secret, potentially compromising LinkedIn application integrations and user data. | `(?i)[\w.-]{0,50}?(?:linked[_-]?in)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{16})(?:[\x60'"\s;]\…` | 2 | linkedin, linked_in, linked-in |
| `lob-api-key` | Uncovered a Lob API Key, which could lead to unauthorized access to mailing and address verification services. | `(?i)[\w.-]{0,50}?(?:lob)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}((live\|test)_[a-f0-9]{35})(?:[\x60'"\s…` |  | test_, live_ |
| `lob-pub-api-key` | Detected a Lob Publishable API Key, posing a risk of exposing mail and print service integrations. | `(?i)[\w.-]{0,50}?(?:lob)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}((test\|live)_pub_[a-f0-9]{31})(?:[\x60…` |  | test_pub, live_pub, _pub |
| `looker-client-id` | Found a Looker Client ID, risking unauthorized access to a Looker account and exposing sensitive data. | `(?i)[\w.-]{0,50}?(?:looker)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{20})(?:[\x60'"\s;]\|\\[nr]…` |  | looker |
| `looker-client-secret` | Found a Looker Client Secret, risking unauthorized access to a Looker account and exposing sensitive data. | `(?i)[\w.-]{0,50}?(?:looker)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{24})(?:[\x60'"\s;]\|\\[nr]…` |  | looker |
| `mailchimp-api-key` | Identified a Mailchimp API key, potentially compromising email marketing campaigns and subscriber data. | `(?i)[\w.-]{0,50}?(?:MailchimpSDK.initialize\|mailchimp)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-f0-9…` |  | mailchimp |
| `mailgun-private-api-token` | Found a Mailgun private API token, risking unauthorized email service operations and data breaches. | `(?i)[\w.-]{0,50}?(?:mailgun)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(key-[a-f0-9]{32})(?:[\x60'"\s;]\|\…` |  | mailgun |
| `mailgun-pub-key` | Discovered a Mailgun public validation key, which could expose email verification processes and associated data. | `(?i)[\w.-]{0,50}?(?:mailgun)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(pubkey-[a-f0-9]{32})(?:[\x60'"\s;]…` |  | mailgun |
| `mailgun-signing-key` | Uncovered a Mailgun webhook signing key, potentially compromising email automation and data integrity. | `(?i)[\w.-]{0,50}?(?:mailgun)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-h0-9]{32}-[a-h0-9]{8}-[a-h0-9]{…` |  | mailgun |
| `mapbox-api-token` | Detected a MapBox API token, posing a risk to geospatial services and sensitive location data exposure. | `(?i)[\w.-]{0,50}?(?:mapbox)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(pk\.[a-z0-9]{60}\.[a-z0-9]{22})(?:[…` |  | mapbox |
| `mattermost-access-token` | Identified a Mattermost Access Token, which may compromise team communication channels and data privacy. | `(?i)[\w.-]{0,50}?(?:mattermost)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{26})(?:[\x60'"\s;]\|\\…` |  | mattermost |
| `maxmind-license-key` | Discovered a potential MaxMind license key. | `\b([A-Za-z0-9]{6}_[A-Za-z0-9]{29}_mmk)(?:[\x60'"\s;]\|\\[nr]\|$)` | 4 | _mmk |
| `messagebird-api-token` | Found a MessageBird API token, risking unauthorized access to communication platforms and message data. | `(?i)[\w.-]{0,50}?(?:message[_-]?bird)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{25})(?:[\x60'"\s…` |  | messagebird, message-bird, message_bird |
| `messagebird-client-id` | Discovered a MessageBird client ID, potentially compromising API integrations and sensitive communication data. | `(?i)[\w.-]{0,50}?(?:message[_-]?bird)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([0-9a-f]{8}-[0-9a-f]{4}-[…` |  | messagebird, message-bird, message_bird |
| `microsoft-teams-webhook` | Uncovered a Microsoft Teams Webhook, which could lead to unauthorized access to team collaboration tools and data leaks. | `https://[a-z0-9]+\.webhook\.office\.com/webhookb2/[a-z0-9]{8}-([a-z0-9]{4}-){3}[a-z0-9]{12}@[a-z0-9]{8}-([a-z0-9]{4}-){3}[a-z0-9]{12}/IncomingWebhook…` |  | webhook.office.com, webhookb2, incomingwebhook |
| `netlify-access-token` | Detected a Netlify Access Token, potentially compromising web hosting services and site management. | `(?i)[\w.-]{0,50}?(?:netlify)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9=_\-]{40,46})(?:[\x60'"\s;]…` |  | netlify |
| `new-relic-browser-api-token` | Identified a New Relic ingest browser API token, risking unauthorized access to application performance data and analyt… | `(?i)[\w.-]{0,50}?(?:new-relic\|newrelic\|new_relic)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(NRJS-[a-f0-…` |  | nrjs- |
| `new-relic-insert-key` | Discovered a New Relic insight insert key, compromising data injection into the platform. | `(?i)[\w.-]{0,50}?(?:new-relic\|newrelic\|new_relic)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(NRII-[a-z0-…` |  | nrii- |
| `new-relic-user-api-id` | Found a New Relic user API ID, posing a risk to application monitoring services and data integrity. | `(?i)[\w.-]{0,50}?(?:new-relic\|newrelic\|new_relic)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{64…` |  | new-relic, newrelic, new_relic |
| `new-relic-user-api-key` | Discovered a New Relic user API Key, which could lead to compromised application insights and performance monitoring. | `(?i)[\w.-]{0,50}?(?:new-relic\|newrelic\|new_relic)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(NRAK-[a-z0-…` |  | nrak |
| `notion-api-token` | Notion API token | `\b(ntn_[0-9]{11}[A-Za-z0-9]{32}[A-Za-z0-9]{3})(?:[\x60'"\s;]\|\\[nr]\|$)` | 4 | ntn_ |
| `npm-access-token` | Uncovered an npm access token, potentially compromising package management and code repository access. | `(?i)\b(npm_[a-z0-9]{36})(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | npm_ |
| `nuget-config-password` | Identified a password within a Nuget config file, potentially compromising package management access. | `(?i)<add key=\"(?:(?:ClearText)?Password)\"\s*value=\"(.{8,})\"\s*/>` | 1 | <add key= |
| `nytimes-access-token` | Detected a Nytimes Access Token, risking unauthorized access to New York Times APIs and content services. | `(?i)[\w.-]{0,50}?(?:nytimes\|new-york-times,\|newyorktimes)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-…` |  | nytimes, new-york-times, newyorktimes |
| `octopus-deploy-api-key` | Discovered a potential Octopus Deploy API key, risking application deployments and operational security. | `\b(API-[A-Z0-9]{26})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | api- |
| `okta-access-token` | Identified an Okta Access Token, which may compromise identity management services and user authentication data. | `[\w.-]{0,50}?(?i:[\w.-]{0,50}?(?:(?-i:[Oo]kta\|OKTA))(?:[ \t\w.-]{0,20})[\s'"]{0,3})(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(00[\w=\-…` | 4 | okta |
| `openai-api-key` | Found an OpenAI API Key, posing a risk of unauthorized access to AI services and data manipulation. | `\b(sk-(?:proj\|svcacct\|admin)-(?:[A-Za-z0-9_-]{74}\|[A-Za-z0-9_-]{58})T3BlbkFJ(?:[A-Za-z0-9_-]{74}\|[A-Za-z0-9_-]{58})\b\|sk-[a-zA-Z0-9]{20}T3BlbkFJ…` | 3 | t3blbkfj |
| `openshift-user-token` | Found an OpenShift user token, potentially compromising an OpenShift/Kubernetes cluster. | `\b(sha256~[\w-]{43})(?:[^\w-]\|\z)` | 3.5 | sha256~ |
| `perplexity-api-key` | Detected a Perplexity API key, which could lead to unauthorized access to Perplexity AI services and data exposure. | `\b(pplx-[a-zA-Z0-9]{48})(?:[\x60'"\s;]\|\\[nr]\|$\|\b)` | 4 | pplx- |
| `pkcs12-file` | Found a PKCS #12 file, which commonly contain bundled private keys. | `(?i)(?:^\|\/)[^\/]+\.p(?:12\|fx)$` |  |  |
| `plaid-api-token` | Discovered a Plaid API Token, potentially compromising financial data aggregation and banking services. | `(?i)[\w.-]{0,50}?(?:plaid)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(access-(?:sandbox\|development\|prod…` |  | plaid |
| `plaid-client-id` | Uncovered a Plaid Client ID, which could lead to unauthorized financial service integrations and data breaches. | `(?i)[\w.-]{0,50}?(?:plaid)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{24})(?:[\x60'"\s;]\|\\[nr]\…` | 3.5 | plaid |
| `plaid-secret-key` | Detected a Plaid Secret key, risking unauthorized access to financial accounts and sensitive transaction data. | `(?i)[\w.-]{0,50}?(?:plaid)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{30})(?:[\x60'"\s;]\|\\[nr]\…` | 3.5 | plaid |
| `planetscale-api-token` | Identified a PlanetScale API token, potentially compromising database management and operations. | `\b(pscale_tkn_(?i)[\w=\.-]{32,64})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | pscale_tkn_ |
| `planetscale-oauth-token` | Found a PlanetScale OAuth token, posing a risk to database access control and sensitive data integrity. | `\b(pscale_oauth_[\w=\.-]{32,64})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | pscale_oauth_ |
| `planetscale-password` | Discovered a PlanetScale password, which could lead to unauthorized database operations and data breaches. | `(?i)\b(pscale_pw_(?i)[\w=\.-]{32,64})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | pscale_pw_ |
| `postman-api-token` | Uncovered a Postman API token, potentially compromising API testing and development workflows. | `\b(PMAK-(?i)[a-f0-9]{24}\-[a-f0-9]{34})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | pmak- |
| `prefect-api-token` | Detected a Prefect API token, risking unauthorized access to workflow management and automation services. | `\b(pnu_[a-zA-Z0-9]{36})(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | pnu_ |
| `private-key` | Identified a Private Key, which may compromise cryptographic security and sensitive data encryption. | `(?i)-----BEGIN[ A-Z0-9_-]{0,100}PRIVATE KEY(?: BLOCK)?-----[\s\S-]{64,}?KEY(?: BLOCK)?-----` |  | -----begin |
| `privateai-api-token` | Identified a PrivateAI Token, posing a risk of unauthorized access to AI services and data manipulation. | `[\w.-]{0,50}?(?i:[\w.-]{0,50}?(?:private[_-]?ai)(?:[ \t\w.-]{0,20})[\s'"]{0,3})(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{32})…` | 3 | privateai, private_ai, private-ai |
| `pulumi-api-token` | Found a Pulumi API token, posing a risk to infrastructure as code services and cloud resource management. | `\b(pul-[a-f0-9]{40})(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | pul- |
| `pypi-upload-token` | Discovered a PyPI upload token, potentially compromising Python package distribution and repository integrity. | `pypi-AgEIcHlwaS5vcmc[\w-]{50,1000}` | 3 | pypi-ageichlwas5vcmc |
| `rapidapi-access-token` | Uncovered a RapidAPI Access Token, which could lead to unauthorized access to various APIs and data services. | `(?i)[\w.-]{0,50}?(?:rapidapi)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9_-]{50})(?:[\x60'"\s;]\|\\…` |  | rapidapi |
| `readme-api-token` | Detected a Readme API token, risking unauthorized documentation management and content exposure. | `\b(rdme_[a-z0-9]{70})(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | rdme_ |
| `rubygems-api-token` | Identified a Rubygem API token, potentially compromising Ruby library distribution and package management. | `\b(rubygems_[a-f0-9]{48})(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | rubygems_ |
| `scalingo-api-token` | Found a Scalingo API token, posing a risk to cloud platform services and application deployment security. | `\b(tk-us-[\w-]{48})(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | tk-us- |
| `sendbird-access-id` | Discovered a Sendbird Access ID, which could compromise chat and messaging platform integrations. | `(?i)[\w.-]{0,50}?(?:sendbird)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{…` |  | sendbird |
| `sendbird-access-token` | Uncovered a Sendbird Access Token, potentially risking unauthorized access to communication services and user data. | `(?i)[\w.-]{0,50}?(?:sendbird)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-f0-9]{40})(?:[\x60'"\s;]\|\\[n…` |  | sendbird |
| `sendgrid-api-token` | Detected a SendGrid API token, posing a risk of unauthorized email service operations and data exposure. | `\b(SG\.(?i)[a-z0-9=_\-\.]{66})(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | sg. |
| `sendinblue-api-token` | Identified a Sendinblue API token, which may compromise email marketing services and subscriber data privacy. | `\b(xkeysib-[a-f0-9]{64}\-(?i)[a-z0-9]{16})(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | xkeysib- |
| `sentry-access-token` | Found a Sentry.io Access Token (old format), risking unauthorized access to error tracking services and sensitive appli… | `(?i)[\w.-]{0,50}?(?:sentry)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-f0-9]{64})(?:[\x60'"\s;]\|\\[nr]…` | 3 | sentry |
| `sentry-org-token` | Found a Sentry.io Organization Token, risking unauthorized access to error tracking services and sensitive application … | `\bsntrys_eyJpYXQiO[a-zA-Z0-9+/]{10,200}(?:LCJyZWdpb25fdXJs\|InJlZ2lvbl91cmwi\|cmVnaW9uX3VybCI6)[a-zA-Z0-9+/]{10,200}={0,2}_[a-zA-Z0-9+/]{43}(?:[^a-zA…` | 4.5 | sntrys_eyjpyxqio |
| `sentry-user-token` | Found a Sentry.io User Token, risking unauthorized access to error tracking services and sensitive application data. | `\b(sntryu_[a-f0-9]{64})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3.5 | sntryu_ |
| `settlemint-application-access-token` | Found a Settlemint Application Access Token. | `\b(sm_aat_[a-zA-Z0-9]{16})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | sm_aat |
| `settlemint-personal-access-token` | Found a Settlemint Personal Access Token. | `\b(sm_pat_[a-zA-Z0-9]{16})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | sm_pat |
| `settlemint-service-access-token` | Found a Settlemint Service Access Token. | `\b(sm_sat_[a-zA-Z0-9]{16})(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | sm_sat |
| `shippo-api-token` | Discovered a Shippo API token, potentially compromising shipping services and customer order data. | `\b(shippo_(?:live\|test)_[a-fA-F0-9]{40})(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | shippo_ |
| `shopify-access-token` | Uncovered a Shopify access token, which could lead to unauthorized e-commerce platform access and data breaches. | `shpat_[a-fA-F0-9]{32}` | 2 | shpat_ |
| `shopify-custom-access-token` | Detected a Shopify custom access token, potentially compromising custom app integrations and e-commerce data security. | `shpca_[a-fA-F0-9]{32}` | 2 | shpca_ |
| `shopify-private-app-access-token` | Identified a Shopify private app access token, risking unauthorized access to private app data and store operations. | `shppa_[a-fA-F0-9]{32}` | 2 | shppa_ |
| `shopify-shared-secret` | Found a Shopify shared secret, posing a risk to application authentication and e-commerce platform security. | `shpss_[a-fA-F0-9]{32}` | 2 | shpss_ |
| `sidekiq-secret` | Discovered a Sidekiq Secret, which could lead to compromised background job processing and application data breaches. | `(?i)[\w.-]{0,50}?(?:BUNDLE_ENTERPRISE__CONTRIBSYS__COM\|BUNDLE_GEMS__CONTRIBSYS__COM)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\…` |  | bundle_enterprise__contribsys__com, bundle_gems__contribsys… |
| `sidekiq-sensitive-url` | Uncovered a Sidekiq Sensitive URL, potentially exposing internal job queues and sensitive operation details. | `(?i)\bhttps?://([a-f0-9]{8}:[a-f0-9]{8})@(?:gems.contribsys.com\|enterprise.contribsys.com)(?:[\/\|\#\|\?\|:]\|$)` |  | gems.contribsys.com, enterprise.contribsys.com |
| `slack-app-token` | Detected a Slack App-level token, risking unauthorized access to Slack applications and workspace data. | `(?i)xapp-\d-[A-Z0-9]+-\d+-[a-z0-9]+` | 2 | xapp |
| `slack-bot-token` | Identified a Slack Bot token, which may compromise bot integrations and communication channel security. | `xoxb-[0-9]{10,13}-[0-9]{10,13}[a-zA-Z0-9-]*` | 3 | xoxb |
| `slack-config-access-token` | Found a Slack Configuration access token, posing a risk to workspace configuration and sensitive data access. | `(?i)xoxe.xox[bp]-\d-[A-Z0-9]{163,166}` | 2 | xoxe.xoxb-, xoxe.xoxp- |
| `slack-config-refresh-token` | Discovered a Slack Configuration refresh token, potentially allowing prolonged unauthorized access to configuration set… | `(?i)xoxe-\d-[A-Z0-9]{146}` | 2 | xoxe- |
| `slack-legacy-bot-token` | Uncovered a Slack Legacy bot token, which could lead to compromised legacy bot operations and data exposure. | `xoxb-[0-9]{8,14}-[a-zA-Z0-9]{18,26}` | 2 | xoxb |
| `slack-legacy-token` | Detected a Slack Legacy token, risking unauthorized access to older Slack integrations and user data. | `xox[os]-\d+-\d+-\d+-[a-fA-F\d]+` | 2 | xoxo, xoxs |
| `slack-legacy-workspace-token` | Identified a Slack Legacy Workspace token, potentially compromising access to workspace data and legacy features. | `xox[ar]-(?:\d-)?[0-9a-zA-Z]{8,48}` | 2 | xoxa, xoxr |
| `slack-user-token` | Found a Slack User token, posing a risk of unauthorized user impersonation and data access within Slack workspaces. | `xox[pe](?:-[0-9]{10,13}){3}-[a-zA-Z0-9-]{28,34}` | 2 | xoxp-, xoxe- |
| `slack-webhook-url` | Discovered a Slack Webhook, which could lead to unauthorized message posting and data leakage in Slack channels. | `(?:https?://)?hooks.slack.com/(?:services\|workflows\|triggers)/[A-Za-z0-9+/]{43,56}` |  | hooks.slack.com |
| `snyk-api-token` | Uncovered a Snyk API token, potentially compromising software vulnerability scanning and code security. | `(?i)[\w.-]{0,50}?(?:snyk[_.-]?(?:(?:api\|oauth)[_.-]?)?(?:key\|token))(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s…` |  | snyk |
| `sonar-api-token` | Uncovered a Sonar API token, potentially compromising software vulnerability scanning and code security. | `(?i)[\w.-]{0,50}?(?:sonar[_.-]?(login\|token))(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}((?:squ_\|sqp_\|s…` |  | sonar |
| `sourcegraph-access-token` | Sourcegraph is a code search and navigation engine. | `(?i)\b(\b(sgp_(?:[a-fA-F0-9]{16}\|local)_[a-fA-F0-9]{40}\|sgp_[a-fA-F0-9]{40}\|[a-fA-F0-9]{40})\b)(?:[\x60'"\s;]\|\\[nr]\|$)` | 3 | sgp_, sourcegraph |
| `square-access-token` | Detected a Square Access Token, risking unauthorized payment processing and financial transaction exposure. | `\b((?:EAAA\|sq0atp-)[\w-]{22,60})(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | sq0atp-, eaaa |
| `squarespace-access-token` | Identified a Squarespace Access Token, which may compromise website management and content control on Squarespace. | `(?i)[\w.-]{0,50}?(?:squarespace)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-…` |  | squarespace |
| `stripe-access-token` | Found a Stripe Access Token, posing a risk to payment processing services and sensitive financial data. | `\b((?:sk\|rk)_(?:test\|live\|prod)_[a-zA-Z0-9]{10,99})(?:[\x60'"\s;]\|\\[nr]\|$)` | 2 | sk_test, sk_live, sk_prod, rk_test, rk_live, rk_prod |
| `sumologic-access-id` | Discovered a SumoLogic Access ID, potentially compromising log management services and data analytics integrity. | `[\w.-]{0,50}?(?i:[\w.-]{0,50}?(?:(?-i:[Ss]umo\|SUMO))(?:[ \t\w.-]{0,20})[\s'"]{0,3})(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(su[a-zA-…` | 3 | sumo |
| `sumologic-access-token` | Uncovered a SumoLogic Access Token, which could lead to unauthorized access to log data and analytics insights. | `(?i)[\w.-]{0,50}?(?:(?-i:[Ss]umo\|SUMO))(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{64})(?:[\x60'…` | 3 | sumo |
| `telegram-bot-api-token` | Detected a Telegram Bot API Token, risking unauthorized bot operations and message interception on Telegram. | `(?i)[\w.-]{0,50}?(?:telegr)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([0-9]{5,16}:(?-i:A)[a-z0-9_\-]{34})…` |  | telegr |
| `travisci-access-token` | Identified a Travis CI Access Token, potentially compromising continuous integration services and codebase security. | `(?i)[\w.-]{0,50}?(?:travis)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{22})(?:[\x60'"\s;]\|\\[nr]…` |  | travis |
| `twilio-api-key` | Found a Twilio API Key, posing a risk to communication services and sensitive customer interaction data. | `SK[0-9a-fA-F]{32}` | 3 | sk |
| `twitch-api-token` | Discovered a Twitch API token, which could compromise streaming services and account integrations. | `(?i)[\w.-]{0,50}?(?:twitch)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{30})(?:[\x60'"\s;]\|\\[nr]…` |  | twitch |
| `twitter-access-secret` | Uncovered a Twitter Access Secret, potentially risking unauthorized Twitter integrations and data breaches. | `(?i)[\w.-]{0,50}?(?:twitter)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{45})(?:[\x60'"\s;]\|\\[nr…` |  | twitter |
| `twitter-access-token` | Detected a Twitter Access Token, posing a risk of unauthorized account operations and social media data exposure. | `(?i)[\w.-]{0,50}?(?:twitter)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([0-9]{15,25}-[a-zA-Z0-9]{20,40})(?…` |  | twitter |
| `twitter-api-key` | Identified a Twitter API Key, which may compromise Twitter application integrations and user data security. | `(?i)[\w.-]{0,50}?(?:twitter)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{25})(?:[\x60'"\s;]\|\\[nr…` |  | twitter |
| `twitter-api-secret` | Found a Twitter API Secret, risking the security of Twitter app integrations and sensitive data access. | `(?i)[\w.-]{0,50}?(?:twitter)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{50})(?:[\x60'"\s;]\|\\[nr…` |  | twitter |
| `twitter-bearer-token` | Discovered a Twitter Bearer Token, potentially compromising API access and data retrieval from Twitter. | `(?i)[\w.-]{0,50}?(?:twitter)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(A{22}[a-zA-Z0-9%]{80,100})(?:[\x60…` |  | twitter |
| `typeform-api-token` | Uncovered a Typeform API token, which could lead to unauthorized survey management and data collection. | `(?i)[\w.-]{0,50}?(?:typeform)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(tfp_[a-z0-9\-_\.=]{59})(?:[\x60'"…` |  | tfp_ |
| `vault-batch-token` | Detected a Vault Batch Token, risking unauthorized access to secret management services and sensitive data. | `\b(hvb\.[\w-]{138,300})(?:[\x60'"\s;]\|\\[nr]\|$)` | 4 | hvb. |
| `vault-service-token` | Identified a Vault Service Token, potentially compromising infrastructure security and access to sensitive credentials. | `\b((?:hvs\.[\w-]{90,120}\|s\.(?i:[a-z0-9]{24})))(?:[\x60'"\s;]\|\\[nr]\|$)` | 3.5 | hvs., s. |
| `yandex-access-token` | Found a Yandex Access Token, posing a risk to Yandex service integrations and user data privacy. | `(?i)[\w.-]{0,50}?(?:yandex)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(t1\.[A-Z0-9a-z_-]+[=]{0,2}\.[A-Z0-9…` |  | yandex |
| `yandex-api-key` | Discovered a Yandex API Key, which could lead to unauthorized access to Yandex services and data manipulation. | `(?i)[\w.-]{0,50}?(?:yandex)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(AQVN[A-Za-z0-9_\-]{35,38})(?:[\x60'…` |  | yandex |
| `yandex-aws-access-token` | Uncovered a Yandex AWS Access Token, potentially compromising cloud resource access and data security on Yandex Cloud. | `(?i)[\w.-]{0,50}?(?:yandex)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}(YC[a-zA-Z0-9_\-]{38})(?:[\x60'"\s;]…` |  | yandex |
| `zendesk-secret-key` | Detected a Zendesk Secret Key, risking unauthorized access to customer support services and sensitive ticketing data. | `(?i)[\w.-]{0,50}?(?:zendesk)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=\|>\|:{1,3}=\|\\|\\|\|:\|=>\|\?=\|,)[\x60'"\s=]{0,5}([a-z0-9]{40})(?:[\x60'"\s;]\|\\[nr…` |  | zendesk |

# 附录 B：Nosey Parker 内置规则清单

来源：`praetorian-inc/noseyparker` 的 `crates/noseyparker/data/default/builtin/rules/*.yml`（master，抓取于 2026-10-14）。
共 189 条规则，分布在 87 个规则文件。
Nosey Parker 的规则是 `(?x)` 扩展模式（忽略空白、允许 `#` 注释），下方已压缩为单行。

| 规则 id | 名称 | 正则 | 分类 | 规则文件 |
| --- | --- | --- | --- | --- |
| `np.adafruit.1` | Adafruit IO Key | `(?x) \b (aio\_[a-zA-Z0-9]{28}) \b` | api, secret | adafruitio.yml |
| `np.adobe.1` | Adobe OAuth Client Secret | `(?x)(?i) \b (p8e-[a-z0-9-]{32}) (?:[^a-z0-9-]\|$)` | api, secret | adobe.yml |
| `np.age.1` | Age Recipient (X25519 public key) | `\b(age1[0-9a-z]{58})\b` | identifier | age.yml |
| `np.age.2` | Age Identity (X22519 secret key) | `\b(AGE-SECRET-KEY-1[0-9A-Z]{58})\b` | secret | age.yml |
| `np.anthropic.1` | Anthropic API Key | `(?x) \b (sk-ant-api[0-9]{2}-[a-zA-Z0-9_-]{95}) (?: [^a-zA-Z0-9_-] \| $ )` | api, secret | anthropic.yml |
| `np.artifactory.1` | Artifactory API Key | `(?i)artifactory.{0,50}\b([a-z0-9]{73})\b` | api, fuzzy, secret | artifactory.yml |
| `np.atlassian.1` | Atlassian Cloud API Token | `(?x) \b( ATATT3xFfGF0 [a-zA-Z0-9_-]{20,200} =[0-9A-F]{8} )\b` | api, secret | atlassian.yml |
| `np.auth0.1` | Auth0 Application Credentials | `(?x)(?s) (?: AUTH0_DOMAIN \| AUTH0_ISSUER_BASE_URL ) \s* = \s* ['"] ( (?:https?://)? [a-zA-Z0-9._-]{10,100}) ['"] .{0,100} AUTH0_CLIENT_ID \s* = \s* ['"] ([a-zA-Z0-9]{32}) ['"] .{0,100} AUTH0_CLIENT_SECRET \s* = \s* ['"…` | api, secret, fuzzy | auth0.yml |
| `np.aws.1` | AWS API Key | `\b((?:A3T[A-Z0-9]\|AKIA\|AGPA\|AIDA\|AROA\|AIPA\|ANPA\|ANVA\|ASIA)[A-Z0-9]{16})\b` | api, identifier | aws.yml |
| `np.aws.2` | AWS Secret Access Key | `(?x)(?i) \b aws_? (?:secret)? _? (?:access)? _? (?:key)? ["'']? \s{0,30} (?::\|=>\|=) \s{0,30} ["'']? ([a-z0-9/+=]{40}) (?: [^a-z0-9/+=] \| $ )` | api, fuzzy, secret | aws.yml |
| `np.aws.3` | AWS Account ID | `(?i)aws_?(?:account)_?(?:id)?["'`]?\s{0,30}(?::\|=>\|=)\s{0,30}["'`]?([0-9]{4}-?[0-9]{4}-?[0-9]{4})` | api, fuzzy, identifier | aws.yml |
| `np.aws.4` | AWS Session Token | `(?i)(?:aws.?session\|aws.?session.?token\|aws.?token)["'`]?\s{0,30}(?::\|=>\|=)\s{0,30}["'`]?([a-z0-9/+=]{16,200})[^a-z0-9/+=]` | api, fuzzy, secret | aws.yml |
| `np.aws.5` | Amazon MWS Auth Token | `(?i)(amzn\.mws\.[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})` | api | aws.yml |
| `np.s3.1` | AWS S3 Bucket | `(?x) (?: ^ \| [\s/"'] \| %2F ) ( (?: [a-zA-Z0-9_-]+ \. )+ (?# bucket name as subdomain ) (?: s3 \| s3-af-south-1 \| s3-ap-east-1 \| s3-ap-northeast-1 \| s3-ap-northeast-2 \| s3-ap-northeast-3 \| s3-ap-south-1 \| s3-ap-s…` | api, identifier | aws.yml |
| `np.s3.2` | AWS S3 Bucket | `(?x) (?: ^ \| [\s/"'] \| %2F ) ( (?: s3 \| s3-af-south-1 \| s3-ap-east-1 \| s3-ap-northeast-1 \| s3-ap-northeast-2 \| s3-ap-northeast-3 \| s3-ap-south-1 \| s3-ap-south-2 \| s3-ap-southeast-1 \| s3-ap-southeast-2 \| s3-a…` | api, identifier | aws.yml |
| `np.arn.1` | Amazon Resource Name | `(?x) \b ( arn : (?: aws \| aws-cn \| aws-us-gov ) (?# partition ) : [a-zA-Z0-9_-]{2,} (?# service ) : (?: af-south-1 \| ap-east-1 \| ap-northeast-1 \| ap-northeast-2 \| ap-northeast-3 \| ap-south-1 \| ap-south-2 \| ap-s…` | api, identifier | aws.yml |
| `np.appsync.1` | AWS AppSync API Key | `(?x) \b (da2-[a-z0-9]{26}) \b` | api, secret | aws.yml |
| `np.aws.6` | AWS API Credentials | `(?x) \b ((?:A3T[A-Z0-9]\|AKIA\|AGPA\|AIDA\|AROA\|AIPA\|ANPA\|ANVA\|ASIA)[A-Z0-9]{16}) (?# API key ) \b (?: (?s) .{0,40} ) (?# Arbitrary intermediate stuff; ?s causes . to match newlines ) \b ([A-Za-z0-9/+=]{40}) (?# sec…` | api, fuzzy, secret | aws.yml |
| `np.azure.1` | Azure Connection String | `(?x) (?i) (?: AccountName \| SharedAccessKeyName \| SharedSecretIssuer) \s*=\s* ([^${;<\[.\s*"'\#] [^.;\n"'\#]{2,80}) \s*;\s* .{0,10}\s* (?# possible extra stuff, e.g., string concatenation) (?: AccountKey \| SharedAcce…` | api, fuzzy, secret | azure.yml |
| `np.azure.2` | Azure App Configuration Connection String | `(?x) (https://[a-zA-Z0-9-]+\.azconfig\.io); Id=(.{4}-.{2}-.{2}:[a-zA-Z0-9+/]{18,22}); Secret=([a-zA-Z0-9+/]{36,50}=)` | api, fuzzy, secret | azure.yml |
| `np.azure.3` | Azure DevOps Personal Access Token | `(?x) (?i: ADO_PAT \| pat_token \| personal_?access_?token \| \$token ) \s* = \s* ["'] ([a-z0-9]{52}) ["']` | api, fuzzy, secret | azure.yml |
| `np.azure.4` | Azure DevOps Personal Access Token | `\b([a-zA-Z0-9]{76}AZDO[a-zA-Z0-9]{4})\b` | api, secret | azure.yml |
| `np.bitbucket.1` | Bitbucket App Password | `(?x) \b (ATBB[a-zA-Z0-9]{32}) \b` | api, secret | bitbucket.yml |
| `np.blynk.1` | Blynk Device Access Token | `(?x) https://(?:fra1\.\|lon1\.\|ny3\.\|sgp1\.\|blr1\.)*blynk\.cloud/external/api/[a-zA-Z0-9/]*\?token= ([a-zA-Z0-9_\-]{32}) &` | api, secret | blynk.yml |
| `np.blynk.2` | Blynk Organization Access Token | `(?x) https://(?:fra1\.\|lon1\.\|ny3\.\|sgp1\.\|blr1\.)*blynk\.cloud/api/[a-zA-Z0-9_\-\s/\\]* -H\s*"Authorization:\s*Bearer\s* ([a-zA-Z0-9_\-]{40}) "` | api, secret | blynk.yml |
| `np.blynk.3` | Blynk Organization Access Token | `(?x) -H\s*"Authorization:\s*Bearer\s* ([a-zA-Z0-9_\-]{40}) "[\s\\]*https://(?:fra1\.\|lon1\.\|ny3\.\|sgp1\.\|blr1\.)*blynk\.cloud/api` | api, secret | blynk.yml |
| `np.blynk.8` | Blynk Organization Client Credentials | `(?x) https://(?:fra1\.\|lon1\.\|ny3\.\|sgp1\.\|blr1\.)*blynk\.cloud/oauth2/[a-zA-Z0-9_\-\s/\\?=&]* (oa2-client-id_[a-zA-Z0-9_\-]{32}) (?: : \| &client_secret= ) ([a-zA-Z0-9_\-]{40})` | api, secret | blynk.yml |
| `np.blynk.9` | Blynk Organization Client Credentials | `(?x) \b (oa2-client-id_[a-zA-Z0-9_\-]{32}) :([a-zA-Z0-9_\-]{40}) [\s\\]*https://(?:fra1\.\|lon1\.\|ny3\.\|sgp1\.\|blr1\.)*blynk\.cloud/oauth2` | api, secret | blynk.yml |
| `np.codeclimate.1` | CodeClimate Reporter ID | `(?x) (?: CODECLIMATE_REPO_TOKEN \| CC_TEST_REPORTER_ID) \s* [:=] \s* ([a-f0-9]{64})\b` | api, fuzzy, secret | codeclimate.yml |
| `np.cratesio.1` | crates.io API Key | `\b(cio[a-zA-Z0-9]{32})\b` | api, secret | crates.io.yml |
| `np.databricks.1` | Databricks Personal Access Token | `(?x) \b ( dapi[a-f0-9]{32} (?: -[0-9]+ )? (?# optional -NUM suffix ) ) \b` | api, secret | databricks.yml |
| `np.dtrack.1` | Dependency-Track API Key | `\b(odt_[A-Za-z0-9]{32,255})\b` | api, secret | dependency_track.yml |
| `np.digitalocean.1` | DigitalOcean Application Access Token | `(?x)(?i) \b (doo_v1_[a-f0-9]{64}) \b` | api, secret | digitalocean.yml |
| `np.digitalocean.2` | DigitalOcean Personal Access Token | `(?x)(?i) \b (dop_v1_[a-f0-9]{64}) \b` | api, secret | digitalocean.yml |
| `np.digitalocean.3` | DigitalOcean Refresh Token | `(?x)(?i) \b (dor_v1_[a-f0-9]{64}) \b` | api, secret | digitalocean.yml |
| `np.django.1` | Django Secret Key | `(?x) \#\ SECURITY\ WARNING:\ keep\ the\ secret\ key\ used\ in\ production\ secret! \s* .{0,5} SECRET_KEY \s* = \s* r?["'] ([^"'\n]{5,100}) ["']` | fuzzy, secret | django.yml |
| `np.dockerhub.1` | Docker Hub Personal Access Token | `(?x) \b (dckr_pat_[a-zA-Z0-9_-]{27}) (?: $ \| [^a-zA-Z0-9_-] )` | api, secret | dockerhub.yml |
| `np.doppler.1` | Doppler CLI Token | `(?x) \b (dp\.ct\.[a-zA-Z0-9]{40,44}) \b` | api, secret | doppler.yml |
| `np.doppler.2` | Doppler Personal Token | `(?x) \b (dp\.pt\.[a-zA-Z0-9]{40,44}) \b` | api, secret | doppler.yml |
| `np.doppler.3` | Doppler Service Token | `(?x) \b (dp\.st\.(?:[a-z0-9\-_]{2,35}\.)?[a-zA-Z0-9]{40,44}) \b` | api, secret | doppler.yml |
| `np.doppler.4` | Doppler Service Account Token | `(?x) \b (dp\.sa\.[a-zA-Z0-9]{40,44}) \b` | api, secret | doppler.yml |
| `np.doppler.5` | Doppler SCIM Token | `(?x) \b (dp\.scim\.[a-zA-Z0-9]{40,44}) \b` | api, secret | doppler.yml |
| `np.doppler.6` | Doppler Audit Token | `(?x) \b (dp\.audit\.[a-zA-Z0-9]{40,44}) \b` | api, secret | doppler.yml |
| `np.dropbox.1` | Dropbox Access Token | `(?x) \b (sl\.[a-zA-Z0-9_-]{130,152}) (?: $ \| [^a-zA-Z0-9_-] )` | api, secret | dropbox.yml |
| `np.dynatrace.1` | Dynatrace Token | `\b(dt0[a-zA-Z]{1}[0-9]{2}\.[A-Z0-9]{24}\.[A-Z0-9]{64})\b` | api, secret | dynatrace.yml |
| `np.facebook.1` | Facebook Secret Key | `(?x)(?i) \b (?: facebook \| fb ) .? (?: api \| app \| application \| client \| consumer \| customer \| secret \| key ) .? (?: key \| oauth \| sec \| secret )? .{0,2} \s{0,20} .{0,2} \s{0,20} .{0,2} (?# string terminator…` | api, fuzzy, secret | facebook.yml |
| `np.facebook.2` | Facebook Access Token | `\b(EAACEdEose0cBA[a-zA-Z0-9]+)\b` | api, secret | facebook.yml |
| `np.figma.1` | Figma Personal Access Token | `(?x)(?i) figma.{0,20} \b ( [0-9a-f]{4}- [0-9a-f]{8}- [0-9a-f]{4}- [0-9a-f]{4}- [0-9a-f]{4}- [0-9a-f]{12} ) \b` | api, fuzzy, secret | figma.yml |
| `np.firecrawl.1` | Firecrawl API Key | `\b(fc-[a-f0-9]{32})\b` | api, secret | firecrawl.yml |
| `np.generic.1` | Generic Secret | `(?x)(?i) secret .{0,20} \b ([0-9a-z]{32,64}) \b` | fuzzy, generic, secret | generic.yml |
| `np.generic.2` | Generic API Key | `(?x)(?i) (?: api_key \| apikey \| access_key \| accesskey ) .{0,3} [\ \t]* (?: : \| = \| := \| => \| , \| ' \| " ) [\ \t]* .{0,3} \b ([0-9a-z][0-9a-z\-._/+]{30,62}[0-9a-z]) \b` | fuzzy, generic, secret | generic.yml |
| `np.generic.3` | Generic Username and Password | `(?x) (?: username \| USERNAME \| user \| USER) (?# username context keyword ) [\ \t]* = [\ \t]* (?# assignment / binder, sans newline ) ["'] ([a-zA-Z0-9.@_\-+]{3,30}) ["'] (?# quoted username ) \s* [,;]? \s* (?# optiona…` | fuzzy, generic, secret | generic.yml |
| `np.generic.4` | Generic Username and Password | `(?x) (?: username \| USERNAME \| Username \| UserName \| user \| USER) (?# username context keyword ) [\ \t]* = [\ \t]* (?# assignment / binder, sans newline ) ( [a-zA-Z][a-zA-Z0-9_+-]{2,29} \| [a-zA-Z][a-zA-Z0-9._+-]{2…` | fuzzy, generic, secret | generic.yml |
| `np.generic.5` | Generic Password | `(?x)(?i) password["']? (?# preceding context ) [\ \t]* (?: = \| : \| := \| => ) [\ \t]* (?# binder ) " ([^$<%@.,\s+'"(){}&/\#\-][^\s+'"(){}/]{4,}) (?# password ) "` | fuzzy, generic, secret | generic.yml |
| `np.generic.6` | Generic Password | `(?x)(?i) password["']? (?# preceding context ) [\ \t]* (?: = \| : \| := \| => ) [\ \t]* (?# binder ) ' ([^$<%@.,\s+'"(){}&/\#\-][^\s+'"(){}/]{4,}) (?# password ) '` | fuzzy, generic, secret | generic.yml |
| `np.generic.7` | Credentials in .NET System.Net.NetworkCredential | `(?x) \b (?: NetworkCredential \| NETWORKCREDENTIAL \| networkcredential ) \s* \( \s* " ( [^"\n]{3,50} ) " (?# username ) \s* , \s* " ( [^"\n]{3,50} ) " (?# password ) \s* \)` | fuzzy, generic, secret | generic.yml |
| `np.generic.8` | Credentials in .NET System.DirectoryServices.DirectoryEntry | `(?x) \b (?: DirectoryEntry \| DIRECTORYENTRY \| directoryentry ) \s* \( \s* " ( [^"\n]{3,100} ) " (?# path ) \s* , \s* " ( [^"\n]{3,50} ) " (?# username ) \s* , \s* " ( [^"\n]{3,50} ) " (?# password ) (?: \s* , \s* [^)]…` | fuzzy, generic, secret | generic.yml |
| `np.generic.9` | Sensitive Value in .NET Configuration | `(?x) < \s* add \s+ key \s* = \s* " ( [^\n"]{0,20} (?: pass \| Pass \| PASS \| secret \| Secret \| SECRET \| user \| User \| USER \| admin \| Admin \| ADMIN \| token \| Token \| TOKEN ) [^\n"]{0,20} ) " \s+ value \s* = \…` | fuzzy, generic, secret | generic.yml |
| `np.generic.10` | Connection String in .NET Configuration | `(?x) < \s* add \s+ key \s* = \s* " ( [^\n"]{0,20} (?: [Cc]onnection[Ss]tring \| CONNECTIONSTRING ) [^\n"]{0,20} ) " \s+ value \s* = \s* " ([^\n"]{3,200}) " \s* / \s* >` | fuzzy, generic, secret | generic.yml |
| `np.generic.11` | Generic Password | `(?x)(?i) the\ (?: default\ )? password\ is\ ' ( [^'\n\#$\|"{<+\ ()\\] (?# more restrictive first character to eliminate common interpolation forms ) [^'\n]{3,60} )'` | fuzzy, generic, secret | generic.yml |
| `np.generic.12` | Generic Password | `(?x)(?i) the\ (?: default\ )? password\ is\ " ( [^'\n\#$\|"{<+\ ()\\] (?# restrictive first character to suppress common interpolation forms ) [^"\n]{3,60} )"` | fuzzy, generic, secret | generic.yml |
| `np.generic.13` | Generic Username and Password | `(?x)(?i) the\ (?: default\ )? password\ for\ (?: the\ )? (?: user\|username ) \ ' ( [^'\n\#$\|"{<+\ ()\\] (?# restrictive first character to suppress common interpolation forms ) [^'\n]{2,24} )' \ is\ ' ( [^'\n\#$\|"{<+…` | fuzzy, generic, secret | generic.yml |
| `np.generic.14` | Generic Username and Password | `(?x)(?i) the\ (?: default\ )? password\ for\ (?: the\ )? (?: user\|username ) \ " ( [^'\n\#$\|"{<+\ ()\\] (?# restrictive first character to suppress common interpolation forms ) [^"\n]{2,24} )" \ is\ " ( [^'\n\#$\|"{<+…` | fuzzy, generic, secret | generic.yml |
| `np.generic.15` | Generic Secret | `(?x)(?i) \b if \b .{0,50} \b (?: secret \| pass \| password \| challenge ) \b \s* (?: == \| != \| === \| !== ) \s* " ( [^"\n]{3,60} ) "` | fuzzy, generic, secret | generic.yml |
| `np.generic.16` | Generic Secret | `(?x)(?i) \b if \b .{0,50} \b (?: secret \| pass \| password \| challenge ) \b \s* (?: == \| != \| === \| !== ) \s* ' ( [^'\n]{3,60} ) '` | fuzzy, generic, secret | generic.yml |
| `np.gitalk.1` | Gitalk OAuth Credentials | `(?x) \b new \s+ Gitalk \s* \( \s* \{ \s* clientID: \s* '([a-f0-9]{20})', \s* clientSecret: \s* '([a-f0-9]{40})',` | api, secret, fuzzy | gitalk.yml |
| `np.github.1` | GitHub Personal Access Token | `\b(ghp_[a-zA-Z0-9]{36})\b` | api, secret | github.yml |
| `np.github.2` | GitHub OAuth Access Token | `\b(gho_[a-zA-Z0-9]{36})\b` | api, secret | github.yml |
| `np.github.3` | GitHub App Token | `\b((?:ghu\|ghs)_[a-zA-Z0-9]{36})\b` | api, secret | github.yml |
| `np.github.4` | GitHub Refresh Token | `\b(ghr_[a-zA-Z0-9]{76})\b` | api, secret | github.yml |
| `np.github.5` | GitHub Client ID | `(?x)(?i) (?:github) .? (?: api \| app \| application \| client \| consumer \| customer )? .? (?: id \| identifier \| key ) .{0,2} \s{0,20} .{0,2} \s{0,20} .{0,2} (?# string terminators, whitespace, binding operators, et…` | api, fuzzy, identifier | github.yml |
| `np.github.6` | GitHub Secret Key | `(?x)(?i) github .? (?: api \| app \| application \| client \| consumer \| customer \| secret \| key ) .? (?: key \| oauth \| sec \| secret )? .{0,2} \s{0,20} .{0,2} \s{0,20} .{0,2} (?# string terminators, whitespace, bi…` | api, fuzzy, secret | github.yml |
| `np.github.7` | GitHub Personal Access Token (fine-grained permissions) | `(?x) \b (github_pat_[0-9a-zA-Z_]{82}) \b` | api, secret | github.yml |
| `np.gitlab.1` | GitLab Runner Registration Token | `\b(GR1348941[0-9a-zA-Z_-]{20})(?:\b\|$)` | api, secret | gitlab.yml |
| `np.gitlab.2` | GitLab Personal Access Token | `\b(glpat-[0-9a-zA-Z_-]{20})(?:\b\|$)` | api, secret | gitlab.yml |
| `np.gitlab.3` | GitLab Pipeline Trigger Token | `\b(glptt-[0-9a-f]{40})\b` | api, secret | gitlab.yml |
| `np.google.1` | Google Client ID | `(?i)\b([0-9]+-[a-z0-9_]{32})\.apps\.googleusercontent\.com` | api, identifier | google.yml |
| `np.google.2` | Google OAuth Client Secret | `(?x) \b (GOCSPX-[a-zA-Z0-9_-]{28}) (?:[^a-zA-Z0-9_-] \| $)` | api, secret | google.yml |
| `np.google.3` | Google OAuth Client Secret | `(?x)(?i) client.?secret .{0,10} \b ([a-z0-9_-]{24}) (?: [^a-z0-9_-] \|$)` | api, fuzzy, secret | google.yml |
| `np.google.4` | Google OAuth Access Token | `(?x) \b (ya29\.[0-9A-Za-z_-]{20,1024}) (?: [^0-9A-Za-z_-]\|$) (?# Used instead of `\b` because that doesn't play well with trailing `-` )` | api, secret | google.yml |
| `np.google.5` | Google API Key | `(?x) \b (AIza[0-9A-Za-z_-]{35}) (?: [^0-9A-Za-z_-] \| $ )` | api, secret | google.yml |
| `np.gcs.1` | Google Cloud Storage Bucket | `(?x) (?: ^ \| [\s/"'] \| %2F ) ( (?: [a-zA-Z0-9_-]+ \. )+ (?# bucket name as subdomain ) storage\.googleapis\.com ) \b` | api, identifier | google.yml |
| `np.gcs.2` | Google Cloud Storage Bucket | `(?x) (?: ^ \| [\s/"'] \| %2F ) ( storage\.googleapis\.com / [a-zA-Z0-9_-]+ (?: \. [a-zA-Z0-9_-]+ )* (?# bucket name as path ) ) (?: [^a-zA-Z0-9_-] \| $ ) (?# this instead of a \b anchor because that doesn't play nicely …` | api, identifier | google.yml |
| `np.google.6` | Google OAuth Credentials | `(?x) \b ([0-9]+-[a-z0-9_]{32}\.apps\.googleusercontent\.com) (?# client ID ) (?: (?s) .{0,40} ) (?# Arbitrary intermediate stuff; ?s causes . to match newlines ) \b (?: (?i) client.?secret .{0,10} )? ( (?:GOCSPX-[a-zA-Z…` | api, fuzzy, secret | google.yml |
| `np.gradle.1` | Hardcoded Gradle Credentials | `(?x) (?i) credentials \s* \{ (?:\s*//.*)* (?# skip line comments) \s* (?:username\|password) \s+ ['"]([^'"]{1,60})['"] (?:\s*//.*)* (?# skip line comments) \s* (?:username\|password) \s+ ['"]([^'"]{1,60})['"]` | fuzzy, secret | gradle.yml |
| `np.grafana.1` | Grafana API Token | `(?x) \b (eyJrIjoi [A-Za-z0-9]{60,100}) \b` | api, secret | grafana.yml |
| `np.grafana.2` | Grafana Cloud API Token | `(?x) \b (glc_eyJrIjoi [A-Za-z0-9]{60,100}) \b` | api, secret | grafana.yml |
| `np.grafana.3` | Grafana Service Account Token | `(?x) \b (glsa_[a-zA-Z0-9]{32}_[a-fA-F0-9]{8}) \b` | api, secret | grafana.yml |
| `np.groq.1` | Groq API Key | `\b(gsk_[a-zA-Z0-9]{50,54})\b` | api, secret | groq.yml |
| `np.pwhash.1` | Password Hash (md5crypt) | `(\$1\$[./A-Za-z0-9]{8}\$[./A-Za-z0-9]{22})` | hashed, secret | hashes.yml |
| `np.pwhash.2` | Password Hash (bcrypt) | `(\$2[abxy]\$\d+\$[./A-Za-z0-9]{53})` | hashed, secret | hashes.yml |
| `np.pwhash.3` | Password Hash (sha256crypt) | `(?x) ( \$ 5 (?# magic ) (?: \$ rounds=\d+ )? (?# optional rounds ) \$ [./A-Za-z0-9]{8,16} (?# salt ) \$ [./A-Za-z0-9]{43} (?# hash ) )` | hashed, secret | hashes.yml |
| `np.pwhash.4` | Password Hash (sha512crypt) | `(?x) ( \$ 6 (?# magic ) (?: \$ rounds=\d+ )? (?# optional rounds ) \$ [./A-Za-z0-9]{8,16} (?# salt ) \$ [./A-Za-z0-9]{86} (?# hash ) )` | hashed, secret | hashes.yml |
| `np.pwhash.5` | Password Hash (Cisco IOS PBKDF2 with SHA256) | `(?x) ( \$ 8 (?# magic ) \$ [./A-Za-z0-9]{8,16} (?# salt ) \$ [./A-Za-z0-9]{43} (?# hash ) )` | hashed, secret | hashes.yml |
| `np.krb5.asrep.23.1` | Password Hash (Kerberos 5, etype 23, AS-REP) | `(?x) ( \$ krb5asrep (?# magic ) \$ 23 (?# etype ) \$ (?: [^:]+ : )? (?# user @ domain : ) [0-9a-f]{32} (?# salt ) \$ [0-9a-f]{64,} (?# hash ) ) \b` | hashed, secret | hashes.yml |
| `np.hashicorp.1` | Hashicorp Vault Service Token (< v1.10) | `(?x) (?i: hashicorp \| vault \| token \| key \| secret ) ["':=\ ]{0,5} \b (s\.[A-Za-z0-9_-]{24,128}) (?: [^A-Za-z0-9_-] \| $ )` | fuzzy, api, secret | hashicorp.yml |
| `np.hashicorp.2` | Hashicorp Vault Batch Token (< v1.10) | `(?x) (?i: hashicorp \| vault \| token \| key \| secret ) ["':=\ ]{0,5} \b (b\.[A-Za-z0-9_-]{24,500}) (?: [^A-Za-z0-9_-] \| $ )` | fuzzy, api, secret | hashicorp.yml |
| `np.hashicorp.3` | Hashicorp Vault Recovery Token (< v1.10) | `(?x) (?i: hashicorp \| vault \| token \| key \| secret ) ["':=\ ]{0,5} \b (r\.[A-Za-z0-9_-]{24,500}) (?: [^A-Za-z0-9_-] \| $ )` | fuzzy, api, secret | hashicorp.yml |
| `np.hashicorp.4` | Hashicorp Vault Service Token (>= v1.10) | `(?x) \b (hvs\.[A-Za-z0-9]{24,130}) (?: [^A-Za-z0-9_-] \| $ )` | api, secret | hashicorp.yml |
| `np.hashicorp.5` | Hashicorp Vault Batch Token (>= v1.10) | `(?x) \b (hvb\.[A-Za-z0-9_-]{24,500}) (?: [^A-Za-z0-9_-] \| $ )` | api, secret | hashicorp.yml |
| `np.hashicorp.6` | Hashicorp Vault Recovery Token (>= v1.10) | `(?x) \b (hvr\.[A-Za-z0-9]{24,130}) (?: [^A-Za-z0-9_-] \| $ )` | api, secret | hashicorp.yml |
| `np.hashicorp.7` | Hashicorp Vault Unseal Key | `(?x) (?i: unseal ) \b .{1,10} \b ([a-zA-Z0-9+/]{44}) (?: [^a-zA-Z0-9+/] \| $ )` | api, secret, fuzzy | hashicorp.yml |
| `np.heroku.1` | Heroku API Key | `(?i)heroku.{0,20}key.{0,20}\b([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b` | api, fuzzy, secret | heroku.yml |
| `np.http.1` | HTTP Basic Authentication | `(?x)(?i) Authorization (?: :\s+ \| \s*.{1, 5}\s*) Basic \s+ ( [A-Za-z0-9+/]{6,} ={0,2} ) (?: [^A-Za-z0-9+/=] \| $ )` | secret, fuzzy, generic | http.yml |
| `np.http.2` | HTTP Bearer Token | `(?x)(?i) Authorization (?: :\s+ \| \s*.{1, 5}\s*) Bearer \s+ ([a-zA-z0-9._~+/-]{6,} =*) (?: [^a-zA-z0-9._~+/=-] \| $ )` | secret, fuzzy, generic | http.yml |
| `np.huggingface.1` | HuggingFace User Access Token | `\b(hf_[a-zA-Z]{34})\b` | api, secret | huggingface.yml |
| `np.jenkins.1` | Jenkins Token or Crumb | `(?x) (?i) jenkins.{0,12}(?: (?: crumb \| token ) .{0,10} )? \b ( [0-9a-f]{32,36} \| [a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12} ) (?: [^0-9a-f-] \| $ )` | api, fuzzy, secret | jenkins.yml |
| `np.jenkins.2` | Jenkins Setup Admin Password | `(?x)(?m) Please\ use\ the\ following\ password\ to\ proceed\ to\ installation: (?: \n\n \| \r\n\r\n ) ([a-f0-9]{30,36})$` | fuzzy, secret | jenkins.yml |
| `np.jina.1` | Jina Search Foundation API Key | `(?x) \b (jina_[a-zA-Z0-9]{60}) \b` | api, secret | jina.yml |
| `np.jwt.1` | JSON Web Token (base64url-encoded) | `(?x) \b ( ey[a-zA-Z0-9_-]{12,} (?# header ) \. ey[a-zA-Z0-9_-]{12,} (?# payload ) \. [a-zA-Z0-9_-]{12,} (?# signature ) ) (?:[^a-zA-Z0-9_-]\|$) (?# this instead of a \b anchor because that doesn't play nicely with `-` )` | api | jwt.yml |
| `np.jwt.2` | JSON Web Token Secret | `(?x)(?m)(?i) ^\s* (?: jwt: \s+ secret: \| jwtsecret: ) \s+ "( [^"]{6,200} )"` | fuzzy, secret | jwt.yml |
| `np.jwt.3` | JSON Web Token Secret | `(?x)(?m)(?i) ^\s* (?: jwt: \s+ secret: \| jwtsecret: ) \s+ '( [^']{6,200} )'` | fuzzy, secret | jwt.yml |
| `np.kagi.1` | Kagi API Key | `(?x)(?s) \b (?: kagi \| KAGI ) .{0,100} \b ( [a-zA-Z0-9_-]{11}\.[a-zA-Z0-9_-]{43} ) (?: $ \| [^a-zA-Z0-9_-] )` | api, fuzzy, secret | kagi.yml |
| `np.kubernetes.1` | Kubernetes Bootstrap Token | `(?x) (?: token \| Token \| TOKEN \| bootstrap \| BOOTSTRAP) .{0,8} \b ([a-z0-9]{6}\.[a-z0-9]{16}) \b` | api, secret, fuzzy | kubernetes.yml |
| `np.kubernetes.2` | Kubernetes Bootstrap Token | `(?x) token-id: \s+ ([a-z0-9]{6}) \s+ token-secret: \s+ ([a-z0-9]{16}) \b` | api, secret | kubernetes.yml |
| `np.linkedin.1` | LinkedIn Client ID | `(?x)(?i) linkedin .? (?: api \| app \| application \| client \| consumer \| customer )? .? (?: id \| identifier \| key ) .{0,2} \s{0,20} .{0,2} \s{0,20} .{0,2} (?# string terminators, whitespace, binding operators, etc …` | api, fuzzy, identifier | linkedin.yml |
| `np.linkedin.2` | LinkedIn Secret Key | `(?x)(?i) linkedin .? (?: api \| app \| application \| client \| consumer \| customer \| secret \| key ) .? (?: key \| oauth \| sec \| secret )? .{0,2} \s{0,20} .{0,2} \s{0,20} .{0,2} (?# string terminators, whitespace, …` | api, fuzzy, secret | linkedin.yml |
| `np.mailchimp.1` | MailChimp API Key | `(?x)(?i) (?:mailchimp\|mc).{0,20} \b ([a-f0-9]{32}-us[0-9]{1,3}) \b` | api, fuzzy, secret | mailchimp.yml |
| `np.mailgun.1` | Mailgun API Key | `(?i)(?:mailgun\|mg).{0,20}key-([a-z0-9]{32})\b` | api, fuzzy, secret | mailgun.yml |
| `np.mapbox.1` | Mapbox Public Access Token | `(?i)(?s)mapbox.{0,30}(pk\.[a-z0-9\-+/=]{32,128}\.[a-z0-9\-+/=]{20,30})(?:[^a-z0-9\-+/=]\|$)` | api, fuzzy | mapbox.yml |
| `np.mapbox.2` | Mapbox Secret Access Token | `(?i)(?s)mapbox.{0,30}(sk\.[a-z0-9\-+/=]{32,128}\.[a-z0-9\-+/=]{20,30})(?:[^a-z0-9\-+/=]\|$)` | api, fuzzy, secret | mapbox.yml |
| `np.mapbox.3` | Mapbox Temporary Access Token | `(?i)(?s)mapbox.{0,30}(tk\.[a-z0-9\-+/=]{32,128}\.[a-z0-9\-+/=]{20,30})(?:[^a-z0-9\-+/=]\|$)` | api, fuzzy, secret | mapbox.yml |
| `np.msteams.1` | Microsoft Teams Webhook | `(?x)(?i) ( https:// outlook\.office\.com/webhook/ [a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12} @ [a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12} /IncomingWebhook/ [a-f0-9]{32} / [a-f0-9]{8}-…` | api, secret | microsoft_teams.yml |
| `np.mongodb.1` | Credentials in MongoDB Connection String | `(?x) (?: mongodb\+srv \| mongodb ) :// (?# URI scheme ) ([a-zA-Z0-9%;._~!$&'()*+,;=-]{3,}) (?# username) : ([a-zA-Z0-9%;._~!$&'()*+,;=-]{3,}) (?# password) @ ([a-zA-Z0-9_.-]{3,} (?: :\d{1,5})?) (?# hostname and port) (?…` | secret | mongo.yml |
| `np.nasa.1` | NASA API Key | `(?x) \b nasa\.gov/ .{0,200} api_key=([a-zA-Z0-9]{40}) \b` | api, secret, fuzzy | nasa.yml |
| `np.netrc.1` | netrc Credentials | `(?x) ( machine \s+ [^\s]+ \| default ) \s+ login \s+ ([^\s]+) \s+ password \s+ ([^\s]+)` | secret | netrc.yml |
| `np.newrelic.1` | New Relic License Key | `(?x)(?i) \b ([a-z0-9]{6}[a-f0-9]{30}nral) \b` | secret | newrelic.yml |
| `np.newrelic.2` | New Relic License Key | `(?x)(?i) associated\ with\ your\ New\ Relic\ account\.\s+ license_key:\s* ([a-f0-9]{40}) \b` | secret | newrelic.yml |
| `np.newrelic.3` | New Relic API Service Key | `(?x)(?i) \b (nrak-[a-z0-9]{27}) \b` | api, secret | newrelic.yml |
| `np.newrelic.4` | New Relic Admin API Key | `(?x)(?i) \b (nraa-[a-f0-9]{27}) \b` | api, secret | newrelic.yml |
| `np.newrelic.5` | New Relic Insights Insert Key | `(?x)(?i) \b (nrii-[a-z0-9_-]{32}) (?: [^a-z0-9_-] \| $)` | api, secret | newrelic.yml |
| `np.newrelic.6` | New Relic Insights Query Key | `(?x)(?i) \b (nriq-[a-z0-9_-]{32}) (?: [^a-z0-9_-] \| $)` | api, secret | newrelic.yml |
| `np.newrelic.7` | New Relic REST API Key | `(?x)(?i) \b (nrra-[a-f0-9]{42}) \b` | api, secret | newrelic.yml |
| `np.newrelic.8` | New Relic Pixie API Key | `(?x)(?i) \b (px-api-[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}) \b` | api, secret | newrelic.yml |
| `np.newrelic.9` | New Relic Pixie Deploy Key | `(?x)(?i) \b (px-dep-[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}) \b` | api, secret | newrelic.yml |
| `np.npm.1` | NPM Access Token (fine-grained) | `(?x) \b (npm_[A-Za-z0-9]{36}) \b` | api, secret | npm.yml |
| `np.nuget.1` | NuGet API Key | `\b(oy2[a-z0-9]{43})\b` | api, secret | nuget.yml |
| `np.odbc.1` | Credentials in ODBC Connection String | `(?x)(?i) (?: User \| User\ Id \| UserId \| Uid) \s*=\s* ([^\s;]{3,100}) \s* ; [\ \t]* .{0,10} [\ \t]* (?# possible extra stuff, e.g., string concatenation) (?: Password \| Pwd) \s*=\s* ([^\t\ ;]{3,100}) \s* (?: [;"'] \|…` | fuzzy, secret | odbc.yml |
| `np.okta.1` | Okta API Token | `(?i)(?s)(?:okta\|ssws).{0,40}\b(00[a-z0-9_-]{39}[a-z0-9_])\b` | api, secret | okta.yml |
| `np.openai.1` | OpenAI API Key | `(?x) \b (sk-[a-zA-Z0-9]{48}) \b` | api, secret | openai.yml |
| `np.particleio.1` | particle.io Access Token | `(?x) https://api\.particle\.io/v1/[a-zA-Z0-9_\-\s/"\\?]* (?:access_token=\|Authorization:\s*Bearer\s*) \b ([a-zA-Z0-9]{40}) \b` | api, secret | particle.io.yml |
| `np.particleio.2` | particle.io Access Token | `(?x) (?:access_token=\|Authorization:\s*Bearer\s*) \b ([a-zA-Z0-9]{40}) \b [\s"\\]*https://api\.particle\.io/v1` | api, secret | particle.io.yml |
| `np.pem.1` | PEM-Encoded Private Key | `(?x) -----BEGIN\ .{0,20}\ ?PRIVATE\ KEY\ ?.{0,20}----- \s* ( (?: [a-zA-Z0-9+/=\s"',] \| \\r \| \\n ) {50,} ) \s* -----END\ .{0,20}\ ?PRIVATE\ KEY\ ?.{0,20}-----` | secret | pem.yml |
| `np.pem.2` | Base64-PEM-Encoded Private Key | `(?x) ( (?: LS0tLS1CRUdJTiBSU0EgUFJJVkFURSBLRVktLS0t (?# prefix of base64 encoding of `-----BEGIN RSA PRIVATE KEY-----` ) \| LS0tLS1CRUdJTiBEU0EgUFJJVkFURSBLRVktLS0t (?# prefix of base64 encoding of `-----BEGIN DSA PRIVA…` | secret | pem.yml |
| `np.phpmailer.1` | PHPMailer Credentials | `(?x) \$mail->Host \s* = \s* '([^'\n]{5,})'; \s* (?: //.* )? (?: \s* .* \s* ){0,3} \$mail->Username \s* = \s* '([^'\n]{5,})'; \s* (?: //.* )? (?: \s* .* \s* ){0,3} \$mail->Password \s* = \s* '([^'\n]{5,})';` | fuzzy, secret | phpmailer.yml |
| `np.postgres.1` | Credentials in PostgreSQL Connection URI | `(?x) (?: postgres \| postgresql ) :// (?# URI scheme ) ([a-zA-Z0-9%;._~!$&'()*+,;=-]{3,}) (?# username) : ([a-zA-Z0-9%;._~!$&'()*+,;=-]{3,}) (?# password) @ ([a-zA-Z0-9_.-]{3,} (?: :\d{1,5})?) (?# hostname and port) (?:…` | secret | postgres.yml |
| `np.postman.1` | Postman API Key | `(?x) \b (PMAK-[a-zA-Z0-9]{24}-[a-zA-Z0-9]{34}) \b` | api, secret | postman.yml |
| `np.postmark.1` | Postmark API Token | `(?x) (?i) postmark [a-z0-9_-]{0,20} .{0,10} \b ([a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}) \b` | fuzzy, api, secret | postmark.yml |
| `np.psexec.1` | Credentials in PsExec Invocation | `(?x) (?i) psexec .{0,100} -u \s* (\S+) \s+ (?# username ) -p \s* (\S+) (?# password )` | fuzzy, secret | psexec.yml |
| `np.pypi.1` | PyPI Upload Token | `(?x) \b (pypi-AgEIcHlwaS5vcmc[a-zA-Z0-9_-]{50,}) (?:[^a-zA-Z0-9_-]\|$)` | api, secret | pypi.yml |
| `np.reactapp.1` | React App Username | `(?x)(?i) \b REACT_APP (?: _[A-Z0-9]+)* _USER (?: NAME)? (?# variable name ) \s* = \s* ['"]? ( [^\s'"$]{3,} ) (?# value ) (?: [\s'"$] \| $ )` | fuzzy, identifier | react.yml |
| `np.reactapp.2` | React App Password | `(?x)(?i) \b REACT_APP (?: _[A-Z0-9]+)* _PASS (?: WORD)? (?# variable name ) \s* = \s* ['"]? ( [^\s'"$]{6,} ) (?# value ) (?: [\s'"$] \| $ )` | fuzzy, secret | react.yml |
| `np.rubygems.1` | RubyGems API Key | `(?x)(?i) \b (rubygems_[a-f0-9]{48}) \b` | api, secret | rubygems.yml |
| `np.salesforce.1` | Salesforce Access Token | `(?x) \b ( 00[a-zA-Z0-9]{13} (?# organization ID ) ! [a-zA-Z0-9._]{96} (?# opaque token ) ) (?: \b \| $ \| [^a-zA-Z0-9._] )` | api, secret | salesforce.yml |
| `np.sauce.1` | Sauce Token | `(?x)(?i) sauce .{0,50} \b ([a-f0-9-]{36}) (?: [^a-f0-9-] \| $ )` | api, fuzzy, secret | sauce.yml |
| `np.segment.1` | Segment Public API Token | `(?x) \b (sgp_[a-zA-Z0-9]{64}) \b` | api, secret | segment.yml |
| `np.sendgrid.1` | SendGrid API Key | `\b(SG\.[0-9A-Za-z_-]{22}\.[0-9A-Za-z_-]{43})\b` | api, secret | sendgrid.yml |
| `np.shopify.1` | Shopify Domain | `(?x) \b ( (?:[a-zA-Z0-9-]+\.)* [a-zA-Z0-9-]+ \.myshopify\.com ) \b` | identifier | shopify.yml |
| `np.shopify.2` | Shopify App Secret | `\b(shpss_[a-fA-F0-9]{32})\b` | api, secret | shopify.yml |
| `np.shopify.3` | Shopify Access Token (Public App) | `\b(shpat_[a-fA-F0-9]{32})\b` | api, secret | shopify.yml |
| `np.shopify.4` | Shopify Access Token (Custom App) | `\b(shpca_[a-fA-F0-9]{32})\b` | api, secret | shopify.yml |
| `np.shopify.5` | Shopify Access Token (Legacy Private App) | `\b(shppa_[a-fA-F0-9]{32})\b` | api, secret | shopify.yml |
| `np.slack.2` | Slack Bot Token | `\b(xoxb-[0-9]{10,12}-[0-9]{10,14}-[a-zA-Z0-9]{23,25})\b` | api, secret | slack.yml |
| `np.slack.4` | Slack User Token | `\b(xoxp-[0-9]{12}-[0-9]{12}-[0-9]{12}-[a-f0-9]{32})\b` | api, secret | slack.yml |
| `np.slack.5` | Slack App Token | `\b(xapp-[0-9]{12}-[a-zA-Z0-9/+]{24})\b` | api, secret | slack.yml |
| `np.slack.6` | Slack Legacy Bot Token | `\b(xoxb-[0-9]{10,13}-[a-zA-Z0-9]{24})\b` | api, secret | slack.yml |
| `np.slack.3` | Slack Webhook | `(?i)(https://hooks.slack.com/services/T[a-z0-9_]{8}/B[a-z0-9_]{8,12}/[a-z0-9_]{24})` | api, secret | slack.yml |
| `np.sonarqube.1` | SonarQube Token | `(?i)sonar.{0,5}login.{0,5}\s*\b([a-f0-9]{40})\b` | api, fuzzy, secret | sonarqube.yml |
| `np.sourcegraph.1` | Sourcegraph Access Token | `\b(sgp_(?:[a-fA-F0-9]{16}_\|local_)?[a-fA-F0-9]{40})\b` | api, secret | sourcegraph.yml |
| `np.square.1` | Square Access Token | `(?i)\b(sq0atp-[a-z0-9_-]{22})\b` | api, secret | square.yml |
| `np.square.2` | Square OAuth Secret | `(?i)\b(sq0csp-[a-z0-9_-]{43})\b` | api, secret | square.yml |
| `np.stackhawk.1` | StackHawk API Key | `\b(hawk\.[0-9A-Za-z_-]{20}\.[0-9A-Za-z_-]{20})\b` | api, secret | stackhawk.yml |
| `np.stripe.1` | Stripe API Key | `(?i)\b((?:sk\|rk)_live_[a-z0-9]{24})\b` | api, secret | stripe.yml |
| `np.stripe.2` | Stripe API Test Key | `(?i)\b((?:sk\|rk)_test_[a-z0-9]{24})\b` | api, secret, test | stripe.yml |
| `np.tavily.1` | Tavily API Key | `(?x) \b ( tvly-[a-zA-Z0-9]{32} ) \b` | api, secret | tavily.yml |
| `np.teamcity.1` | TeamCity API Token | `(?x) \b ( eyJ0eXAiOiAiVENWMiJ9 (?# decodes to `{"typ": "TCV2"}` ) \. [A-Za-z0-9_-]{36} \. [A-Za-z0-9_-]{48} ) (?: [^A-Za-z0-9_-] \| $ )` |  | teamcity.yml |
| `np.telegram.1` | Telegram Bot Token | `(?x) \b (\d+:AA[a-zA-Z0-9_-]{32,33}) (?: [^a-zA-Z0-9_-] \| $)` | api, secret | telegram.yml |
| `np.thingsboard.1` | ThingsBoard Access Token | `(?x) thingsboard\.cloud/api/v1/ ([a-z0-9]{20})` | api, identifier, secret | thingsboard.yml |
| `np.thingsboard.2` | ThingsBoard Provision Device Key | `(?x) "provisionDeviceKey"\s*:\s*" ([a-z0-9]{20}) "` | api, secret | thingsboard.yml |
| `np.thingsboard.3` | ThingsBoard Provision Device Secret | `(?x) "provisionDeviceSecret"\s*:\s*" ([a-z0-9]{20}) "` | api, fuzzy, secret | thingsboard.yml |
| `np.truenas.1` | TrueNAS API Key (WebSocket) | `(?x) "params"\s*:\s*\[\s*" (\d+-[a-zA-Z0-9]{64}) "\s*\]` | api, fuzzy, secret | truenas.yml |
| `np.truenas.2` | TrueNAS API Key (REST API) | `(?x) Bearer\s* (\d+-[a-zA-Z0-9]{64}) \b` | api, fuzzy, secret | truenas.yml |
| `np.twilio.1` | Twilio API Key | `(?i)twilio.{0,20}\b(sk[a-f0-9]{32})\b` | api, fuzzy, secret | twilio.yml |
| `np.twitch.1` | Twitch Stream Key | `(?xi) (?: \b (?:twitch)? (?:[_-]?)? stream (?:[_-]?)? key \b [^A-Za-z0-9]{0,3} )? ["']? ( live_ (?# live_ prefix) \d{8,12} (?# 8–12 digit account ID - examples have 9, unsure of variance) _ [A-Za-z0-9]{30,36} (?# unders…` | api, secret | twitch.yml |
| `np.twitter.1` | Twitter Client ID | `(?x)(?i) \b twitter .? (?: api \| app \| application \| client \| consumer \| customer )? .? (?: id \| identifier \| key ) .{0,2} \s{0,20} .{0,2} \s{0,20} .{0,2} (?# string terminators, whitespace, binding operators, et…` | api, fuzzy, identifier | twitter.yml |
| `np.twitter.2` | Twitter Secret Key | `(?x)(?i) twitter .? (?: api \| app \| application \| client \| consumer \| customer \| secret \| key ) .? (?: key \| oauth \| sec \| secret )? .{0,2} \s{0,20} .{0,2} \s{0,20} .{0,2} (?# string terminators, whitespace, b…` | api, fuzzy, secret | twitter.yml |
| `np.vmware.1` | Credentials in Connect-VIServer Invocation | `(?x)(?i) Connect-VIServer .{0,50} -User \s+ (\S{3,30}) \s+ (?# username ) .{0,50} -Password \s+ (\S{3,30}) (?# password )` | fuzzy, secret | vmware.yml |
| `np.wireguard.1` | WireGuard Private Key | `PrivateKey\s*=\s*([A-Za-z0-9+/]{43}=)` | fuzzy, secret | wireguard.yml |
| `np.wireguard.2` | WireGuard Preshared Key | `PresharedKey\s*=\s*([A-Za-z0-9+/]{43}=)` | fuzzy, secret | wireguard.yml |

## 结构化规则文件（供状态机参考）

- `jwt.yml`：JWT 三段式（header.payload.signature）
- `pem.yml`：PEM 私钥与 base64 套壳私钥
- `http.yml`：HTTP Basic / Bearer 认证头
- `hashes.yml`：密码哈希格式（crypt/bcrypt/Kerberos）
- `generic.yml`：无前缀通用凭据（低精度、依赖上下文关键词）
- `netrc.yml`：~/.netrc
- `mongo.yml`：MongoDB 连接串
- `postgres.yml`：PostgreSQL 连接串
- `odbc.yml`：ODBC 连接串
- `azure.yml`：Azure 连接串与密钥
- `jwk.yml`：JWK（若存在）

# 附录 C：trufflehog AI/LLM 相关检测器

来源：`trufflesecurity/trufflehog` 的 `pkg/detectors/*`（main，抓取于 2026-10-14）。
trufflehog 共 907 个检测器，其中名称与 AI/LLM 相关的 19 个如下。
trufflehog 的强项是**在线校验**（verify 会真的拿 key 打一次厂商 API），
因此它的 `keywords`/`Prefixes` 可作为状态机初筛关键词的直接参考。

| 检测器 | Prefixes | Keywords | 关键正则 |
| --- | --- | --- | --- |
| `anthropic` |  | `"sk-ant-api03", "sk-ant-admin01"` | ``\b(sk-ant-(?:admin01\|api03)-[\w\-]{93}AA)\b`` |
| `assemblyai` |  | `"assemblyai"` | `detectors.PrefixRegex([]string{"assemblyai"}) + `\b([0-9a-z]{32})\b`` |
| `azure_openai` |  | `".openai.azure.com"` | ``(?i)([a-z0-9-]+\.openai\.azure\.com)``<br>`detectors.PrefixRegex([]string{"api[_.-]?key", "openai[_.-]?key"}) + `\b(?-i:([a-f0-9]{32}))\b`` |
| `databrickstoken` |  | `"databricks", "dapi"` | ``\b([a-z0-9-]+(?:\.[a-z0-9-]+)*\.(cloud\.databricks\.com\|gcp\.databricks\.com\|azuredatabricks\.net))\b``<br>``\b(dapi[0-9a-f]{32}(-\d)?)\b`` |
| `deepgram` |  | `"deepgram"` | `detectors.PrefixRegex([]string{"deepgram"}) + `\b([0-9a-z]{40})\b`` |
| `deepseek` |  | `"deepseek"` | `detectors.PrefixRegex([]string{"deepseek"}) + `\b(sk-[a-z0-9]{32})\b`` |
| `elevenlabs` |  |  |  |
| `gemini` |  | `"master-", "account-"` | ``\b((?:master-\|account-)[0-9A-Za-z]{20})\b``<br>``[A-Za-z0-9]{27,28}`` |
| `googlegemini` |  | `"aizasy"` | ``\b(AIzaSy[A-Za-z0-9_-]{33})`` |
| `groq` |  | `"gsk_"` | ``\b(gsk_[a-zA-Z0-9]{52})\b`` |
| `huggingface` |  | `"hf_", "api_org_"` | ``\b(?:hf_\|api_org_)[a-zA-Z0-9]{34}\b`` |
| `langfuse` |  | `"pk-lf-", "sk-lf-"` | `detectors.PrefixRegex([]string{"langfuse"}) + `\b(pk-lf-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b``<br>`detectors.PrefixRegex([]string{"langfuse"}) + `\b(sk-lf-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b`` |
| `langsmith` |  | `"lsv2_pt_", "lsv2_sk_"` |  |
| `openai` |  | `"T3BlbkFJ"` | ``\b(sk-(?:(?:proj\|svcacct\|service)-[A-Za-z0-9_-]+\|[a-zA-Z0-9]+)T3BlbkFJ[A-Za-z0-9_-]+)\b`` |
| `openaiadmin` |  | `"sk-admin-"` | ``\b(sk-admin-[A-Za-z0-9_-]{58}T3BlbkFJ[A-Za-z0-9_-]{58})\b`` |
| `openrouter` |  | `"sk-or-v1-"` | ``\b(sk-or-v1-[0-9a-f]{64})\b`` |
| `replicate` |  | `"replicate", "r8_"` | ``\b(r8_[0-9A-Za-z-_]{37})\b`` |
| `snowflake` |  | `"snowflake"` | `detectors.PrefixRegex([]string{"account"}) + `\b([a-zA-Z]{7}-[0-9a-zA-Z-_]{1,255}(.privatelink)?)\b`` |
| `xai` |  | `"xai-"` | ``\b(xai-[0-9a-zA-Z_]{80})\b`` |

# 附录 D：Yelp detect-secrets 插件清单

来源：`Yelp/detect-secrets` 的 `detect_secrets/plugins/*.py`（master，抓取于 2026-10-14）。
共 27 个插件。detect-secrets 的插件分为：固定格式插件（高精度）、
熵检测插件（Base64/Hex 高熵字符串）、关键词插件（KeywordDetector 依赖变量名）。

| 插件 | denylist 正则 | 其它正则 |
| --- | --- | --- |
| `artifactory` |  |  |
| `aws` |  | `(=\|,\|\()*([\'"]?)([%s]{40})(\2)(\))?'%(re.escape(string.ascii_letters+string.digits+'+/=` |
| `azure_storage_key` |  |  |
| `base` |  |  |
| `basic_auth` |  |  |
| `cloudant` |  |  |
| `discord` |  |  |
| `github_token` |  |  |
| `gitlab_token` |  |  |
| `high_entropy_strings` |  |  |
| `ibm_cloud_iam` |  |  |
| `ibm_cos_hmac` |  |  |
| `ip_public` |  |  |
| `jwt` |  |  |
| `keyword` |  |  |
| `mailchimp` |  |  |
| `npm` |  |  |
| `openai` |  |  |
| `private_key` |  |  |
| `pypi_token` |  |  |
| `sendgrid` |  |  |
| `slack` |  |  |
| `softlayer` |  |  |
| `square_oauth` |  |  |
| `stripe` |  |  |
| `telegram_token` |  |  |
| `twilio` |  |  |

