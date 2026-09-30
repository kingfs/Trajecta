# 从 llm-tracelab 迁移到 Trajecta

项目在 `v2.0.0` 从 `llm-tracelab` 改名为 Trajecta。改名不只是字符串替换：环境变量前缀、默认数据库文件、cassette prelude magic 都换了名字，而且应用数据库已经从 SQLite 转为 Postgres 唯一事实源。

本文描述当前二进制提供的迁移路径。迁移工具是独立 CLI `trajecta`（源码在 `cmd/trajecta`，逻辑在 `internal/legacymigrate`），迁移命令都挂在 `trajecta upgrade` 下，它负责四件事：

1. 读取部署 `.env`，把旧前缀变量映射成当前前缀并加载配置；
2. 读取改名前的 SQLite 应用库，把其中的条目合并进 Postgres（重复即跳过）；
3. 并发重写 `.http` cassette 的 prelude magic（只替换首行）；
4. 只按格式校验 cassette，并在校验通过后归档旧 SQLite 文件。

**所有命令默认 dry-run，只有显式传 `--apply` 才会写盘。**

## 迁移后的目标状态

| 项目 | 迁移前 | 迁移后 |
| --- | --- | --- |
| 环境变量前缀 | `LLM_TRACELAB_*` | `TRAJECTA_*`（`.env` 与 `docker-compose.yml` 都要改） |
| 应用数据库 | `{{output_dir}}/llm_tracelab.sqlite3`、`trace_index.sqlite3` | Postgres（唯一事实源） |
| cassette prelude magic | `# llm-tracelab/v3` | `# trajecta/v3`（读取端两者都接受） |
| 本地 SQLite 文件 | 被 `serve` 打开 | 改名归档为 `*.migrated`，不再被读取 |

归档是**重命名**，不是删除：迁移后不再有 SQLite 本地文件被使用，但旧文件仍在磁盘上，可随时回滚读取。

## 前置条件

- 目标 Postgres 可连接，并已应用应用库 schema：`server db migrate up -c config.yaml`（`trajecta upgrade db` 只写数据，不建表）。
- 一个可用的 `config.yaml` 或等价环境变量；`.env` 会被自动读取。
- 二进制：`task build:go` 会同时产出服务端 `server` 与 CLI `trajecta`，也可以单独构建：

```bash
go build -trimpath -o trajecta ./cmd/trajecta
```

### 从宿主机跑，还是从容器里跑

Postgres 由 compose 提供时，`.env` 里的 DSN 主机名通常是 compose 服务名（`postgres:5432`），宿主机解析不了这个名字。两种做法：

```bash
# 1) 在 compose 网络内运行（推荐：DSN、目录、.env 都不需要改）
docker compose run --rm --entrypoint /app/bin/trajecta trajecta upgrade env
docker compose run --rm --entrypoint /app/bin/trajecta trajecta upgrade --apply

# 2) 在宿主机运行，把 DSN 指向已发布端口
TRAJECTA_DATABASE_DSN='postgres://user:pass@127.0.0.1:5432/llm_tracelab?sslmode=disable' \
  ./trajecta upgrade --apply --trace-dir ./data/traces
```

容器里的 `/app/data/traces` 必须是同一个卷，否则 cassette 与 SQLite 文件会在容器内不可见。

## 第 0 步：只读确认现状

```bash
./trajecta upgrade env               # .env、有效配置、发现的旧库、compose 前缀检查
./trajecta upgrade sqlite list       # 将要迁移的 SQLite 文件（路径、大小、mtime）
./trajecta upgrade cassettes census  # cassette 格式普查（只读首行，很快）
```

`env` 会打印每条 `.env` 变量的去向（`LLM_TRACELAB_X → TRAJECTA_X`，值中的 DSN 密码会被打码）、有效 `database.driver`/`dsn`、实际搜索目录，以及 `docker-compose.yml` 是否仍在传旧前缀变量。

### 变量前缀与 compose 必须一起改

当前二进制只读 `TRAJECTA_*`，代码里没有 legacy 前缀回退。compose 里继续传 `LLM_TRACELAB_*` 不会报错，但会被静默忽略，应用会退回 `config.yaml` 中的配置（默认可能是 `database.driver: sqlite`）。`trajecta upgrade env` 会对这种组合给出警告；改 `.env` 时必须同步改 compose 的 `environment:` 段。

### 不要让 `config.yaml` 继续指向 SQLite 文件

`database.dsn` 指向 SQLite 文件时，改动文件名而不改 `dsn`，会让 SQLite 驱动**新建一个空库**（`auto_migrate: true` 还会顺手建表），看起来像数据丢失。迁移到 Postgres 后，把 `database.driver` 与 `database.dsn` 都指向 Postgres。

## 第 1 步：把 SQLite 条目合并进 Postgres

```bash
./trajecta upgrade db                 # dry-run：报告每张表将要写入多少行
./trajecta upgrade db --apply         # 实际写入
```

行为与保证：

- **幂等**：每行用 `INSERT ... ON CONFLICT DO NOTHING` 写入，主键或其它唯一键已存在的行会被跳过并计入 `duplicate`，可以反复执行。
- **只写交集列**：只迁移 SQLite 与 Postgres 都存在的列。Postgres 侧 `NOT NULL` 且无默认值、而旧表没有的列会让该表进入 `blocked` 状态并打印列名；确认可以用占位值填充时加 `--fill-missing-required`。
- **时间戳**：旧库里的时间列存在四种历史编码——raw SQL 的 RFC3339Nano（`2025-12-23T12:17:14.088863521Z`）、SQLite 驱动写入的 `time.Time.String()`（`2026-05-15 01:54:55.069380977 +0000 UTC`）、带单调时钟后缀的同一格式（`... m=+0.095068526`）、以及 `CURRENT_TIMESTAMP` 的 `2026-01-02 03:04:05`。三者都会解析为 UTC；Postgres 的 `timestamptz` 精确到微秒，纳秒部分由 Postgres 四舍五入。
- **布尔**：旧库用 `numeric` 存布尔，`0/1`、`true/false`、`yes/no`、`t/f` 都能转换。
- **自增主键**：Postgres 的 identity 列由数据库生成，值按源数据写入；写完一张表后，工具会把对应序列推进到不小于 `MAX(列)`，且**绝不回退**（例如 `channel_models` 的 `START WITH 47244640256` 不会被拉低）。
- **源库记账表跳过**：`schema_migrations`、`app_schema_status` 属于源库自身的迁移记账，不迁移。
- **迁移后校验**：每张表写完后会把源库主键流式取出，逐个确认在 Postgres 中存在；缺失数不为 0 时以退出码 1 结束。`--verify-only` 只做这项校验而不写任何数据（`sqlite archive` 的归档闸门就是它）。插入用的是 `ON CONFLICT DO NOTHING`，任何唯一键命中都会跳过该行，所以校验也按同一语义比对：先比 Postgres 主键，未命中的行再比该表的**其它唯一键**（完整、非部分、非表达式索引，主键优先）。通过次级唯一键命中的行计入 `alt_key_matched` 并在报告中单独列出（例如服务已用自增主键建过同一 `username` / `token_hash` / `(upstream_id, model)`，旧行因此带着旧代理主键），不算缺失；只有**任何**唯一键都不命中的行才计入 `missing_keys`。`logs` 的唯一键 `trace_id` 让 `layout apply` 改写过 `logs.path` 的库也能对账。校验分两遍：第一遍只流式取主键列（命中主键索引，代价与只看主键时相同），只有主键未命中的行才回查次级唯一键的列。这样即使表上还有别的唯一索引（例如 `semantic_nodes` 除主键 `id` 外还有 `(trace_id, node_id)`），也不会因为要一次取出所有键列的并集而失去覆盖索引、退化成全表扫描；除主键外没有任何可用唯一键时，仍以复制阶段的行数记账作为闸门。
- **并发**：表之间按外键依赖分波并行（当前 schema 里唯一的外键是 `api_tokens.user_tokens → users.id`），行按批（`--batch-size`，默认 500）插入；批内出现数据异常或唯一键冲突时，该批会退化为逐行隔离，只把真正失败的行计入 `failed`。

常用参数：`--table` / `--skip-table`（可重复）、`--batch-size`、`--jobs`、`--sqlite`（显式指定库文件，可重复）、`--sqlite-open auto|ro|immutable|rw`。

## 第 2 步：重写 cassette magic（可选，但推荐）

```bash
./trajecta upgrade cassettes rewrite            # dry-run
./trajecta upgrade cassettes rewrite --apply
```

读取端同时接受 `# llm-tracelab/v3` 与 `# trajecta/v3`，所以这一步不是必须的；它的价值是让整个 cassette 库与当前写入端一致。

安全性：

- **只替换首行**，且只有首行恰好是 `# llm-tracelab/v3` 时才替换；首行是别的内容（例如 V2 录制）时只计数不改动。
- 录制内容逐字节拷贝（1 MiB 缓冲），新长度比旧长度少 4 字节，不做任何重新序列化。
- 写入同目录临时文件（`.trajecta-magic-*.tmp`）→ `fsync` → 保留原权限与 mtime → 原子 `rename`。中断不会截断或破坏任何 cassette；`--apply` 前会先清理上次中断遗留的临时文件。
- `--verify`（默认开启）会在重写后重读首行确认。

## 第 3 步：只校验格式

```bash
./trajecta upgrade cassettes check
```

只检查**格式**，不检查内容：magic 行、`# meta:` / `# event:` 的 JSON 是否可解析、`layout` 里声明的四段长度加上 payload 偏移是否正好等于文件大小。录制内容本身从不读取，所以半截录制会以「长度不自洽」被报出来，而不是被当成内容错误。

- 默认：`# llm-tracelab/v3` 是 warning（读取端兼容），长度不自洽是 error。
- `--fail-on-legacy`：把旧 magic 升级为 error。
- `--fail-on-v2`：把旧 `LLM_PROXY_V2` 录制视为 error。
- `--strict`：所有 warning 升级为 error。
- `--tolerate-partial`：把长度不自洽降级为 warning（适合已知存在中断录制的库）。
- `--max-issues`：限制打印的问题条数（默认 200）。

## 第 4 步：归档旧 SQLite 文件

```bash
./trajecta upgrade sqlite archive            # 先校验，再报告计划
./trajecta upgrade sqlite archive --apply
```

归档前会对每个库跑一次 `--verify-only`：只有每张表的源主键都能在 Postgres 中找到才执行重命名。`--force` 可跳过该校验（不推荐）。`--suffix` 默认 `.migrated`；目标名已存在时追加 UTC 时间戳，不会覆盖。`-wal` / `-shm` / `-journal` 会跟着一起改名。

**运行时快照表的漂移**：`upstream_targets` 与 `upstream_models` 不是权威记录，而是运行中的服务按当前 provider 配置与探测结果重写的运行时快照与兼容投影（见 `docs/ROUTING_AND_CREDENTIALS.md`）。因此服务运行期间，旧库里这两张表的行可能已被当前状态替换而不在 Postgres 中——这属于设计行为，不是丢数据。这类缺失默认仍会让闸门拒绝归档；确认过「缺失全部落在快照表内」后，可用 `--tolerate-snapshot-drift`：

```bash
./trajecta upgrade sqlite archive --tolerate-snapshot-drift          # 先看计划与 tolerated 计数
./trajecta upgrade sqlite archive --tolerate-snapshot-drift --apply
```

该开关只豁免 `upstream_targets`、`upstream_models` 这两张表，并在报告里单独打印 `tolerated` 计数（`upgrade db --verify-only` 同样支持），其余每张权威表（`logs`、`parse_jobs`、`semantic_nodes`、`trace_observations`、`users`、`api_tokens` 等）仍逐主键严格校验；任何权威表缺键时依旧拒绝归档。它比 `--force` 更可取：`--force` 会取消全部校验。

## 一键执行：`trajecta upgrade`

```bash
./trajecta upgrade            # dry-run 全流程
./trajecta upgrade --apply
```

顺序为「合并数据库 → 重写 magic → 校验 → 归档」。任何一步报告失败行或结构错误时都不会归档；`--skip-cassettes` 跳过第 2、3 步，`--skip-archive` 保留旧文件。

## 命令与退出码

| 命令 | 作用 | 是否写盘 |
| --- | --- | --- |
| `trajecta upgrade env` | 加载 `.env`、打印有效配置、列出旧库、检查 compose 前缀 | 否 |
| `trajecta upgrade db` | SQLite → Postgres 合并（`--apply` 才写） | 需 `--apply` |
| `trajecta upgrade db --verify-only` | 只确认源主键都在 Postgres | 否 |
| `trajecta upgrade cassettes census`（别名 `version`） | 统计 cassette 格式分布 | 否 |
| `trajecta upgrade cassettes rewrite` | 重写 prelude magic | 需 `--apply` |
| `trajecta upgrade cassettes check` | 校验 cassette 格式 | 否 |
| `trajecta upgrade sqlite list` | 列出旧库文件 | 否 |
| `trajecta upgrade sqlite archive` | 归档旧库文件（`--tolerate-snapshot-drift` 只豁免两张运行时快照表） | 需 `--apply` |
| `trajecta upgrade` | 上述流程串联 | 需 `--apply` |
| `trajecta layout plan` | 只读地报告 cassette 目录布局的搬迁计划 | 否 |
| `trajecta layout apply` | 执行搬迁计划：重命名 cassette 并同步索引路径 | 需 `--apply` |
| `trajecta version` | 打印构建信息 | 否 |

退出码：`0` 成功；`1` 命令已执行但发现问题（失败行、缺失主键、结构错误、需要人工处理的表）；`3` 用法或前置条件错误（例如目标数据库不是 Postgres）。

## 通用参数

- `--env-file PATH` / `--no-env`：默认读取当前目录的 `.env`。支持 `export KEY=`、单双引号、行内 `#` 注释、空值；不做变量展开。已经存在于进程环境中的变量优先（`LoadEnvFile` 的 `override=false`）。
- `-c, --config PATH`：配置文件，默认 `config.yaml`。
- `--format text|json`：`json` 时 stdout 输出 `{ok, command, result, warnings}`；进度与警告写 stderr。
- `--jobs N`：并发度。cassette 并发上限为 64，未指定时取 `clamp(NumCPU, 4, 32)`；数据库表级并发未指定时取 `clamp(NumCPU, 2, 8)`。
- `--apply`：所有写操作的唯一开关。
- `--sqlite PATH` / `--trace-dir DIR`：显式指定库文件或搜索目录（都可重复）。发现顺序为 `--sqlite` → `--trace-dir` → 配置的 trace 输出目录 → `data/traces`、`logs`、`data`；候选文件名为 `trajecta.sqlite3`、`llm_tracelab.sqlite3`、`trace_index.sqlite3`，按 mtime 从新到旧。

## 安全与一致性

- **SQLite 只读**：默认 `auto` 先尝试 `mode=ro`，被文件系统拒绝时回退 `immutable=1`。`immutable` 在存在非空 `-wal`/`-journal` 时会被拒绝，避免读到过期快照；打开可写（`rw`）需要显式指定。
- **进度可观测**：长任务每 2 秒打印一行进度（扫描/重写/复制/跳过/失败计数）。
- **`trace_index.secret`**：`{{output_dir}}/trace_index.secret`（权限 600）是 `channel_configs.api_key_ciphertext` 与部分 `headers_json` 的本地加密密钥。迁移 SQLite 数据时**保持它与数据目录在一起**，否则历史密文在新环境里无法解密。
- **不要在校验失败时归档**：归档闸门依赖主键校验；`--force` 只应在确认过源数据已被 Postgres 覆盖后使用。若缺失只出现在运行时快照表 `upstream_targets` / `upstream_models` 中，用 `--tolerate-snapshot-drift` 精确豁免这两张表，而不是用 `--force` 关闭全部校验。

## 故障处理

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| `the target must be Postgres, but the configuration resolves to "sqlite"` | `config.yaml`/环境变量仍指向 SQLite | 设置 `TRAJECTA_DATABASE_DRIVER=postgres` 与 `TRAJECTA_DATABASE_DSN`，或改配置 |
| 某表 `blocked`，打印 `missing required columns` | Postgres 侧 `NOT NULL` 且无默认值的列在旧表里不存在 | 先确认该列语义；可接受占位值时加 `--fill-missing-required`；否则先手动补齐再迁移 |
| 某表 `failed > 0` | 单行数据无法转换（类型、超长、非法 JSON 等） | 查看打印的失败样本；修正源库行或先手动导入，再重跑（幂等） |
| `missing keys > 0` | 有行没写进 Postgres（转换失败或被跳过） | 不要归档；先解决失败行 |
| `missing keys > 0` 且**只在** `upstream_targets` / `upstream_models` | 服务已按当前配置/探测结果重写了这两张运行时快照表，旧行被替换而非丢失 | 确认缺失键都在这两张表内后，用 `--tolerate-snapshot-drift` 归档；报告会打印 `tolerated` 计数 |
| `missing keys > 0` 出现在 `logs` / `parse_jobs` / `semantic_nodes` / `trace_observations` 等权威表 | 有行没写进 Postgres（转换失败或被跳过） | 不要归档；先解决失败行 |
| `unable to open database file (14)` | 文件系统不允许只读打开 | 使用默认 `auto`（会回退 `immutable`）或显式 `--sqlite-open immutable` |
| `prelude_unterminated` / `layout_mismatch` | 录制被中断，前言没有空行或长度不自洽 | 用 `--tolerate-partial` 降级为 warning，或删除该录制后重建索引 |
| compose 里旧前缀变量被忽略 | 二进制只读 `TRAJECTA_*` | 同步更新 `docker-compose.yml` 的 `environment:` |

## 与 `scripts/migrate-to-trajecta.sh` 的关系

`scripts/migrate-to-trajecta.sh` 仍然可用，适合只做「`.env` 前缀改写 + SQLite 文件改名 + 人工确认清单」的轻量场景（它默认 dry-run，并在写入前备份 `.env`）。它不适合大库：cassette 阶段为每个文件起一个 `head` 进程，数十万文件会非常慢。

CLI `trajecta upgrade` 是当前推荐路径：它读同一份 `.env`、并发处理 cassette、把 SQLite 条目真正合并进 Postgres，并在验证通过后归档文件。

## 整理 cassette 目录布局（可选）

录制端一直按 `<upstream site host>/<model>/<YYYY>/<MM>/<DD>/<name>.http` 写文件，但两类历史数据会偏离这个形状：模型名本身含 `/`（例如 `feature/gpt-5.6-sol`），以及当时没有解析出 upstream 的录制（只有 `<model>/<YYYY>/<MM>/<DD>`，prelude 里的 `meta.url` 是相对的 `/v1/responses`，真实 site 已经无法恢复）。

`layout plan` 只读地给出搬迁计划，不会创建、移动、改名或删除任何文件：

```bash
./trajecta layout plan                                  # 读取 cassette root 下每个文件的 prelude
./trajecta layout plan --out plan.json                  # 保留完整计划（.json 全量，.csv 只含 move 列表）
./trajecta layout plan --unknown-site _no-site          # 缺 site 段的文件默认落到 unknown-site/
./trajecta layout plan --sample 0 --format json         # 只看计数
```

输出把每个文件分到 `canonical`（已在目标形状）、`site missing`、`model truncated`、`ambiguous`（路径与 prelude 里的 model 对不上，例如 `<site>/<date>` 这种缺 model 段的历史形态，需要人工决定）与 `unreadable`；`ambiguous` 与 `unreadable` 都不会被规划搬迁。判定依据是每个文件 prelude 里的 `meta.model`——这是区分「site 段」与「含 `/` 的模型名」的唯一可靠信息。为了让计划自洽，目标路径里的模型名会去掉首尾 `/`，模型名含 `.`/`..`/空段时该文件按 `unreadable` 报告。

`layout apply` 执行这份计划。默认是 dry run，只有显式 `--apply` 才会动文件：

```bash
./trajecta layout apply --plan plan.json                       # dry run：报告会搬多少、索引会有多少行变化
./trajecta layout apply --plan plan.json --limit 20 --sample 20 # 先试跑 20 个
./trajecta layout apply --plan plan.json --apply                # 真正执行
./trajecta layout apply --plan plan.json --apply --only-model deepseek-flash
```

每个 move 都是「重命名文件 + 在同一个事务里改写索引中指向它的每一行」，索引行只有三列存 cassette 路径：

| 列 | 说明 |
| --- | --- |
| `logs.path` | trace 索引主键，也是 `logs` 的唯一路径来源 |
| `upstream_exchanges.cassette_path` | 可选副本（可空） |
| `overview_metric_bucket_members.path` | 概览指标的成员主键（按 path 增量维护，没有重建入口） |

`logs.trace_id` 从不改写，因此 `parse_jobs`、`trace_observations`、`trace_findings`、`analysis_jobs`、`session_summaries` 与 trace 的关联保持不变；`request_audits.path`（HTTP 路径）、`semantic_nodes.path`（JSONPath）与 `trace_findings.evidence_path` 不是 cassette 路径，也不参与搬迁。不要用 `migrate --rebuild-index` 代替搬迁：它会清空并重建 `logs`，给每个路径重新生成 `trace_id`，派生分析数据会全部失联。

安全语义：

- **先校验整份计划再动手**：路径必须是相对 root 且不能越出 root，`from == to`、格式非法的计划在任何重命名之前就报错退出。
- **只搬「源存在且目标不存在」的文件**；两端都存在按 `target-exists` 报告，绝不覆盖。
- **索引更新失败就把文件移回**（唯一冲突意味着另一个 trace 已占用目标路径），报告里的 `failed` 条目会写明原因。
- **可重入**：重复执行同一份计划时，已在目标位置的文件报 `already-applied`；如果文件已经搬走但索引还指向旧路径（rename 与提交之间中断），会报 `resumed` 并把索引补齐。
- `--verify-model`（默认开）会重新读一遍 prelude，模型名与计划不一致就拒绝搬迁（防止用过期计划归档）。
- `--db-prefix` 用于数据库里存的前缀与本地路径不同的场景，例如服务跑在容器里、索引里是 `/app/data/traces/...` 而宿主机上是 `/data/gateway/data/traces`：`--root /data/gateway/data/traces --db-prefix /app/data/traces`。
- `--no-db` 只搬文件不动索引，会给出一条告警；不带它时若配置解析不到 Postgres，命令直接拒绝执行（宁可不搬，也不留下索引与文件不一致的状态）。
- 搬迁不改变 mtime 与 size，因此索引的 freshness 字段仍然有效，下一次增量 `Sync()` 会跳过这些文件。

## 派生表的 trace id 修复

合并之前如果已经用新版本服务跑过一轮索引，`logs.trace_id` 会按 cassette 重算，而旧库的派生表（`trace_observations`、`parse_jobs`、`semantic_nodes` 等）仍然带着旧索引记录的 recorder id。插入用的是 `ON CONFLICT DO NOTHING`，所以同 path 的旧 `logs` 行会被跳过 —— 旧行本身没有丢，但派生表里的 id 与 `logs.trace_id` 不再相等，按 id 关联的过滤（例如 Monitor 的 observation status）只会看到服务重算出来的那一份，旧的那一份既不展示也不再参与统计。

`trajecta upgrade db --reconcile-derived-trace-ids` 修复这种状态。它需要旧库来还原 id → path 的映射，归档后的 `*.sqlite3.migrated` 也可以直接传给 `--sqlite`：

- 对每张带 `trace_id` 的派生表，先列出 `logs` 中不存在的孤儿 id；
- 用旧库的 `logs`（旧 id → cassette path）与当前索引（path → 当前 id）把孤儿 id 映射到当前 id；
- **删除**身份（该表唯一键去掉 `trace_id` 后剩下的列）在当前 id 下已经存在的重复行；
- 把剩下的孤儿行 **UPDATE** 到当前 id；旧库查不到对应 trace 的行原样保留并计入 `unresolved`，所以任何一份"唯一副本"都不会被删掉。

默认 dry-run，只统计 `duplicates`、`remapped`、`unresolved`；`--apply` 才写入。每批的删除与更新在同一个事务里，整批冲突时退化为逐个 id 处理，失败的 id 保留原值并继续。

### 陈旧索引行（cassette 已经搬走）

`layout apply` 之后如果索引行没有跟着更新，会出现「`logs.path` 指向的文件已经不在，但同一录制已经索引在新路径下」的行：Monitor 列表里同一录制出现两次，其中一条打不开。带 `--data-root`（默认取配置里的 trace 目录）运行 `upgrade db --reconcile-derived-trace-ids` 时，这类行会被当作**供体**处理：

- 按文件名（basename）在「文件确实存在的行」里找候选；候选恰好一个才算供体，多个（歧义）或一个都没有（vault 里根本没有这个文件）只统计、不修改；
- 先用同一套规则处理供体的派生行：重复的删除，仅剩的重指向存活的 trace id；
- 确认没有任何派生行还引用该 trace id 之后，才删除这条 `logs` 行（需要 `--apply --prune-superseded-index-rows`，删除语句自带 `NOT EXISTS` 保护，永远不会带走派生数据）；
- 找不到文件的元数据行一律保留（它可能是那次请求唯一的记录），只在报告与 warning 中列出。

在容器里运行时数据库记录的路径就是容器路径，直接判断文件是否存在即可；在容器外运行时用 `--data-root <本地根>` 配合 `--recorded-prefix <数据库里记录的前缀>` 做前缀替换。

报告默认只列 10 条示例路径；把 `--max-samples` 调大（例如 `--max-samples 2000`）即可把「找不到 cassette」与「候选不唯一」的路径全部列出，用来逐条核对。

## 旧挂载点的录制导入

旧部署常把宿主机目录挂到**容器内的旧路径**（例如 `/opt/llm_proxy/logs`），`logs` 表记的就是那个容器内前缀。新部署把录制写进当前 trace 目录（默认 `/app/data/traces`），而目录整理（`layout plan`/`layout apply`）与 cassette magic 重写都只扫描**当前数据根**，所以留在旧挂载点的录制永远不会被搬进 vault：

- 现象：`GET /api/traces/<id>` 返回 `404 {"error":"file not found"}`——列表里看得见这条，详情与 raw 打不开；
- 数据仍在：文件在宿主机旧挂载点下，相对路径与 `logs.path` 去掉旧前缀后一致（这正是上一节里「找不到 cassette 的元数据行一律保留」保住的那批行）；
- 也**不是** magic 问题：这批文件多半是 `LLM_PROXY_V2`（定长 2KB 头）。读取端必须继续支持该格式，因此不需要为它们重写 magic。

判定（只读）：

```bash
# 索引里指向旧前缀的行，以及它们引用了哪些相对路径
psql -c "SELECT path FROM logs WHERE path LIKE '/opt/llm_proxy/logs/%' ORDER BY path" > /tmp/legacy.txt
# 逐条确认文件仍在旧根下（把 /opt/llm_proxy/logs/ 换成宿主机旧根）
while IFS= read -r p; do [ -f "<旧根>/${p#/opt/llm_proxy/logs/}" ] || echo "缺失: $p"; done < /tmp/legacy.txt
```

导入分三步：先复制，再改索引，最后验证。

```bash
# 1) 复制进当前 vault，保持相对路径；排除旧索引自带的非 cassette 文件
tar -C <旧根> --exclude=./trace_index.secret -cf - . | tar -C <vault 根> -xf -
```

```sql
-- 2) 改写前缀。logs.path 是主键，改写后不能与既有行冲突；整批放在一个事务里。
--    WHERE 带前缀所以幂等，重跑不会二次改动。
BEGIN;
UPDATE logs SET path = replace(path, '/opt/llm_proxy/logs/', '/app/data/traces/')
 WHERE path LIKE '/opt/llm_proxy/logs/%';
COMMIT;
```

```bash
# 3) 验证：逐条确认新路径存在，再用 API 抽样 detail 与 raw
```

要点：

- 另外两张表也存 cassette 路径：`upstream_exchanges.cassette_path` 与 `overview_metric_bucket_members.path`。含旧前缀时同样要改写；先分别统计，为 0 才只需改 `logs.path`。
- 改写前先落一份 `trace_id,path` 回滚清单，回滚就是反向 `replace`；也要先查新路径是否已被既有行占用。
- 旧根里可能有索引没收录的 `.http`（旧索引没收，或索引重建过）。它们会被复制进 vault，但**不会**自动获得索引行：`layout plan` 只整理布局、不为文件建索引，`migrate --rebuild-index` 会重新生成 `trace_id`，不能用于已有数据。
- 导入后这些行与其它行没有区别（前缀统一、能被 API 打开）；它们不会有 Observation/findings，除非另行重解析。

## 迁移之后

- Postgres 是唯一事实源；`serve` 只连 Postgres，不再打开任何 SQLite 文件。
- 归档文件（`*.migrated`）保留在磁盘上，是否删除由运维决定；它们不再被任何命令读取。
- 原始 `.http` cassette 始终是 replay 与详情页的事实源，数据库只是派生索引；需要重建索引或派生数据时见[存储与部署](./STORAGE_AND_DEPLOYMENT.md)与[语义解析、Observation IR 与审计](./OBSERVATION_AND_AUDIT.md)。
