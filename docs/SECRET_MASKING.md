# 敏感凭据与个人数据遮蔽（Secret Masking）

本文描述 `pkg/secretmask` 的当前能力，以及代理在请求与响应路径上如何使用它。

## 它解决什么问题

用户（或本地 agent）在对话里粘贴真实 API key 时，这个值会随请求离开本机、进入上游厂商与模型上下文。开启本能力后，请求体在离开本机前会被扫描，命中的凭据被替换为**保形假值**；上游与模型只看到假值，本机则保留真值。模型如果在回复或工具调用里回显了假值，响应在写给客户端前会被换回真值。

同一个机制也覆盖个人数据，目前是**中国居民身份证号**（`pii.chinese_id`）与**中国大陆手机号**（`pii.cn_mobile`）：两者走同一套识别、打分、遮蔽与还原路径，只是假值不是逐字节替换，而是重新渲染成结构成立的号码（见「假值形态」）。

它保护的是「值本身不被泄露给上游」。它不承诺的事情见最后的「已知边界」。

## 开关

**默认开启。** 关闭方式二选一：

```yaml
secret_masking:
  enabled: false
```

或环境变量 `TRAJECTA_SECRET_MASKING_ENABLED=false`。

**持久化映射同样默认开启**，由 `secret_masking.mapping_store` 控制：

```yaml
secret_masking:
  enabled: true
  mapping_store:
    enabled: true
    path: ""          # 默认 {{output_dir}}/secret_mask_mappings.db
    load_limit: 0     # 0 = 加载全部；正数只加载最新 N 条
```

环境变量为 `TRAJECTA_SECRET_MASKING_MAPPING_STORE_ENABLED`、`TRAJECTA_SECRET_MASKING_MAPPING_STORE_PATH` 与 `TRAJECTA_SECRET_MASKING_MAPPING_STORE_LOAD_LIMIT`。关掉它回到纯进程内映射：重启后旧假值全部失效，模型上下文里带着的旧假值会被当成新值再遮蔽一次。

之所以默认开启：扫描本身几乎不花时间（见「度量」），而漏掉一个真实 key 的代价是它永久进入上游与模型上下文。开启它改变的是模型实际看到的内容，属于语义变更，因此运维需要能一眼看出它在工作——每个被改写的值（凭据、身份证号与手机号都算）都会落一条 `secret.masked` 事件，响应里每次还原落一条 `secret.restored`；映射文件在启动时也会输出一行 `Secret masking mapping store ready`，带路径与已加载条数。

默认值由 `internal/config.Load` 在解码 YAML **之前**给字段播种实现（与 `debug.mask_key` 同一手法）。这样省略该键即取默认值，而显式 `enabled: false` 与环境变量仍然优先——环境变量在解码**之后**应用，因此始终覆盖文件。

## 假值形态

假值必须同时满足三条性质，缺一条都会出问题。

**一、与真值等长。** 这是承重约束。代理会把上游返回的 `Content-Length` 原样透传给客户端，而请求体长度由 net/http 从「实际交给它的字节切片」推导（`internal/proxy/handler.go` 的 `sendUpstreamRequest` 用 `len(bodyBytes)` 设置 `outreq.ContentLength`）。长度一旦变化，两侧都会静默错位：请求被截断或报传输错误，客户端的 `Content-Length` 与实际响应不符。

**二、保留前缀、标点与分段字母表。** 假值保留真值的前缀（`sk-proj-`、`AIza`、`gsk_` 等）、所有非字母数字字符、以及厂商水印（OpenAI 系四个规则的 `T3BlbkFJ`、Bedrock 短期的 host 串）。字母数字部分按「连续字母数字串」逐段替换：整段都是小写十六进制就仍从小写十六进制取字符，因此 Databricks、智谱、Langfuse、OpenRouter 这类带 hex 段的 key 不会退化成自由文本。这样，只按形状校验的工具仍然接受假值。

**三、确定性。** 假值的首次渲染是 `(seed, 规则名, 真值)` 的纯函数：同一个 key 在每一轮对话里渲染成同一个假值。这既保证多轮对话不自相矛盾，也保证上游的 prompt cache 前缀不发生抖动。开启持久化映射后，同一个真值优先复用已记录的假值，因此换种子（重启、重新加载）或映射被淘汰出内存都不会改变它；渲染函数仍决定一个从未记录过的值长什么样。

渲染在 `pkg/secretmask/vault.go`：以 HMAC-SHA256 生成密钥流，逐字节替换为同类字符。

身份证号与手机号是例外。逐字节替换会打坏它们的内部结构，下游按结构校验的工具会拒绝假值。

身份证号的假值由 `renderChineseID` 用同一份密钥流重新渲染：省份从 GB/T 2260 的省级前缀里取，出生日期固定落在 1950–2009、1–12 月、1–28 日（永远是合法日历日），顺序码取随机数字，最后一位按 ISO 7064 MOD 11-2 重算。**出生日期刻意不保留**：它是最直接指向个人的部分，而下游不需要这个日期为真；代价是「把身份证号与另一处出生日期交叉核对」的流程会对不上（见「已知边界」）。

手机号的假值由 `renderCNMobile` 渲染：**保留真值的三位号段**（`1[3-9]x`，运营商分配是公开信息，保留它让下游的号码形状检查仍然通过），后 8 位数字从密钥流重排。**后 8 位刻意不保留**：与身份证的生日同理，代价是依赖末四位做展示或核对的流程会对不上（见「已知边界」）。

等长、确定性、可还原三条性质对两者都不变。

## Vault

`Vault` 是进程内唯一能还原假值的反向索引。开启 `mapping_store` 时它由 bbolt 文件支撑，关闭时只存在于内存：

- 种子默认来自 `crypto/rand`，每个进程不同。从线路上抓到的假值不能只靠种子还原；开启持久化映射时，本机映射文件是这个进程之外唯一的反向来源，因此它和 cassette、审计表按同一级别保护。
- 启动时加载全部映射（`load_limit: 0`，默认）并且不淘汰；正数只加载最新 N 条，并保留 `DefaultVaultCapacity`（4096）条余量，超过后才按最旧优先淘汰。加载全部的代价是内存随不同敏感值数量增长，与映射文件的增长同阶；只有正数窗口才会淘汰。淘汰只影响内存，下一次遇到同一个真值会先查映射文件并复用已记录的假值，不会换一个假值。
- 遮蔽是幂等的：已经在 vault 里的假值会被跳过，二次遮蔽等价于一次。这正是「上游上下文里的旧假值不会被二次替换」的机制。
- 可并发使用。一个进程只有一份 vault，所有请求共用：多个请求同时遮蔽、多个响应同时还原互不干扰（流式还原在每次扫描时持有读锁）。新值的「查映射文件 + 渲染」由一把独立的创建锁串行化，已命中的值不经过它。
- 新值写入内存后才交给映射文件；写文件是异步批量提交，不占请求路径（见「持久化映射」）。

## 持久化映射

映射文件 `{{output_dir}}/secret_mask_mappings.db` 是一个嵌入式 [bbolt](https://github.com/etcd-io/bbolt) 文件，权限 `0600`。选它而不是 application DB 的原因：映射与渲染它的那个本机进程绑定，属于 cassette 同一侧的本地产物；bbolt 提供有序键、单次批量 fsync 与崩溃安全提交，不需要数据库迁移，也不要求生产环境的 Postgres 可写。

文件里三个 bucket：

| bucket | 键 | 值 |
| --- | --- | --- |
| `meta` | `version` / `next_seq` | 格式版本与下一个插入序号 |
| `mappings` | 8 字节大端插入序号 | 加密后的 JSON `{real, fake}` |
| `by_real` | `HMAC(本地密钥, 真值)` | 插入序号，用来去重与按真值查找 |

- **真值只存一条。** 去重键是真值本身（HMAC），不是 `(规则, 真值)`：同一个敏感值不管在哪个会话、哪一天、被哪条规则命中，永远只有一条转换关系。重复写入直接跳过，先记录的假值获胜。
- **加密。** `real` / `fake` 用本地密钥（`trace_index.secret`，`store.SecretStorageMode()`）封成 `tlsec:v1:` 信封；`by_real` 用 HMAC 而不是裸 SHA-256，因为 11 位手机号的裸摘要可以被离线穷举。没有本地密钥时退化为明文（与 `credential_mask_audits` 的 `plaintext-local` 一致）。
- **只存必要信息。** 值里只有 `real` 与 `fake`：规则和检测时间属于审计行，转换和还原都不需要它们。
- **异步写入。** `Vault` 在内存里插入新映射后，把 `(real, fake)` 投进一个有界队列；后台 goroutine 每 25 ms 或每 128 条提交一个 bbolt 事务。队列满（1024）时该次 append 同步落盘，绝不丢映射。`Handler.Close()` → `Vault.Close()` 会先排空队列再关文件；`serve` 在退出时调用它。
- **启动加载。** 默认加载全部（`load_limit: 0`）且不淘汰，已记录的假值不会被挤出内存；正数只加载最新 N 条（按插入序号倒序取），用启动内存换识别窗口。内存里没有的真值在首次遇到时先按 `by_real` 查文件：这就是「映射已淘汰/不在窗口内，但真值再次出现时仍得到原假值」的保证。查文件只发生在首次遇到某个真值，已命中的值走内存。
- **假值识别。** 加载进内存的假值同时进入 `byFake`，`Mask` 遇到完全相同的假值时跳过。于是一个假值跨重启、跨上下文压缩都保持不变，不会被再遮蔽成第二个假值。
- **文件只增不减。** bbolt 不把释放的页归还给文件系统；按真值去重意味着增长上限是「见过的不同敏感值数」，不是请求数。需要缩小文件时停进程后重建；直接删文件会丢映射，旧假值不再被识别。运行中直接 `cp` 也可能拿到不一致的页，备份应在进程停止时做或用文件系统快照。
- **清理工具。** `trajecta db secret-mapping` 在服务停止后查看与清理这个文件（`stats` / `list` / `delete` / `prune` / `compact`），详见下一节。运行中的 serve 也可以通过 Monitor 的「质量 → 遮蔽映射」标签页查看和删除（见下下节），那条路径直接使用进程内已打开的 bbolt 句柄，因此不需要停进程。删除一个映射不会改变历史 cassette 与审计行，但那个旧假值之后不再被识别：模型上下文里还带着它时，会被当成一个新值重新遮蔽。

### 运行中的映射管理（Monitor）

`GET /api/secret-mappings` 返回运行中进程持有的整张映射表，而不只是 Vault 已加载进内存的窗口：`load_limit` 为正数时，文件里比窗口更旧的条目也会出现在页面上。返回字段包含插入序号 `seq`、明文 `real`/`fake`、两侧长度、该行是否加密以及无法解密时的 `decode_error`；`store` 段给出文件路径、大小与行数统计。查询参数为 `q`（匹配真值或假值）、`page`、`page_size`，按插入序号倒序，因此刚刚渲染的新值排在最前。读取前会先排空异步 append 队列，避免页面在映射刚写入时短暂显示为空。

`DELETE /api/secret-mappings/{seq}` 在一个操作里做两件事：先从 bbolt 删除该行，再从 Vault 的 `byReal` / `byFake` / 还原索引与 `order` 中移除同一对值。第二步是「实时生效」的关键：已命中的真值在 `fakeFor` 里先查内存，只删文件的话，运行中的进程仍会继续使用旧假值直到重启。删除路径持有创建锁，因此与「查文件 + 渲染新值」的慢路径互斥，不会出现半删除状态；删除前先 flush 写入队列，避免一条仍在批量缓冲中的 append 在删除之后把行写回来。删除不是抑制规则：下一次请求再次命中同一个真值时，Vault 仍会渲染并记录一条新映射。CLI 的 `delete`/`prune` 仍然要求先停 serve，因为它要自己打开 bbolt 文件；Monitor 这条路径不适用该限制。

### 抑制名单（误报）

抑制名单回答的是另一个问题：「这个规则在这个值上是误报，以后不要再替换」。它不能靠删除映射实现——映射只是已渲染过的记录，规则仍会再次命中并写回一条新映射。抑制项因此单独保存，并在 `Mask` 的候选循环里、`fakeFor` 之前生效：命中的 finding 直接被跳过，不生成假值、不写映射、不产生 `secret.masked` 事件，也不写 `credential_mask_audits`。

存储沿用同一个 `{{output_dir}}/secret_mask_mappings.db`，新增两个追加式 bucket（旧文件打开时自动创建，版本号仍为 1）：

| bucket | 键 | 值 |
| --- | --- | --- |
| `suppressed` | 插入序号（8 字节大端） | 本地密钥封存的 JSON `{rule, real, reason, created_at}` |
| `suppressed_by_pair` | `HMAC(本地密钥, "suppression/v1" + length(rule) + rule + length(real) + real)` | 插入序号，用于按 `(rule, real)` 去重 |

- **规则作用域。** 键是 `(rule, real)`，不是单独的真值：同一段文本在另一条规则下可能是真凭据。`rule` 为空（UI 显示「全部规则」）表示该真值在所有规则下都抑制；`*` 在写入时归一化为空。规则名必须在可屏蔽规则表里，`access.url` / `access.local_path` 这类 `ReportOnly` 规则会被拒绝，因为抑制一个永不替换的规则没有意义。
- **精确匹配。** 只按完整真值匹配，不做子串、大小写折叠或模糊匹配；需要覆盖多个值就添加多条。值上限 8192 字节，备注上限 1024 字节，避免把整段文档写进映射文件。
- **实时生效。** `POST /api/secret-suppressions` 先写 bbolt，成功后才更新 Vault 内存中的抑制集合（copy-on-write，`Mask` 每次取一次快照）；`DELETE /api/secret-suppressions/{id}` 先删 bbolt 行与 `suppressed_by_pair` 索引，再移除内存项。删除后下一次命中恢复替换，已有的映射会被复用，因此假值不会因为一次抑制/解除而改变。
- **不影响历史。** 新增抑制不会删除已有映射、历史审计行或事件通知；它只改变后续请求是否替换。移除抑制也不会删除映射。
- **启动加载。** `NewVaultWithStore` 在加载映射后加载全部抑制项；无法用当前本地密钥解密的行会跳过并打日志，但仍出现在 `GET /api/secret-suppressions` 里（带 `decode_error`），可以按 ID 删除。
- **管理面。** `GET /api/secret-suppressions` 分页返回 `id`、`rule`、`real`、`reason`、`created_at`、`decode_error` 与可屏蔽规则名列表；`POST` 接收 `{rule, real, reason}`；`DELETE /api/secret-suppressions/{id}` 删除一条。Monitor 的「质量 → 抑制名单」提供新增、搜索与移除；「遮蔽映射」页每行的「抑制」按钮等价于以空规则添加一条，用于一键把误报值从所有规则下摘掉。`trajecta db secret-mapping compact` 会连同这两个 bucket 一起重建，不会在压缩时丢掉抑制项。

### 映射文件的清理（`trajecta db secret-mapping`）

`{{output_dir}}/secret_mask_mappings.db` 是整个 output_dir 共用的一份 bbolt 映射：键是真实值（`by_real` 用本地密钥对真值做 HMAC），值是本地密钥封存的 `{real, fake}` 对，另加插入序号；文件里没有 session/trace、偏移或检测时间，也不按 trace 分区。按 trace 记录「哪次请求替换了哪些值」的是 application DB 的 `credential_mask_audits` 表，两者用途不同：审计表是逐次记录，映射文件只用于重启后复用与识别假值。

运行中的 serve 持有 bbolt 排他锁，所以这条 CLI 清理路径必须先停 serve；命令不会静默失败，会报明确的「stop serve first」。需要在不重启的情况下删除一条误报映射时，用 Monitor 的「质量 → 遮蔽映射」页面。所有写命令默认 dry-run，只有 `--apply` 才落盘；`--config/-c` 与 `--format json` 对所有子命令可用。dry-run 的承诺是「不改映射行」：为了读取真值，命令仍会以读写方式打开 bbolt（可能更新文件元页），`stats`/`list`/`delete`/`prune` 还要打开 application DB（`auto_migrate` 生效时可能触发迁移），只有 `compact` 不打开数据库。

| 子命令 | 作用 |
| --- | --- |
| `stats` | 路径、是否存在、条目数/索引数、文件大小、密文/明文/不可读行数、序号范围、加密模式 |
| `list [--show-values] [--contains <子串>] [--limit N \| --all]` | 默认脱敏（seq、真值/假值长度、真值的**带密钥 HMAC 前缀**、假值的 SHA-256 前缀；真值前缀不用裸 SHA-256，否则低熵手机号可被离线穷举确认。没有本地密钥时 `by_real` 本身退化为域分隔 SHA-256，指纹随之退化，但此时映射文件已经是明文）；`--show-values` 才输出明文；`--contains` 匹配真值或假值 |
| `delete (--real <值> \| --seq <n> \| --contains <子串>) [--apply]` | 三选一；在一个 bbolt 事务里同时删除 mappings 行与 by_real 索引行 |
| `prune --rule <规则名> [--since RFC3339] [--until RFC3339] [--purge-audits] [--apply]` | 先从 `credential_mask_audits` 取出该规则/时间窗产生的真值，再删除对应映射行；`--purge-audits` 同时删除匹配的审计行（只删 `credential_mask_audits`，不动 system_events）。映射文件没有时间戳，时间窗只筛审计行 |
| `compact [--apply]` | bbolt 删除行后文件不会缩小；用活行重建并保留 `<path>.bak`（已有 `.bak` 时拒绝）。`.bak` 同样含真值，校验后应删除 |

清理线上误报的典型流程（旧算法写进映射与审计的错误条目）：

1. 停 serve，先 `stats` 看清规模；
2. 用 `prune --rule env.secret_assignment --until <修复上线时间>` 做 dry-run：代码片段的误报都记在 `env.secret_assignment` 下，但同一规则名下也可能有正常条目，时间窗用来把清理限定在修复上线之前；dry-run 输出 `would_delete_mappings` 与 `audit_rows`（`purged_audit_rows` 只在 `--apply` 后出现），确认量级后需要连审计表一起清就加 `--purge-audits`；
3. `--apply` 落盘，再 `compact --apply` 回收文件空间（确认后删除 `.bak`）；
4. 重启 serve：被删除的旧假值不再被识别，模型上下文里若还带着它们，会被当作新值重新遮蔽；历史 cassette 与已保留的审计行不受影响。

退出码沿用 CLI 契约：成功 0，操作失败 1，用法错误 3。`stats`/`list`/`delete`/`prune` 需要 application DB（真值用 `trace_index.secret` 解密、HMAC 索引也用它）；DB 连不上时这些命令会失败，`compact` 只做原始字节复制、不依赖 DB。密钥轮换后旧行不可解密：`stats` 的不可读行计数与 `list` 会显示，用 `delete --seq` 清理；`prune` 无法解密的审计行计入 `undecryptable_audit_rows`。

## 识别与打分

引擎分四层，越往后越贵，只有前一层放行的候选才进入下一层；另有一条独立的原生通道在 DFA 之后运行，负责词法层看不到的两类值——env 赋值与 DSN 密码（见下），两者结果合并后共同参与改写。

**L1 词法 DFA。** `pkg/secretmask/rules.rl` 是 DFA 词法模式的唯一事实源（名称锚定的两条 env 规则不在状态机里，见下），由 Ragel 编译成 Go 状态机（`rules_ragel.go`，已提交）。扫描器用 `|* ... *|`，因此**最长匹配优先，等长时先声明者优先**，规则顺序具有语义。运行时不做回溯。

长度上下界不在状态机里表达，而在 `scan.go` 的 `collector.emit` 里用原生代码判断。把固定重复 `{n}` 交给状态机展开到宽字符类上，状态表会按组合数膨胀：实测把一条 `b64u{24} '.' b62u{6} '.' b62u+` 形态的候选规则加进去，生成代码从 25238 行涨到 144434 行（5.7 倍），生成时间也同步变长，而「`+` 加原生长度检查」没有这个问题。当前 `rules_ragel.go` 是 35140 行 / 387519 字节（`sk-` 通用池的字符类扩到 `b62d` 后重新生成）。

访问指示符另有一套 **report-only** 状态机 `indicator.rl`（`indicator_ragel.go`，已提交）：`access.url` 与 `access.local_path` 只给出 URL 与本地路径的形状，供 `internal/resources` 校验模型工具调用里的候选 token；`ReportOnly` 使 `Finding.Maskable` 恒为 false，`Vault.Mask` 也从不使用这套机器，资源索引也不改写任何载荷。不把这两条并进 `rules.rl` 是刻意的：Ragel 扫描器会消费它匹配到的字节，URL 规则若与凭据规则同机，`https://…?X-Amz-Credential=AKIA…` 会把里面的凭据整段吞掉，遮蔽就再也看不到它；两套机器意味着同一份字节扫两遍，各自独立。这套机器与 `rules.rl` / 凭据状态机相互独立（`rules_ragel.go` 只因 `sk-` 通用池字符类变化重新生成过，与本机器无关）。

**粗筛与细判。** 带前缀的凭据（`sk-proj-`、`AIza`、`ghp_` …）由状态机直接给出规则 ID，因为锚点本身就是词法。数字类个人数据没有这样的锚点：身份证号是 18 位，手机号 11 位，银行卡 12–19 位，它们共同的词法只是「一段数字」。为每一类各写一条 `[0-9]{n}` 会让状态机按长度重复展开；先粗扫再细扫则等于把同一份请求扫两遍。`rules.rl` 因此只有一条粗规则 `digit_run = [0-9]+ [Xx]?;`，命中后交给 `scan.go` 的 `emitDigits`：按 `digitClasses` 的顺序逐类试长度与校验器，第一类通过即采用，两侧边界由 `bounded` 统一检查。加一类数字（手机号、银行卡）只改 `digitClasses` 与该规则的元数据，**不新增 DFA 状态、也不需要重新生成**，一次扫描同时服务所有数字类。手机号就是按这条路径加入的：`rules.rl` 未改、生成表未因该规则变化，改动集中在规则元数据、候选检查与渲染器。

**原生赋值与 DSN 扫描（不是 DFA）。** 还有两类值没有词法形状，或者只有「用不上」的词法：

- env 赋值：`POSTGRES_PASSWORD=…`、`OPENAI_API_KEY=…`、工具输出里的整段 `.env`。证据是变量名，而变量名必须是环境变量的写法：**全大写**，且以 `_KEY` 或 `_PASSWORD` 结尾（`OPENAI_API_KEY`、`AWS_SECRET_ACCESS_KEY`、`POSTGRES_PASSWORD`）。这条限制是线上误报的直接修复：更早的规则把任何末段为 KEY/TOKEN/SECRET/PASSWORD 的名字（`clientSecret:`、`token:`、`dbPassword=`、`PGPASSWORD`、裸 `KEY`）都当成赋值，于是工具结果里回传的源码被整段改写——`clientSecret: string(target.ClientSecret)` 里的 `string(target.ClientSecret`、Go 里 `bodyKey: strings.TrimSpace(key)` 里的 `strings.TrimSpace(key` 都曾出现在审计表里。代码片段不是凭据，改错一个比漏掉一个 shapeless 值的代价更大。
- DSN：`postgres://user:password@host:5432/db`。唯一秘密的部分是 userinfo 里的 password，scheme 本身就是「中间那段是密码」的证据，因此不需要变量名。`dsn.go` 对一组服务 scheme（`postgres`/`postgresql`/`pgsql`/`cockroachdb`/`redis`/`rediss`/`mysql`/`mariadb`/`mongodb`/`mongodb+srv`/`amqp`/`amqps`/`mssql`/`sqlserver`/`clickhouse`）做封闭列表识别，只改写 password 子跨度；用户名、主机、端口与库名保持可读。列表是封闭的：通用的 `scheme://user:password@host` 会把文档示例与 `git clone` URL 也改掉。

`pkg/secretmask/assignment.go` 因此在 DFA 之外做一遍原生扫描：以 `=` / `:` 为触发点，向前最多回看 64 字节取变量名或 scheme，向后按引号/转义形态取 value span；单遍 O(n)、无命中路径零分配，也不解析 JSON（body 可能是嵌在 JSON 字符串里的 JSON 文本）。DSN 与 env 赋值共用这一遍遍历：DSN 由 `:` 后的 `//` 触发，被 URL 类名字赋值的 DSN 由 `env.url_credential` 认领（`=` 先到，整段 value 已被消费），因此同一个 password 不会被报告两次。三条规则追加在 `rule.go` 末尾：

- `env.secret_assignment`（Strong，70 分 → High）：改写**整个 value**。名字必须全大写并以 `_KEY` 或 `_PASSWORD` 结尾；`_KEY` 的 value ≥ 16 字节，`_PASSWORD` 的 value ≥ 6 字节。
- `env.url_credential`（同 kind/strength）：名字全大写且以 `_URL`/`_URI`/`_DSN`/`_CONNECTION_STRING` 结尾时按 URL 类处理，只改写 `scheme://user:password@host` 里的 password；没有 userinfo password 的值一律不动，整个 DSN 从不整段改写（host 是运维要读的地址，不是凭据）。
- `dsn.password`（同 kind/strength）：任意位置出现的、scheme 在封闭列表里的 DSN，只改写 userinfo password，不需要变量名。

支持的写法：`NAME=value`、`export NAME=value` / `set NAME=value`、`NAME = value`、YAML `NAME: value`、JSON `"NAME": "value"`、以及字符串内嵌 JSON 的转义形态 `\"NAME\": \"value\"`（转义深度 1–3）；单/双引号按内层引号原样保留，`.env` 段落在转义 JSON 字符串里以 `\n`/`\r`/`\t`/`\"` 或原始空白换行。span 只覆盖 value 本身，引号、分隔符与空白不在改写范围内。

长度与跳过：`${NAME:-default}` / `$NAME` 引用、空值、以 `$ % < {` 开头的值、未加引号的 JSON 数字/`true`/`false`/`null`（仅整值赋值；DSN password 可以是纯数字）、占位词（`***`、`xxx`、`…`、`changeme`/`change_me`/`change-me`、`password`/`passwd`/`secret`/`token`/`key`/`dummy`/`example`/`sample`/`test`、JSON schema 类型名、`redacted`/`masked`、`your_*`、`replace*`）、以及引号内超过 1024 字节的「值」都跳过；引号内以空白开头的值也跳过（通常是 shell 命令把收尾引号读成了起始引号）。未加引号的 value 另外拒绝代码表达式：含 `(`/`[`/`{`/`<`，或首段是小写标识符的点号成员访问（`process.env.OPENAI_API_KEY`、`config.API_KEY`）——`.env` 的 value 与 JSON 字符串不长这样。

协议词排除：`assignmentExcludedNames` 把变量名去掉 `_`/`-`/`.` 并小写后与固定表比较——`promptcachekey`、`nexttoken`、`pagetoken`、`continuationtoken`、`cachekey`、`idempotencykey`、`sortkey`、`partitionkey`、`routingkey`、`groupkey`、`primarykey`、`foreignkey`、`objectkey`、`storagekey`、`s3key`、`tokentype`、`tokencount`、`maxtokens`、`prompttokens`、`completiontokens`、`totaltokens`、`reasoningtokens`。这些名字末段是 KEY，但值是协议参数、计数器或标识符；不改写 `PROMPT_CACHE_KEY` 才不会改变 OpenAI Responses 的缓存键。排除是整名匹配，`PROXY_KEY`/`OPENAI_API_KEY` 不受影响。

与 DFA 结果的合并：`mergeFindings` 保证两组 span 不重叠、按 start 排序。原生 span **严格包含**一个或多个 DFA span 时用前者（名字说整个 value 是一个秘密，DFA 可能只认出了其中形似的一段）；等长时若 DFA finding 本来可改写则保留 DFA（厂商规则在审计里更具体），否则用原生的（`PROXY_KEY="sk-..."` 由弱通用形状升格为可改写就靠这条）；其它重叠（被包含或仅交叉）保留 DFA。方向同其他规则：只在**请求方向**改写，cassette 仍记录原始 body，客户端在响应里拿到还原后的真值。

**名称锚定通道的取舍。** 收紧后的覆盖是刻意保守的：`*_TOKEN`、`*_SECRET`、`PGPASSWORD`、`dbPassword`、裸 `KEY`/`PASSWORD`、以及 `_KEY`/`_PASSWORD` 之外的 shapeless 名字都不再改写。它们当中确实可能有真凭据（`GITHUB_TOKEN`、`JWT_SECRET`、`PGPASSWORD`），这是用漏报换误报的方向：线上一次把代码当密码改写，会让上游看到损坏的代码、客户端收到被换回的片段，比漏掉一个没有词法锚点的值更难接受。词法上可判定的 key（`ghp_`、`sk-` 等）仍由 DFA 覆盖，与名字无关。

**L2 结构校验。** `validators.go` 实现六个离线校验，规则通过 `Validator` 字段引用：

- `jwt`：三段 base64url 全部解码成功，头部是命名了算法的 JSON 对象，载荷是 JSON 对象，且签名解码长度与该算法一致。
- `anthropic_key`：结尾必须是已发布 Anthropic key 家族共有的 `AA`。
- `github_pat`：`github_pat_` 之后 30 字节正文的 CRC32 与末 6 字节 base62 校验位吻合。
- `jwe`：第一段 base64url 解出的 JSON 头里有 `enc` 成员。
- `bech32`：BIP-173 校验和成立（age 私钥），且大小写不混用。
- `chinese_id`：18 位，前 6 位是 GB/T 2260 的省级前缀，第 7–14 位是合法历法生日（含闰年，年份 1800–2099），最后一位是前 17 位的 ISO 7064 MOD 11-2 校验位。

带校验器的规则只上报通过校验的匹配，且通过校验本身就足以认定。

手机号没有可重算的校验位，因此它用的不是 `Validator` 而是 `CandidateCheck`（`cn_mobile_prefix`）：只检查 11 位数字与 `1[3-9]x` 号段是否已分配，不做任何运算，**通过也不提分**。它是「可检查但不可证明」的一档——号段表只把候选空间从任意 11 位数字收窄到已分配号段，真正的改写判据是上下文。

**L3 上下文。** 在匹配位置前后各 96 字节窗口内搜索凭据词汇（`context.go` 的全局词表，加上规则自己的 `Keywords`）。规则可以用 `ContextWindow` 把窗口收窄：手机号用 24 字节，因为「手机号/phone 紧挨着号码」才是真实写法，而 96 字节外的一句「手机」不应把旁边的订单号变成手机号；窗口越窄，这层也越便宜。

规则可以用 `SkipContext` 跳过这一层：校验器通过就已经落在可改写档，窗口扫描不会改变结论，而它是命中密集时最贵的一步。身份证规则就是这么做的——64 KB 里命中约 3800 次时，跳过窗口扫描把耗时从约 23.5 ms 降到约 0.45 ms。**手机号不能跳过**：上下文是它唯一的判据；因为窗口收窄到 24 字节，同样密集的载荷约 3.1 ms（约 21 MB/s），这是该规则的已知最坏情况；普通载荷里命中稀疏，扫描仍约 51–53 MB/s。`WordBounded` 是另一条原生判据：匹配两侧只要紧邻 ASCII 字母或数字就丢弃，避免从 19 位订单号或时间戳里切出一个 18 位来当身份证号。

**L4 置信度门限。** 打分权重见 `finding.go`：Strong 70 分、Anchored 35 分、Generic 10 分；规则定长且长度吻合加 10 分；上下文命中加 40 分；校验器通过则分数不低于 70 分；总分封顶 100。分档为 High ≥ 70、Medium ≥ 45、Low < 45。**只有 Medium 及以上允许被自动改写**，Low 仍然上报，让审计能看到「看见了但故意没动」。

`Strength` 的划分依据是「词法匹配本身证明了多少」：Generic 的锚点是普通代码里也会出现的短前缀（`sk-`、`sk_`、`ek_`、`ak-`、`as-`、`st-`、`pa-`、`nk-`、`pk-`、`pl_`、`tl_`、`lf_`、`key_`、`AQ.`、`1//`、`app-`/`dataset-` 等），Anchored 是厂商特有但正文自由，Strong 是形状内含厂商水印或「前缀 + 精确长度」已经足够决定。

**不透明 JSON 字段不参与识别。** 请求体里有一类值由厂商封装、必须原样回到上游，当前覆盖：OpenAI Responses 的 `encrypted_content`（加密推理状态）；Anthropic Messages 的 `thinking` / `redacted_thinking` 块（整块跳过：`signature` 用来校验 `thinking` 正文，`redacted_thinking.data` 是密文）；Gemini/Vertex Part 的 `thoughtSignature`（Google schema 的原文是「an opaque signature for the thought so it can be reused in subsequent requests」）与 `inlineData` / `inline_data` 内联二进制；AWS Bedrock Converse 的 `reasoningContent`（`reasoningText` 同时含 `text` 与验证它的 `signature`，或 `redactedContent` 密文）；OpenRouter 的 reasoning 项与 Chat `reasoning_details`（`reasoning.text` / `reasoning.encrypted` / `reasoning.summary`，文档要求 content、summary、signature 与 encrypted_content 原样回传）；以及 base64 媒体（`file_data`、`input_audio`、base64 data URI 形式的 `url`/`image_url`）。它们都不是用户凭据，而且必须逐字节回传；线上误报就是 `encrypted_content` 里恰好出现 `xai-` 加足够长的 token 字符，被 `xai.api_key` 命中并改写，上游判定密文被篡改，整个请求失败。实现上，`Vault.Mask` 在词法扫描后按 JSON 结构定位这些区间（`pkg/secretmask/opaque.go`）：成员名清单是 `opaqueJSONFields`，整块跳过的对象类型是 `opaqueJSONObjectTypes`，按对象类型限定到单个成员的规则是 `opaqueJSONScopedFields`（`base64` source 只跳过 `data`，同对象的其他成员仍照常扫描）。落在区间内的候选直接丢弃：既不写 `secret.masked`，也不进 `credential_mask_audits`，更不改写字节。`signature`、`data`、`url` 这类通用成员名不单独列入成员清单，只有它们所在的协议对象/类型或 base64 data URI 才让它们不透明；但整块规则按 JSON 结构匹配、不按 provider 路径限定，因此客户端工具参数里恰好使用这些 type 值的对象也会被整块跳过（见「已知边界」）。定位用 `encoding/json` 逐 token 遍历而不是在字节里搜字段名——消息正文可以原样包含 `"encrypted_content"` 这串文字，按字节跳会让人用提示词把真凭据藏起来；只接受单个完整、无尾随字节的合法 JSON 文档，解析失败时退回原有行为。**这段逻辑刻意不写进 Ragel DFA**：状态机只认识字节形状，不携带 JSON 结构；把键名与嵌套状态编进 DFA 会让凭据扫描依赖请求体语法、状态表变大，而同一个扫描器还要用于纯文本（观测文本、分析）。也不能用「长 base64 就跳过」的启发式：规则集里本来就有需要识别的 base64 包装凭据（PEM 正文、JWT），每个跳过区间都是真凭据可能藏身的位置。

## 覆盖范围

凭据与个人数据可改写规则集共 81 条：Strong 39、Anchored 24、Generic 18（另有 2 条 ReportOnly 访问指示符 `access.url` / `access.local_path` 不计入，规则元数据表共 83 条）。范围是四组：

1. 结构形状（JWT、JWE、PEM 头与 PEM 私钥块、Bearer）与 LLM 厂商前缀，即 [密钥与 Token 格式识别参考](./SECRET_FORMAT_REFERENCE.md) 第 3、4 节所述内容，外加 `sk-`/`sk_` 两个通用池；`sk-` 池的 body 字符类是 `b62d`（字母、数字、`_`、`-`），因此形如 `sk-…_…` 的 key 不会再在分隔符处被截断，具体 `sk-ant`/`sk-proj`/`sk-or` 规则仍按等长先声明者优先拿到标签；
2. 该参考 §8.1「可离线判定」里**锚点足够窄**的那几条：AWS AccessKeyId、GitHub 经典 PAT（CRC32 校验位）与细粒度 PAT、PyPI token、age 私钥、PowerShell `ConvertTo-SecureString`；
3. 个人数据：中国居民身份证号（`pii.chinese_id`）与手机号（`pii.cn_mobile`）。两者都按 Generic 计分——18 位或 11 位数字本身不证明什么。身份证的校验器通过即认定，因此实际只会改写省份、生日与校验位三者同时成立的号码；手机号没有校验位，号段只是候选检查，只有命中 `手机号`/`phone` 这类上下文才进入 Medium 并被改写，裸 11 位数字只上报（Low）；
4. 原生通道（`env.secret_assignment`、`env.url_credential` 与 `dsn.password`，三条都按 Strong 计分）：不是 DFA 规则，由 `assignment.go` / `dsn.go` 的原生遍历产出；覆盖范围、写法、跳过与排除见上一节。

**另有两条只上报的访问指示符规则。** `access.url` 与 `access.local_path`（`indicator.rl`）不计入上面的 81 条与 Strength 分布，也永远不会被改写。URL 覆盖 `http`/`https`/`ws`/`wss`；本地路径覆盖 POSIX 绝对路径、已知根目录、`~/`、`./`、`../`、Windows 盘符、UNC 与带扩展名的相对路径。机器只负责最长的 URL/路径字节串，`indicator.go` 的候选检查才决定它是不是一次本地读取：`/v1/chat/completions` 这类路由前缀、`application/vnd.api+json` 这类 MIME、`net/http` 这类 import 路径、`example.com/path` 这类域名相对引用与 `#/definitions/Foo` 这类 JSON pointer 都被排除。路径字符类本身不含圆括号、`=`、`!` 与 `&`，所以 `(modules/x.md`、`registry_file=internal/x.go` 只记录括号或赋值号后面的那段值（候选检查只能丢弃候选，不能改写值，前缀必须在状态机里就不被消费）。`./`、`../`、`~/` 之后必须跟一个带扩展名的文件或至少两段目录，`../`、`../a`、`./$` 因此不算一次读取。反斜杠开头只接受两种自洽形态：解码形态是一个前导 `\\` 加单分隔符（`\\server\share`），转义形态是两个前导 `\\` 加成对分隔符（`\\\\server\\share`）；两者混用（`\\n\\tid`、`\\d+)\\.(\\d+`、`\\n\\n`）只可能是转义文本，直接拒绝；段名本身是 `n`/`t`/`r` 加行号、`\uXXXX`、`\xXX`，或含 `%s` 这类 printf 占位符时也不接受（`q:\n%s`、`z:\n244`）。句末或 markdown 的收尾标点（`).`、`**`）由 `TrimRight` 从值里去掉，但成对括号属于 URL 本身（`…/wiki/Foo_(bar)`），只有落单的收尾括号才被去掉。段内不包含空白：URL 到第一个空白/引号/反斜杠为止，这是路径与正文之间唯一可靠的分界；`internal/resources` 只在候选 token 被整段命中时才收录，所以含空格的路径（`C:\Program Files\…`、`/home/a/my file.txt`）目前不会进入资源索引，这是为精度保留的已知边界。工具调用参数是 JSON 文本，其中的 Windows 路径反斜杠成对出现（`C:\\Users\\…`）；机器同时接受单、双反斜杠两种形式，资源提取在记录前把成对反斜杠还原成单个，UNC 的两段网络根保持不变。它们的消费方是 `internal/resources` 的资源索引：它只把模型自己产生的工具调用（响应侧、请求侧 assistant 历史与 entry 的原始请求/响应）里的路径/URL 字段、shell 操作数与 patch 头部作为候选，解析 worker 在解析后按值去重，写入 `resources` / `trace_resources`（见[观测与审计](./OBSERVATION_AND_AUDIT.md) 的「资源索引」）；工具结果、消息正文和代码内容不参与，资源不替换、不进入 `secret_mask_mappings.db`，只记录访问了哪些路径与 URL，不记录文件内容。路径 key 与 shell 操作数调用 `IndicatorsWithOptions` 的 `PathOperand`：相对目录（`web/dist/learn`、`../src`）在那里是路径；`/v1` 这类路由前缀、MIME、域名与转义残渣仍被排除，正文与普通 token 使用上面的严格检查。`/dev/null` 等设备 sink 无论作为重定向目标还是 `-o` 一类选项值都不计入。

**PEM 私钥块按整块改写，不是只改头行。** `structural.pem_private_key` 仍然只匹配头行、只上报（`policySkipMask` 只豁免这一条规则 ID），因为头行不含密钥材料；真正承载密钥的是 `structural.pem_private_key_block`。footer 必须重复 header 的标签（`OPENSSH` 对 `OPENSSH`），这是一次反向引用，正则/DFA 表达不了，所以扩展写在 `pem.go` 里、由头行命中触发，沿用 `digit_run` → `digitClasses` 的「粗锚点 + 原生细判」打法：不新增 DFA 状态，也不重新生成 `rules_ragel.go`。

块级假值保留头行、尾行与每一个换行/缩进，只重排正文的字母数字串。于是长度、行宽、`BEGIN`/`END` 标签、YAML 缩进与 JSON 的 `\n` 转义全部原样保留，正文仍是 base64 形状，但解码后是垃圾——私钥假值**故意不是一个可解析的 key**，与身份证/手机号「结构成立」的假值不同。覆盖 `OPENSSH`、PKCS#1（`RSA`/`DSA`/`EC`）、PKCS#8（`PRIVATE KEY`）、`ENCRYPTED PRIVATE KEY`，以及 CRLF、JSON 转义、YAML 块缩进、同一请求多把 key。公钥（`ssh-ed25519 AAAA…`、`authorized_keys`、`known_hosts`）与证书不是秘密，**刻意不匹配**。

还原侧多一层考虑：私钥块全是换行，而 JSON 把换行写成 `\n`。vault 同时登记假值在「深一层 / 浅一层」两种转义深度下的形态（`wireVariants`），因此普通消息正文（转义一次）与工具调用参数（本身是 JSON 字符串，转义两次）都能整条还原成对应深度的真值，客户端拿到的仍是合法 JSON。

**刻意不覆盖 §8.4 的弱形态。** 32/40/64 位 hex、UUID、无锚点的 44/88 位 base64，都是哈希、对象 ID 与密钥共用的形状；而校验器通过会把分数直接抬到可改写档，所以「base64 解码后正好 64 字节」这类判据一旦写成规则，就会把转录里每一个 64 字节块（哈希、证书、随机 nonce）都改写掉。弱形态要成为可改写的，必须先有「必须命中上下文」这一档语义，而现在的打法是上下文**加分**而非**必需**。

**15 位旧号不覆盖。** 1999 年之前的 15 位旧身份证没有校验位，只有 6 位地区 + 6 位生日（YYMMDD）+ 3 位顺序码，仅凭形状无法与普通数字区分；要做就必须把上下文改成「必需」，与当前「上下文加分」的打法冲突。

**手机号只覆盖 11 位连写。** 带 `+86`、空格或连字符的写法只命中其中的 11 位数字段（`+86 13800138000` 可以）；`8613800138000` 这种 13 位连写是单个数字 run，不命中；分机写法（`13800138000x123`）同样不命中。号段表按公开分配整理，新号段上线后需要补表；未列入的号段即使上下文命中也不会改写。词表里没有裸 `tel`（它是 `hotel`、`intel` 的子串，会把旅店记录里的数字都当成手机号），带引号的 JSON 键 `"tel"`（以及它在字符串内嵌 JSON 里的转义形式 `\"tel\"`）在词表内，因此 `{"tel":"…"}` 与 `"content":"{\"tel\":…}"` 都能改写，而 YAML 的 `tel: …` 不能。

**DSN 与 Cookie 的子跨度边界。** DSN 只有 password 是秘密（`postgres://user:pass@host` 里的 pass），Cookie 只有值的一部分是秘密。DSN 由 `dsn.password` 按 scheme 直接识别，不要求变量名：任意位置出现的 `scheme://user:password@host`（scheme 在封闭列表内）都只改写 password 子跨度，host、用户名、端口与查询参数保持原样；没有 userinfo password 的 DSN 不改写。同一个 DSN 挂在 URL 类名字（`*_URL`、`*_DSN`、`*_URI`、`*_CONNECTION_STRING`，名字须全大写）下时由 `env.url_credential` 认领，审计里能看到是名字给了证据，密码只报告一次。ADO.NET 形态（`Server=…;Password=…`）不是 URL，`CONNECTION_STRING` 值本身不做 userinfo 解析，其中的 `Password=…` 也不再命中（`_PASSWORD` 是前半段而不是名字末段；这是收紧后的已知取舍）。`Cookie: a=b` 没有这样的名字锚点，仍不自动改写，由 `internal/analyzer` 的正则事后上报。

`internal/analyzer` 仍保留 DSN、Cookie 与裸 Bearer 形态的正则用于**事后上报**：上报可以有误报，自动改写不能。`structural.bearer_token` 是本规则集里最激进的一条——Bearer token 本身没有形状，锚点就是 RFC 6750 的 scheme 名，而上下文窗口包含匹配自身，所以正文里任何 `Bearer ` 加 20 字节以上的串都会被改写，示例与占位值也不例外。这是刻意的：用户无意贴出的 `Authorization: Bearer …` 正是要遮的东西。代理不改写请求**头**，所以真正用于向上认证的 `Authorization` 头不受影响。

## 在代理中的接入点

请求侧是「记录原始、发送假值」的拆分，两侧都不复用同一个切片：

- 透传路径：`internal/proxy/handler.go`。`recordBody`（别名改写后、conversion/mask 前）交给 cassette，`transformResult.Body.WireBytes()`（conversion/mask 后）交给上游。交给路由器、指纹与失败 cassette 的 `bodyBytes` **不被改动**。
- 本地 Responses 路径：`internal/proxy/responses_server.go`。`buildResponsesServerChatRequest` 收到遮蔽后的体，`PrepareLogFileWithOptionsAndBody` 的第三个参数仍是原始体。

请求体如果带着上一进程记录的假值（模型上下文、压缩后的历史、客户端存下来的转写），`Mask` 会在 `byFake` 里认出它并原样保留。这正是持久化映射解决的二次替换：没有它，新进程会把这个假值当成新的身份证号/手机号/凭据再渲染一次，上游看到第二个假值，而本机再也对不上第一个。

响应侧是还原，且还原器必须装在 `UsageSniffer` **之内**而不是之外：`UsageSniffer.Read` 会把读到的 `p[:n]` 直接写入 cassette，如果还原发生在 sniffer 外层，cassette 里留下的就是假值。

还原不只在写给客户端之前发生。本地 Responses runtime 执行客户端工具时，工具调用参数是从**还原后**的响应里读出来的：`internal/proxy/responses_server.go` 的 `recordResponsesServerChatResponse` 把 `Restorer` 装在 sniffer 之内，`internal/responses/runtime/runtime.go` 再用还原后的文本构造 `FunctionToolCall` 参数。因此客户端工具与本地 runtime 拿到的都是真值；只有**上游托管的工具**（provider-executed / hosted tools）直接看到假值，因为它们在厂商侧执行，本机没有机会还原。

还原是**单遍、单调**的扫描：它只放出自己已经判定过的字节，判定不了的部分留在缓冲区里等下次读取，因此不会重复处理同一个字节。需要等待的情况有两种。

一种是假值跨越两次 `Read`——SSE 事件边界与 `io.Copy` 缓冲区边界都是任意的。扫描停在「已经匹配到某条假值开头、但缓冲区在假值结束前就用完」的位置，等剩余字节到达再判定。

另一种更重要：**厂商把一条消息拆成许多 JSON delta 流式下发，并在 token 边界切断字符串**，于是模型回显的假值会带着 JSON 信封分段到达——第一段以 `sk-proj-AAA` 结束，下一段才继续。只接受连续字节的匹配器在真实流上什么都还原不了，所以一段至少 8 字节、且与某条假值开头吻合的 run 会**开一条链**：跳过中间的信封字节，等后续 run 补齐。整条假值全部凑齐后，各 run 在调用方缓冲区里原地改写；凑不齐的链被原样放回，一个字节都不动。

这条「要么整条还原、要么完全不动」的性质是链能被接受的前提：仅仅「长得像假值」绝不会改写它所在的文本。链的两个界是：run 至少 8 字节才能开链，两段之间最多允许 512 字节间隔（超过就放弃并原样放行，这也限定了「字面量开链但永不解析」时输出被扣住的长度）。

链开着的时候，**一整条假值的优先级高于链**。这不是可选的细节：厂商完全可能把同一个值重复两次，或者在一个开着的链的窗口里出现另一条假值。逐字节地把它当作信封跳过，会让那条真的假值原样交到客户端；而把它的字节当成链自己的续接——当截断的一份后面跟着完整的一份时就会这样，因为那段字节恰好落在链期待的下一个偏移上——会把两份拼成一个「前半真值、后半假值」的怪值。因此扫描每走到一个可能起头假值的字节，就先判断它能否凑成完整的一条；能就改写它并放弃这条链（链的字节原样放行，仍然一个字节都不动）。流式场景下这个判断可能要等更多字节才能做，此时扫描**等待**而不是先消费掉这个字节：字节一旦被判过就不再回看，猜错就等于丢掉一条假值。

vault 为空时每个字节当场判定，未发生遮蔽的流不引入任何延迟。由于假值与真值等长，还原是对调用方缓冲区的原地覆盖，不会移动后续字节。

cassette 记录的是**原始值**，这是有意选择：replay 按请求字节匹配（`pkg/replay`），人读 trace 也需要看到真实请求。代价是 cassette 里存在真实凭据，与 `debug.mask_key` 只处理请求头不同，这一点在 `SECURITY.md` 中有运维提示。

## 检测插件化

检测算法按「算法族」插件化，复用渠道插件的范式（编译期目录 + 运行期启用名单 + admin API + Monitor tab），实现位于 `internal/detector`。**替换本身不可插拔**：插件只决定「检测到什么」，保形假值的渲染、映射持久化、幂等、抑制名单与流式还原仍由 `secretmask.Vault` 集中强制。

- 目录与启用名单：`internal/detector` 注册内置插件描述符（`Plugin`：id/双语名/描述/kind/contract/lanes/rules）。启用状态存在应用数据库 `app_settings` 键 `plugins.detectors`（`{enabled:[ids], settings:{id:{…}}, updated_at}`）。**缺行语义与渠道相反**：缺行回退到 `DefaultConfig`（内置遮蔽与观测检测器默认开），以保持插件化之前的行为；已有行则以 `enabled` 为准，读取时按编译目录过滤未知 id。
- 两种执行契约：`scanner`（`pkg/detect.Scanner`，字节+span）喂审计 XIR 投影与热车道遮蔽；`observation`（`internal/analyzer.Detector`，Observation IR）喂重分析 runner。目录、启用、API、UI 共享，仅执行适配不同。
- 内置插件：`secretmask`（凭据/PII DFA + assignment/DSN，hot+audit，默认开）、`access.indicators`（路径/URL，仅报告，audit，默认关以保持 XIR 投影输出不变）、以及五个观测检测器 `behavior.dangerous_shell`/`credential.regex`/`pii.observation`/`safety.provider_refusal`/`tool.error`（audit，默认开）。
- 热车道：`internal/detector.Masker` 实现 `gateway.Masker`，从已发布快照收集启用插件的可遮蔽命中，交 `Vault.MaskCandidates` 渲染。内置 `secretmask` 命中保留 `RuleID`，渲染仍走精确的编译期规则元数据；第三方扫描器命中以 `MaskCandidate.Meta`（`secretmask.Rule` 描述符）携带渲染/审计身份。`Vault.Mask` 现在是 `MaskCandidates` 的内置特例。`MaskEvent.Meta` 让插件规则以自己的名字/厂商/类别写审计，内置事件 `Meta` 为零、行为不变。
- 规则级开关走插件 `Settings`（DFA 是一台编译期机器，不能按规则拆插件）：`secretmask` 支持 `{"disable_pii":true,"disabled_rules":["pii.chinese_id",…]}`。
- 审计车道：`internal/audit.Inspector.Detectors` 从快照取按 Point 索引的引擎（`AuditEngineFor`，取代旧的 `detect.New(secretmask.New())` 单扫描器硬编码）；`internal/reanalysis` 的 runner 由快照 `Runner()` 提供（`analyzer.NewRunnerList` 不做默认回退，禁用全部观测插件即得到空 runner）。
- IR 作用域：每个插件用 `Points`（复用 `pkg/xir/hook.Point` 词汇）声明它作用的 XIR 字段——`secretmask` 作用于 system/message/tool_call/tool_result/reasoning/output 等文本字段，`access.indicators` 只作用于两侧 tool_call.arguments。审计投影节点经 `internal/audit.NodePoint` 映射到 Point，`detectObservation` 只在该 Point 声明的插件上扫描，`Detection` 输出携带 `point`。**加密内容结构性跳过**：`encrypted_content`/`thinking`/`inlineData` 等在 IR 里是 `xir.Sealed` 块，投影只保留 `Raw` 引用、永不生成 `Text`，`NodePoint` 对 sealed 返回空 Point，因此文本检测器根本看不到它们——跳过是字段投影的性质，而非检测器事后识别丢弃。热路径默认仍为字节级（opaque 保护由 `opaque.go` 兜底），IR 作用域在审计车道强制，并可通过 `secret_masking.hot_path_scope: ir` 在热路径启用（见下节）；`Points` 同时是目录事实，Monitor 抽屉展示插件的 IR 作用域。

## 热路径的 XIR 作用域（opt-in）

`secret_masking.hot_path_scope` 选择转发热路径的检测作用域方式：

- `byte`（默认，留空同义）：零解析快路径。`detector.Masker` 用编译期 DFA 扫整个 body，opaque 由 `opaque.go` 兜底。这是插件化之前的行为，性能不变。
- `ir`：`Masker.MaskWithProtocol` 先把请求体解析为 XIR 投影（`xir.ParseDocument` → codec `DecodeRequest` → `pkg/xir/audit.Build` 请求侧），按插件 `Points` 取对应字段的**原始字节 span**（`internal/detector.ParseRequestScope`），只在这些 span 内扫描；Sealed 块不贡献 span，加密内容结构性排除。命中仍是原始字节偏移，交 `Vault.MaskCandidates` 渲染。无法作用域化（未知协议、解析失败、文本节点无 wire span）时**回退字节快路径**，绝不因作用域化漏检。

正确性由差分夹具钉住：`internal/detector` 的语料库（四协议、含加密推理/thinking/inlineData 的 opaque 用例）上，IR 路径与字节路径的 wire 输出与 `MaskEvent` **逐字节一致**（`TestMaskerIRParityWithVault`）；`internal/proxy` 的 `TestHandlerMasksCredentialsIRScope` 端到端验证 IR 模式下上游遮蔽、等长、客户端还原、cassette 原文。Vault 的 `opaque.go` 在两条路径都仍是最终 opaque 守卫。

**性能（arm64，`go test ./internal/detector -bench . -benchmem`，暖 vault、稀疏凭据）**：

| body | byte ns/op | ir ns/op | ir/byte | byte MB/s | ir MB/s | byte allocs | ir allocs |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 KiB | 42944 | 88621 | ≈2.06× | 31.3 | 15.2 | 9 | 180 |
| 19 KiB | 601679 | 1207000 | ≈2.01× | 33.8 | 16.9 | 34 | 2264 |
| 79 KiB | 2517886 | 4927286 | ≈1.96× | 32.3 | 16.5 | 88 | 8885 |

IR 路径约慢 2×、分配高约 20–100×，开销来自 `wire.Parse` + codec 解码 + `audit.Build` 投影（DFA 扫描本身两条路径相同）。因此默认 `byte` 保持快路径，`ir` 为需要字段级精度/结构性 opaque 排除时的显式选择。协议经 `gateway.RequestTransform.Protocol` 穿透（`gateway.ProtocolMasker` 可选接口），透传与本地 Responses 两条路径一致；`byte` 模式下不传协议，gateway 调 `Mask` 而非 `MaskWithProtocol`。
- 发布与一致性：`detector.Runtime` 持有原子快照，启动时构建、Monitor PATCH 成功后 `Republish` 重建；构建失败保留旧快照。管理 API 为 admin-only 的 `GET /api/detector-plugins` 与 `PATCH /api/detector-plugins/{id}`（`{enabled?,settings?,force?}`）；禁用 hot 车道插件且 vault 仍有映射时返回 409 `plugin_in_use`（`mapping_count`），`force` 豁免。`plugins.detectors` 已加入 `internal/transfer` 的导入白名单（仅目标缺行才插入）。

## 审计事件与审计表

一次命中会在**三个地方**留下记录，各自承担不同职责，不能合并：

**1. cassette prelude 事件**（命名遵循 `<domain>.<event>` 约定）：

- `secret.masked`：每个被替换的值一条，属性为 `rule`、`vendor`、`kind`、`start`、`end`、`length`、`score`、`confidence`、`reasons`。
- `secret.restored`：一次响应一条，属性为 `count` 与 `confidence`（当前恒为 `exact`：只有整条凑齐的假值才会被换回）。

两者都**不携带凭据字节**。prelude 会被复制进 trace 索引、发给 Monitor 并被导出，把真值写进去等于把它再散播一份；`internal/proxy/secretmask_e2e_test.go` 与 `secretmask_audit_test.go` 都把这条不变量钉住了。

**2. `credential_mask_audits` 表**——按 trace 保存真值与假值的唯一位置。每个被替换的值一行（凭据与个人数据都写这里；表名是历史名称，不随覆盖范围改名）：

| 列 | 含义 |
| --- | --- |
| `trace_id` / `event_index` | 指向 trace 与 prelude 里第几条 `secret.masked`；两者唯一，重复索引同一份 cassette 会更新而不是新增 |
| `rule` / `vendor` / `kind` / `confidence` / `score` / `reasons` | 判定依据，与 prelude 事件同源 |
| `start_offset` / `end_offset` / `value_length` | 在原始请求体中的位置与长度 |
| `original_value` / `stand_in_value` | 真值与实际上游收到的保形假值 |
| `storage_mode` | 该行写入时的落盘形态，与 `GET /api/secrets/local-key` 报告的一致；`encrypted-local` 表示列里是 `tlsec:v1:` 本地密钥信封 |
| `detected_at` | 改写时刻 |

只有**真正执行了改写的那次请求**能写出这张表：假值由进程内 vault 生成，cassette 只记原始请求体，回填与重建索引的路径没有接入映射文件，所以它们**不写**这张表。因此写入发生在录制收尾（`internal/recorder` 的 finalize），而不是在后台同步里；脱离进程写入的记录一律没有 `original_value`，`UpsertCredentialMaskAudits` 会跳过它们而不是存一行空值。

开启 `mapping_store` 时还有第四份持久化副本：`{{output_dir}}/secret_mask_mappings.db` 按**真值**去重保存同一对值。它不是审计记录——没有 trace、偏移与命中时间，只用于启动加载、假值识别和真值复用；审计读取面不读它，删掉它不影响已有 trace，只影响后续重启能否复用旧假值。它也不按 session 或 trace 分区：整个 `output_dir` 只有一份文件，真值是唯一的键，同一个值在哪条 trace、哪个会话里出现都映射到同一条记录。按 trace 的「哪次请求被改了哪些值」在 `credential_mask_audits` 表里，两者用途不同。清理见「运行中的映射管理（Monitor）」与「映射文件的清理」两节。

**3. 事件中心一条通知。** `source: "secretmask"`、`category: "credential_masked"`、`severity: "warning"`，**每条规则一条**，指纹固定为 `secretmask:rule:<rule>`，重复命中累加 `occurrence_count`、把 `trace_id` 指向最近一次。它刻意不复述真值与假值，只把 `rule`、`vendor`、`kind`、长度、偏移和 `trace_id` 放进 `details_json`，因为这条记录会推给每个 Monitor 会话并长期保留；真值去审计表取。按规则聚合而不是按 trace 聚合，是为了让这张表像既有的 `routing_failure` 一样有界。身份证与手机号替换走同一条通知与 `secretmask:rule:<rule>` 指纹，只是 `rule`/`kind` 不同（`pii.chinese_id`/`national_id`、`pii.cn_mobile`/`mobile`）；两个读入口都按 `kind` 原样显示，不区分凭据与个人数据。

通知与审计行在**同一个事务**里提交：事件插入失败时整批审计行一起回滚，不会留下「审计标签有数字、事件中心没有记录」的半状态；提交成功后才发布实时变更。写入发生在录制收尾的**独立旁路队列**（`internal/recorder` 的 mask audit side queue）上，不占请求 goroutine，也不占负责重写 cassette 的 finalize worker；瞬时数据库错误会按短退避重试，队列满或尚未启动 worker 时退回同步写入，最坏情况是旧延迟而不是丢行。服务启动时以及之后每 5 分钟，`ReconcileCredentialMaskEvents` 会按 `rule` 检查已有审计行，只给缺失的事件补一条（`EnsureSystemEvent`，`ON CONFLICT DO NOTHING`），因此升级前或异常退出留下的历史缺口会自动愈合，重复对账也不会累加 `occurrence_count`、覆盖读者设置的 `status` 或改写最近 `trace_id`。

事件表不按 trace 复制行：每条规则仍然只有一条通知，`trace_id` 只保留最近一次命中。按 trace 的归因在**读取时**由审计表补齐——`CredentialMaskTraceMatches` 用 `credential_mask_audits` 的 `("trace_id", "event_index")` 索引把 trace 解析到它命中的规则；`GET /api/events?trace_id=<trace>` 返回该 trace 的 occurrence 视图，`matched_trace_id` 是搜索命中的 trace，事件详情的「追踪」链接与替换对优先读它。因此历史 trace 不增加任何事件行，也不会失去自己的替换对。

读/解决/忽略的状态不在事件行上按规则共享，而是按 occurrence 保存：`credential_mask_event_states` 以 `(trace_id, rule)` 为主键，只在读者操作后写入 `read`/`resolved`/`ignored`（以及对应时间），没有行就是 `unread`。这使一条 trace 的忽略不会影响其他 trace 中同一规则的事件，表的大小只随“读者操作过的 occurrence”增长，而不是随每次脱敏增长。事件行上的 `status`/`read_at`/`resolved_at` 只是最近一次 occurrence 的缓存，写路径在 upsert 后从状态表同步；历史 occurrence 的操作不会改动这个缓存。全局列表/摘要读缓存，trace 范围读取从状态表覆盖。

API 的 trace 范围统一使用 query 参数，与 `GET /api/secret-masks?trace_id=` 一致：`GET /api/events?trace_id=`、`GET /api/events/<id>?trace_id=`、`POST /api/events/<id>/read|resolve|ignore?trace_id=`。`trace_id` 必须是完整 UUID；`q` 仍可用 UUID 前缀发现，前缀命中多条时按规则取最近一条。`credential_masked` 的 `ignored` 只忽略该 `(trace_id, rule)` occurrence；同一规则在另一条 trace 的 occurrence 默认仍是 `unread`，同一 trace 内新增的值仍属于同一 occurrence，保持已处理状态。其他来源（解析/分析/路由/上游故障）的忽略仍是规则级永久静音。升级到本版本时，`serve` 启动会先一次性把历史上 `ignored` 的 `credential_masked` 事件改回 `unread`（`app_settings` 键 `system_events.credential_mask_visibility_repaired_v1`），再把旧的规则级 `read`/`resolved`/`ignored` 状态迁到最近一次 occurrence（键 `system_events.credential_mask_occurrence_states_backfilled_v1`）；更早的 occurrence 按未读处理，重启不会重复撤销之后的人工忽略。

读取面：

- `GET /api/secret-masks`：分页列表，筛选参数 `trace_id`、`rule`、`window`（与事件中心同一套时间窗词汇）、`page`、`page_size`；`window` 不传时不按时间过滤——这个接口的两个读入口都按 `trace_id` 取某一次录制的替换，读的是录制事实而不是读取时刻，默认 `today` 会把当天之前的 trace 全部藏掉；显式传入才生效。返回值已解密，`storage_mode` 说明落盘形态，`event_index` 是这次替换在该次交换里的序号（同一 trace 的多行按它排序，因为表本身的排序键是检测时刻加随机 uuid）。
- `GET /api/secret-masks/{id}`：单条。
- `GET /api/secret-mappings`：运行中的全局映射文件（真值 → 假值），`q` / `page` / `page_size`；`DELETE /api/secret-mappings/{seq}` 同时删除文件行与内存映射。两接口与 `/api/secret-masks` 一样只要求登录 Monitor，不额外要求 admin。
- `GET` / `POST /api/secret-suppressions` 与 `DELETE /api/secret-suppressions/{id}`：抑制名单的读取、新增与移除；规则留空表示全部规则。与映射接口同样只要求登录 Monitor，不额外要求 admin。
- MCP 工具 `list_credential_masks`：同参数，返回同样的字段。
- Monitor UI 两个入口都读这个接口：事件页的事件详情在 `category: credential_masked` 时就地列出该规则的替换对（并保留 `details_json` 作为原始证据），trace 详情的「审计」标签页在发现项之上先列出这条请求里所有被替换的凭据；从事件详情点进 trace 会直接落在「审计」标签页，因为答案在那里。两个入口都把真值与假值分行完整打印，并给出偏移、长度、置信度与 `storage_mode`。

密钥轮换或丢失时，无法解密的行仍会被列出，`original` 返回空——`rule`、`trace_id`、偏移仍能用于定位，旁边的 cassette 里就有原文；一条读不出来的行不该让整页查询失败。

## 已知边界

- **审计表与映射文件都是凭据与个人数据存储。** 收起真值是为了给审计看，代价是 `credential_mask_audits` 里躺着真值（身份证号、手机号同样如此）：有本地密钥时是 `tlsec:v1:` 信封，没有时是明文（逐行记 `storage_mode`）。`secret_mask_mappings.db` 同样保存真值与假值，只是按真值去重、只存 `real`/`fake`，不含 trace、偏移与检测时间。两者都应当按 cassette 目录的级别对待，而不是按日志；`GET /api/secret-masks` 与 MCP `list_credential_masks` 会把审计表读出来，`GET /api/secret-mappings` 与 `GET /api/secret-suppressions` 会把映射文件和抑制名单读出来，任何能访问 Monitor 的账号都能看到。需要缩短留存时按 `detected_at` 手工删除；删行不影响 replay 与 trace 详情。
- **PEM 私钥块只在完整时改写。** 找不到同标签的 `-----END ...-----`（截断粘贴）、footer 与 header 标签不一致、或正文里出现 base64/空白/JSON 转义之外的字节时，块不成立，只剩头行上报、一个字节都不改。因此「只贴了一部分私钥」「header 与 END 被拼错」仍然会漏；老式 `Proc-Type: 4,ENCRYPTED` 头（正文前有 `:`/`,` 行）也不在块内，只上报头行。PuTTY `.ppk`、`---- BEGIN SSH2 ENCRYPTED PRIVATE KEY ----`（4 个横线）与 base64 套壳（kubeconfig `client-key-data`、K8s Secret）需要各自的规则，当前不覆盖。公钥与证书按设计不匹配。
- **私钥假值不可用。** 块级假值的正文仍是 base64 形状，但解码后不是有效私钥（`ssh-keygen` 会报错）。依赖「模型先用假值解析/校验 key」的流程会失败；本地工具拿到的是还原后的真值（除 provider 托管工具）。
- **升级前的旧假值不在文件里。** 首次启用持久化（或从没有映射文件的版本升级）时，之前进程渲染的假值没有记录；它们第一次回到请求里仍会被再遮蔽一次，之后该对关系才会被记录。要避免这一次，只能在升级时清空上游上下文或重新开始会话。
- **异步写入有一个很短的持久化窗口。** 新映射先写内存、最多 25 ms 后落盘；进程被强杀时最后一批可能丢失，重启后对应假值会被再遮蔽一次。正常关闭（`Handler.Close`）会排空队列。
- **只有正数 `load_limit` 才有识别窗口。** 默认加载全部，任何记录过的假值都能被 `byFake` 认识。设置正数后，一条比窗口更旧、且已从内存淘汰的假值如果再次出现，会被当成新值再遮蔽一次；真值本身不受影响，它第一次再次出现时会按 `by_real` 查文件并复用原假值。窗口越大覆盖越久，代价是启动内存。
- **映射文件是本机、单实例文件。** bbolt 有排他文件锁：同一个 `output_dir` 不能被两个 Trajecta 进程同时打开映射文件，第二个进程会在启动时报错，而不是静默降级。它也不跨机器共享——在另一台机器上，同一个真值会渲染出不同的假值。共享 `output_dir`（NFS 等）不适合放 bbolt 文件；把 `mapping_store.path` 指到本地磁盘，或关掉它。
- **映射文件打不开时启动失败。** 路径不可写、文件损坏或被另一个进程锁住时，`newSecretVault` 返回错误，handler 创建失败，进程退出。这里刻意不静默退回纯内存：静默退回会让重启后的假值重新变成第二个假值，正是本能力要修的问题；要么修好文件，要么显式设置 `mapping_store.enabled: false` 接受重启失效。
- **本地密钥轮换后旧映射不可读。** 映射值用 `trace_index.secret` 封存，`by_real` 也用同一把密钥做 HMAC；轮换密钥后，旧行无法解密、新查找也按新 HMAC 落空。代理会跳过不可读行并重新渲染、写入新行，旧行成为孤儿；需要干净重建时删除 `secret_mask_mappings.db`。这与 `credential_mask_audits` 在密钥轮换后读不出 `original` 的边界一致。
- **只有上游托管的工具看不到真值。** 客户端工具与本地 runtime 执行工具时拿到的是还原后的真值（见「在代理中的接入点」），HMAC 签名、AWS SigV4、签发 JWT 这类必须在真值上运算的操作在那里可以完成；provider-executed / hosted tools 在厂商侧执行，只看到假值，同样的操作会失败。
- **身份证的生日不保留。** 假值的出生日期是重新渲染的合法日期，不是真值里的那一天。任何把身份证号与另一处出生日期交叉比对的下游（例如 KYC）都会对不上；需要这个一致性的流程应关闭遮蔽，或只依赖号码自身的校验位。
- **手机号的后 8 位不保留。** 假值只保留号段，后 8 位是重新渲染的。依赖末四位做展示、核对或「请输入完整号码」类校验的下游会对不上；号段本身是公开的运营商分配，不构成个人信息。
- **逐 token 的极小分片不重组。** 链从假值的第一段开始跟；普通假值的开链门槛是 8 字节，长假值（如私钥块）是 16 字节。对 SSE 的 JSON 字符串分片，长假值在首段止于该字符串的结束引号时会破例收下短于门槛的首段（`restore.go` 的 `chainStart`），因此 17 字节甚至 5 字节的首段都能开链；真正不重组的是既短于门槛、又不在 JSON 字符串边界的切法，例如逐字节的纯文本流，或首段后紧跟非引号字节。此时客户端拿到的是拼接后的**假值**而不是真值——功能上失败，但没有改写任何无关字节。（注意 `content` 与工具调用参数都走同一条路径，所以这一条对两者都成立。）手机号假值只有 11 字节，8 字节的开链门槛意味着首段被切到 7 字节以下（例如 6+5）就不会重组；身份证 18 字节的余量稍大，但同受这条限制。
- **`/tokenize` 计数请求不遮蔽。** 本地 Responses runtime 的 token 计数器会把待压缩的对话正文原样 POST 到上游的 `/tokenize` 端点（`internal/responses/runtime/tokenize_counter.go`，经 `runtime.go` 的 `EstimateResponsePromptTokens` 在自动压缩时调用），这条路径没有接入 vault。它需要显式配置才启用：`model_profiles` 里要有 `name`/`pattern`、`context_window_tokens`，以及声明了 `capabilities.tokenize` 的启用目标，或用 `tokenize_counter.enabled` 显式打开；跟踪的默认配置里没有 `model_profiles`，因此当前不生效。一旦启用，它发出的正文里含真实凭据。
- **不透明 JSON 字段的边界。** 跳过区间只来自单个完整、无尾随字节的 JSON 文档：NDJSON 或「一份 JSON 后接垃圾」的请求体会退回常规遮蔽，免得一个非法体的区间把真凭据一起跳过。字段名预筛按字面字节匹配，所以把 ASCII 成员名写成 `\u` 转义的请求体（例如 `"encrypted_\u0063ontent"`）不会被识别；标准 JSON 编码器不会这样输出 ASCII 键名，这属于已记录的行为边界，届时密文会按旧行为参与扫描。`url`/`image_url` 只在值是 base64 data URI 时跳过，普通远程 URL 仍参与扫描。整块规则（`thinking`、`redacted_thinking`、`reasoning`、`reasoning.*` 等）按对象 `type` 的结构匹配，不按 provider 或请求路径限定：如果客户端自己的工具参数 JSON（例如 Gemini `functionCall.args`、Anthropic `tool_use.input`）恰好使用了这些 type 值，对象内的字符串也会被整块跳过；这是为了签名完整性而接受的代价。`thinking` 块整块跳过意味着块内即使出现凭据形态也不会改写——同样是为了维持签名；响应侧还原仍按字节匹配假值，如果模型在思考块里回显了假值，客户端拿回真值后再回传仍可能破坏签名，这条属于还原侧的已知边界。
- **原生通道的边界。** env 赋值只接受全大写的 `_KEY`/`_PASSWORD` 名字，因此 `*_TOKEN`、`*_SECRET`、`PGPASSWORD`、`dbPassword`、裸 `KEY`/`PASSWORD` 与表外的协议名都不再改写——这是用漏报换误报的取舍（`GITHUB_TOKEN`、`JWT_SECRET` 这类没有词法形状的值会漏）。`assignmentExcludedNames` 仍排除已知协议名（`PROMPT_CACHE_KEY`、分页/排序/分区 key、token 计数器等），表外的 `*_KEY` 协议字段仍可能被当成秘密改写；`_KEY` 的 value 要求 ≥ 16 字节、`_PASSWORD` ≥ 6 字节。未加引号的 value 另拒绝代码表达式（含 `(`/`[`/`{`/`<`，或首段是小写标识符的点号成员访问），这是代码片段误报的另一半修复；以大写词开头的点号值仍可能被当成密码（`Correct.Horse.Battery` 会改写），而全小写词加点号的密码会漏（`my.pass.word`）。引号内超过 1024 字节的值直接跳过（真实秘密远短于此，长私钥仍由 PEM 规则在值内命中）；URL 类值没有 userinfo password 时不做任何改写；值只在请求方向被改写，cassette 与详情页仍是原始 body，客户端通过响应还原看到真值。数值型整值配置（`MAX_TOKENS=5529`、`PASSWORD_LENGTH=12`）与占位符不会进入改写。
- **DSN 只覆盖封闭的 scheme 列表。** `dsn.password` 只认 `dsn.go` 里列出的数据库/缓存/消息队列 scheme，以及 SQLAlchemy 风格的 `scheme+driver` 拼写（`postgresql+psycopg`、`mysql+pymysql`；按 `+` 前的基础 scheme 归属）；`https://user:pass@host`、`ssh://`、`ftp://` 之类不在列表内，除非挂在全大写 URL 类名字下（`env.url_credential`）。userinfo 里合法的 sub-delims（`! $ & ' ( ) * + , ; =`）与 `:` 可以原样出现，会被整段改写；userinfo 扫描在控制字符/空白、非 ASCII 字节、`"`、反引号、`\`、`< > | ^ { } [ ]` 与 URL 结构字符 `/ ? #` 处停止，password 里未 percent-encode 的 `/`、`?`、`#`、空白因此会截断 authority，这段值可能识别不到（这些都是 RFC 3986 要求 percent-encode 的字符，`'` 是例外：它合法且不作为终止符，所以 `postgres://u:pa'ss@h/db` 会整段改写）。未 percent-encode 的 `@` 是 URL 的 userinfo 分隔符：按第一个 `@` 切分，只改写它前面的部分（RFC 3986 要求 password 里的 `@` 写成 `%40`）。由此带来的语法歧义：无空白分隔的 `,`/`;`/`=` 后紧跟一个带 `@` 的 token 时，整段仍是一个合法 URL，会被解析成 userinfo（例如 `postgres://db:5565,user@example.com` 的 password 是 `5565,user`）；用空白、双引号/反引号或路径分隔两个 token 即可避免。某些编码器把 `/` 写成 JSON 转义形式 `\/`（`postgres:\/\/user:pass@host`）时不识别。没有 userinfo 的 DSN 不改写。新服务要覆盖时把 scheme 加进 `dsnSchemes` 并补一条测试即可，不需要改 DFA 或重新生成。
- **派生与编码形式绕过。** 引擎不做熵估计、不支持 look-behind，也只解码校验器真正需要的那部分（JWT 的三段、JWE 的头部、GitHub 与 age 的校验位）。被编码、截断、改大小写或重新格式化过的凭据识别不到。
- **不阻止「借刀杀人」。** 它保护值不泄露给上游，但不阻止模型通过工具调用让下游系统去使用该凭据。

## 度量

以下数字来自 `go test ./pkg/secretmask -run '^$' -bench . -benchmem -count=3` 在 24 线程机器上的稳定值，看数量级即可；`task bench:secretmask` 跑同一套（`-count=1`，更快但抖动略大）。基准载荷是 transcript 形状：散文、代码、40 位 commit hash、MD5、UUID 与稀疏凭据混合，比纯英文更接近真实 agent 请求。请求扫描的数字包含原生遍历（env 赋值 + DSN）：该遍历与 DFA 扫同一份字节，基准显示比只跑 DFA 多约 5–9% 扫描成本（取决于 body 里 `=`/`:` 与 `://` 的密度），命中路径零分配。

### 请求与响应路径

| 场景 | 载荷 | 耗时 | 吞吐 |
| --- | --- | --- | --- |
| 词法扫描（请求体） | 4 KiB | 56 µs | 74 MB/s |
| 词法扫描 | 64 KiB | 0.89 ms | 74 MB/s |
| 词法扫描 | 512 KiB | 7.2 ms | 73 MB/s |
| 遮蔽（热 vault） | 64 KiB | 0.93 ms | 71 MB/s |
| 遮蔽（热 vault） | 512 KiB | 7.4 ms | 71 MB/s |
| 还原（无命中 / 有命中） | 64 KiB | 0.47 ms | 139–142 MB/s |
| 还原（无命中 / 有命中） | 512 KiB | 3.8 ms | 138–141 MB/s |
| 流式还原（512 B–64 KiB 分片） | 256 KiB | 1.83–1.91 ms | 137–143 MB/s |
| 一次往返（Mask + Restore 各 64 KiB） | 128 KiB | 1.4 ms | 94 MB/s |

- 64 KiB 请求扫描约 0.9 ms，完整请求 + 响应往返约 1.4 ms；512 KiB 请求约 7 ms。4 KiB–512 KiB 的吞吐基本不变，说明扫描没有超线性的退化。
- 载荷内容对吞吐影响很大：散文占主导的 `BenchmarkMaskWarm` 109 KB 约 222 MB/s，而 hash/UUID 密集的 transcript 形状约 70–75 MB/s。引用吞吐数字时要说明载荷；上表用更保守的 transcript 形状。
- 凭据密度：64 KiB 无凭据约 0.89 ms，稀疏（约每 20 KB 一条）约 0.89 ms，中等（约每 2.5 KB 一条）约 0.91 ms，每条消息都带凭据的密集载荷约 1.02 ms。校验与上下文打分只对候选执行，所以正常密度几乎不改变成本；最坏情况仍是下一条的手机号。
- 身份证命中本身：每 64 KB 一条时约 231 MB/s；每 64 KB 约 3500 条时约 151 MB/s（`BenchmarkScanDenseChineseIDs`），因为 `SkipContext` 跳过了上下文扫描。手机号因为不能跳过上下文：每 64 KB 一条（带 `phone=`）时约 211 MB/s，每 64 KB 约 3500 条时约 21 MB/s（`BenchmarkScanDenseMobileNumbers`，24 字节窗口），这是该规则的已知最坏情况。
- 遮蔽 109 KB 散文载荷：热态约 222 MB/s，冷态约 210 MB/s（冷态包含渲染假值）。
- PEM 私钥块：块扩展只在头行命中时跑一次 `bytes.Index` 级的扫描，不改变每字节成本；首次渲染一把 594 B 的 ed25519 约 27 µs、3 KB 的 RSA 约 0.2 ms（密钥流每 32 字节一次 HMAC-SHA256），同一把 key 再出现时约 1–2 µs。还原 70 KB 散文仍约 282 MB/s；vault 里有一把 3 KB 块时也约 290 MB/s。
- 长假值在还原侧的开链门槛是 16 字节而不是 8：私钥块的 `-----BEGIN ` 与所有 PEM 头共享 11 字节，8 字节门槛会让响应里每个证书头都开一条永远凑不齐的链，在 PEM 密集的极端载荷上把还原吞吐从约 280 MB/s 拖到约 50 MB/s，并让流式输出暂扣 512 字节。同一份极端载荷在 16 字节门槛下不再开链。
- 映射文件不改变请求热路径：已加载或已命中的值只查内存 map，只有首次遇到某个真值时才查一次 bbolt；新值渲染后的写入在后台批量提交。启动加载是一次性成本，默认加载全部，规模由不同敏感值的数量决定；正数 `load_limit` 可限制启动内存。
- 还原：散文载荷约 340 MB/s，流式约 348 MB/s；上表的 transcript 载荷约 139 MB/s。跨 delta 重组只增加一次判定，代价已包含在内；「整条假值优先于链」的判定在链开着时逐字节执行，这部分开销也在测量值里，换来的是不漏还原、不拼接。
- 精度：对 `git ls-files` 的 1,217 个文本文件（约 25 MB，另加本地 `config/config.dev.yaml`）扫描得到 135 处命中、40 个文件（High 102、Medium 15、Low 18）。除 `config/config.dev.yaml` 里 2 处真实 key 外，其余全部是测试夹具、文档示例或已知的 Low 误报（`go.sum` 里形似 `LTAI` 的串、`ent/dao` 生成代码里的常量、HTML 快照里的一个 11 位数字等）；Low 一律不会改写。本次收紧的原生规则命中都落在测试夹具与文档示例里（`env.secret_assignment` 19 处、`env.url_credential` 9 处、`dsn.password` 8 处）；在仓库跟踪的 663 个 `.go`/`.ts`/`.js`/`.py` 源文件（约 13 MB，排除 `*_test.go`）上只有 3 处命中、**0 处可改写命中**——工具结果里回传的代码不会再被当成凭据改写，这正是本次修复的目标。
