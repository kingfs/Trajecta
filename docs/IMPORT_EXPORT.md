# 导入与导出（多部署数据合并）

本文说明 `trajecta export` / `trajecta import` 的当前行为：把一个部署的录制数据与不可重建的状态导出成一个 bundle，再合并进另一个部署。

适用场景是把家里和公司的两套 Trajecta 合并到一台机器上集中分析，以及把历史数据迁移到新机器后继续增量导入。两条命令都只在 CLI 提供，Monitor 没有对应入口；两者都假定执行时 `serve` 已停止。

## 命令

```bash
# 导出（时间窗必填，或显式 --all）
trajecta export --since 2026-09-01 --until 2026-10-01 \
  --out office-2026-09.tar.gz --source-name office

# 未指定 --password 时自动生成随机密码，并在命令结束时打印一次
# （也可以 --password 指定，或用环境变量 TRAJECTA_EXPORT_PASSWORD）

# 先干跑，看清将发生什么
trajecta import office-2026-09.tar.gz --password '<pw>'

# 确认后写入
trajecta import office-2026-09.tar.gz --password '<pw>' --apply
# 密码也可放环境变量 TRAJECTA_IMPORT_PASSWORD
```

常用开关：

| 开关 | 作用 |
| --- | --- |
| `export --since/--until` | `logs.recorded_at` 的闭区间窗口；`--since` 只给下界，`--until` 只给上界 |
| `export --all` | 不设窗口，导出全部录制，并额外归档未被 `logs` 索引的孤儿 `.http` |
| `export --password` | 指定 bundle 密码；缺省则生成随机密码并只打印一次 |
| `export --allow-plaintext` | 不加密（bundle 内含解密后的渠道凭据，慎用）；与 `--password` 互斥 |
| `export --compression` | `gzip`（默认）或 `none`；300 GB 级数据可关压缩以换 CPU |
| `export --force` | 覆盖已存在的 bundle 文件 |
| `export --log-file` | 把逐条明细日志同时写入文件 |
| `import --apply` | 真正写库、落盘；不带时只做完整检查并输出报告 |
| `import --channel-map <src-id>=<dst-id>` | 强制把源渠道合并到指定目标渠道（可重复） |
| `import --channel-strategy create` | 不按 base_url+key 合并，一律新建渠道 |
| `import --keep-existing-files` | 目标已有同名但内容不同的 cassette 时保留目标文件并记冲突，不做覆盖 |

两个命令都支持全局 `--format json`，报告走与其他命令相同的 JSON envelope。

## 导出内容

导出的是「原始录制 + 不可重建的状态」；能由 cassette 重新解析出来的派生数据不导出。

| 类别 | 表 | 处理 |
| --- | --- | --- |
| 渠道配置 | `channel_configs`、`channel_models`、`model_aliases`、`model_catalog`、`channel_probe_runs` | 导出；API key 与 `headers_json` 中的密钥在导出时解密 |
| 应用设置（白名单） | `app_settings` 的 `routing.settings`、`rates.alerts`、`responses.function_executors.config_snapshot` | 导出；导入时只在目标缺失该键时写入 |
| 原始录制索引 | `logs` | 导出，保留原 `trace_id`、`mod_time_ns`、`file_size` |
| 上游 exchange 索引 | `upstream_exchanges` | 导出；`cassette_path` 在导入时重写 |
| 遮蔽审计 | `credential_mask_audits`、`credential_mask_event_states` | 导出；真值/假值在导出时解密、导入时用目标密钥重加密 |
| LLM 分析结果 | `analysis_runs` | 导出；数字自增主键在导入时重新分配 |
| 评测体系 | `datasets`、`dataset_examples`、`eval_runs`、`scores`、`experiment_runs` | 导出 |
| Responses 运行时状态 | `responses`、`response_items` | 导出 |
| 审计轨迹 | `request_audits`、`execution_events`、`tool_call_audits` | 导出 |
| 运维事件 | `system_events` | 导出；按 `fingerprint` 合并，保留目标的已读/已解决状态 |
| 原始字节 | `<output_dir>/**/*.http` | 按被导出 `logs` 行引用的路径复制，保留文件 mtime |

不导出：

- `trace_observations`、`trace_findings`、`parse_jobs`、`analysis_jobs`、`parser_versions`、`session_summaries`、`resources`、`trace_resources`：都能由 cassette 重建，目标侧部署后由 `Sync` / `analyze refresh` 重新生成；
- `semantic_nodes`：已由迁移 `20261111000000_drop_semantic_nodes` 废弃，运行时代码不再读写；
- `users`、`api_tokens`：每套部署独立鉴权，不做合并；
- `upstream_targets`、`upstream_models`：legacy 投影，运行时不写；
- `app_settings` 中的 `telemetry.install_id`、`network.proxy` 与两条 `system_events.credential_mask_*_repaired_v1` 修复标记：属于单机身份或环境状态，导入无意义；
- `{{output_dir}}/secret_mask_mappings.db`（bbolt 遮蔽映射）：与本机主密钥绑定，不跨机迁移。

## Bundle 格式

一个流式写入的 tar（默认再套 gzip，可选再套密码信封），布局固定：

```text
manifest.json                       # format=trajecta-export/v1、时间窗、各表行数、cassette 统计
db/channel_configs.jsonl            # 按导出顺序，每行一条记录
db/channel_models.jsonl
...                                 # 共 22 张表
db/logs.jsonl
db/credential_mask_event_states.jsonl
cassettes/<相对路径>.http           # 位于最后，流式复制
```

- `manifest.json` 是完整性契约：导入时逐表核对行数，不符即判定 bundle 被截断并整体中止。
- 表记录一律是 JSON Lines，时间统一为 RFC3339Nano；`NULL` 为 JSON `null`。
- 加密信封：`TRAJECTA-EXPORT-1` 魔数 + 一行 JSON 头（scrypt 参数、salt、nonce seed）+ 64 KiB 分块 AES-256-GCM，末块为带认证标签的哨兵，因此截断和篡改都会被检测出来。密码由 scrypt（N=32768, r=8, p=1）派生。
- `logs.path` 与 `upstream_exchanges.cassette_path` 在 bundle 里保留源侧的完整路径，导入时按「源 `output_dir` 前缀 + 相对路径」重写为目标 `output_dir` 下的同构路径。两套部署都挂载 `/app/data` 时结果为原样。

## 导入语义

导入是**可重复执行的增量合并**，不是恢复快照：

- 行已存在且未变化 → 跳过；变化 → 更新；永远不会删除目标侧已有数据。
- 同一个 bundle 导入第二次不产生任何写入；较新的 bundle 只应用差异。
- 整个数据库写入在**一个 `ConfigurationTransaction`** 内完成，失败整体回滚。
- cassette 文件在事务提交前写入；若事务回滚，本次新建的文件会被删除，避免库里没有索引的孤儿文件。
- 提交后重新计算 `session_summaries`，因此导入的会话立刻能在 Monitor 的会话/历史视图里查到；重算失败只记 warning 并提示 `trajecta db summary rebuild sessions`。

### 渠道合并：base_url + API key

渠道身份是「归一化后的 `base_url` + 解密后的 API key」，与渠道名、优先级无关：

1. `--channel-map` 显式映射优先；
2. 源渠道 id 在目标已存在且 `base_url`+key 都相同 → 复用该 id；
3. 否则按 `base_url`+key 查找目标渠道，命中 → 复用目标渠道 id，并把源侧独有的模型并进去（已存在的 `(channel, model)` 保留目标的启用状态与 profile，只补 `NULL` 的三态能力列与缺失字段）；
4. 都未命中 → 用源 id 新建渠道（id 冲突时改用新 UUID）。

`base_url` 归一化会去掉首尾空白、路径尾斜杠、大小写差异与 fragment，保留 query。因此同一网关的两种写法仍能识别为同一渠道；同一 `base_url` 换了 key 则是不同渠道。

### 冲突与跳过

| 情况 | 行为 |
| --- | --- |
| `logs.path` 已存在且 `trace_id` 相同 | 跳过（已导入） |
| `logs.path` 已存在但 `trace_id` 不同 | 跳过该 trace 并把 trace_id 关联的派生/审计行一起跳过，报告里给 warning |
| `upstream_exchanges.id` / `request_audits.id` 等已存在 | 跳过 |
| `system_events.fingerprint` 已存在 | 合并时间戳；仅当源事件更新时才累加 `occurrence_count`，保留目标已读/已解决状态 |
| `model_catalog.model` 已存在 | 取更早的首次出现、更晚的最后出现/最后使用 |
| `datasets` / `eval_runs` / `responses` / `analysis_runs` 已存在 | 跳过（`analysis_runs` 以 `trace_id+kind+analyzer+analyzer_version+created_at` 判重） |
| cassette 文件已存在且大小与 mtime 一致 | 跳过 |
| cassette 文件已存在但内容不同 | 覆盖并计为 updated；`--keep-existing-files` 改为保留目标并记冲突 |

`--dry-run`（默认）会完整读一遍 bundle，报告每条渠道的匹配结果、每张表的写入/跳过数量和 cassette 的新增/更新/跳过数量，但不写库、不建目录。

## 密钥处理

`channel_configs.api_key_ciphertext`、`headers_json` 中的密钥成员、`credential_mask_audits.original_value`/`stand_in_value` 都用本机 `{{output_dir}}/trace_index.secret` 加密。导出时用源部署的密钥解密，明文只存在于 bundle 内部；bundle 默认整体加密，所以密码是这批数据的唯一保护。导入时用目标部署的密钥重新加密，目标永远不需要源的密钥文件。

`--allow-plaintext` 可以产出不加密的 bundle，此时渠道凭据是明文，仅应在受控介质上使用。

## 执行顺序与注意事项

1. 停止两端的 `serve`（导出端与导入端都要停）。两端录制、索引与 `secret_mask_mappings.db` 都按单进程假设设计。
2. 两端 Trajecta 版本与 Postgres 迁移版本应一致（bundle 记录 `migrations_version`，但当前只用于诊断，不做强制校验）。
3. 导出时间窗必填，除非显式 `--all`；时间窗按 `logs.recorded_at`（UTC 存储、按 `monitor.timezone` 解释输入）。
4. 导入完成后重启 `serve`：渠道配置在启动时投影成路由快照。
5. 想知道「某个会话是不是早就导过了」：同一 bundle 重复导入是零写入；报告里的 `skipped_unchanged` 即已存在条数。
6. 目标侧若从未跑过 `Sync`，首次启动会为导入的 cassette 入队 `parse_jobs`，这是预期行为（派生数据重建），Cassette 文件本身不会被重写。

## 已知边界

- 不支持持续/双向同步：命令是「冻结一段数据后合并」，不处理两端同时写入的并发合并。
- 不迁移 `users` / `api_tokens`：集中分析实例自带鉴权。
- 不迁移 `secret_mask_mappings.db`：它绑定本机密钥，且 bbolt 只允许单进程打开。
- `semantic_nodes` 的历史数据不迁移：该表已废弃，迁移会直接 DROP。
- bundle 中 `analysis_runs`、`channel_models`、`dataset_examples` 的数字自增主键会被重新分配；这些表没有外部按该 id 的引用，重分配是安全的。
