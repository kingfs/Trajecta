# 运维 Runbook：备份、恢复、磁盘满与密钥轮换

本文只描述当前代码事实。命令细节见 [存储与部署](./STORAGE_AND_DEPLOYMENT.md)、[Postgres 运维](./POSTGRES_OPERATIONS.md)、[导入导出](./IMPORT_EXPORT.md) 与 [开发文档](./DEVELOPMENT.md)。

## 1. 要备份什么

按恢复优先级排列：

1. **应用数据库**（生产为 Postgres）：`logs`、`channel_configs`、`channel_models`、`model_aliases`、`app_settings`、`users`、`api_tokens`、Responses/audit 表等。用 `pg_dump` 或 PITR/快照；恢复步骤见 [Postgres 运维](./POSTGRES_OPERATIONS.md)。
2. **`{{output_dir}}/trace_index.secret`**：渠道凭据与遮蔽映射的本地主密钥。丢失后已存渠道 API key 无法解密。文件权限应为 `0600`。
3. **cassette 目录**（`trace.output_dir`）：事实源。按文件系统快照或停机后 `rsync`/归档；运行中直接 `cp` 可能拿到写入中的文件。
4. **`{{output_dir}}/secret_mask_mappings.db`**：遮蔽映射与抑制名单的 bbolt 文件。必须在 `serve` 停止后复制，或用文件系统快照；运行中直接 `cp` 可能拿到不一致的页。
5. **SQLite 部署**：`{{output_dir}}/trajecta.sqlite3` 与上述文件一起备份（仅本地/dev/test 或旧库导入）。

> cassette 是事实源；只要 cassette 与 `trace_index.secret` 在，DB 索引与派生数据可以重建。反过来不成立。

## 2. 恢复顺序

1. 恢复 Postgres（或 SQLite 文件），执行 `trajecta db migrate status --check-db` 确认 schema 完整。
2. 恢复 `trace_index.secret`（`0600`）。
3. 恢复 cassette 目录与 `secret_mask_mappings.db`（停机状态复制）。
4. 启动 `serve`；启动时会跑一次 `Store.Sync()` 补索引，并为新索引的 cassette 入队 parse job。
5. 抽查 Monitor 的流量列表、trace 详情与遮蔽映射页；必要时 `trajecta analyze backfill-exchanges` / `db summary rebuild sessions`。

## 3. 磁盘满应急

1. 停止 `serve`，避免继续写入。
2. 清理可重建数据：先 `trajecta db cleanup requests`（默认 dry-run，确认后加 `--apply`），它删除 `logs` 及派生表；cassette 是事实源，不要用 `migrate --rebuild-index` 去“修复路径”。
3. 检查 `trace.output_dir` 与数据库所在卷；必要时迁移到更大卷后重启。
4. 恢复启动；观察日志中的 `Cassette files are incomplete and were skipped` 与 `Trace index sync` 计数。
5. 长期方案：为 cassette 目录设置文件系统配额/告警；容量策略必须是显式运维配置，应用不会静默截断 cassette。

## 4. 密钥轮换

```bash
trajecta -c config/config.yaml db secret status
trajecta -c config/config.yaml db secret export --out trace_index.secret.backup
trajecta -c config/config.yaml db secret rotate --yes
```

- `rotate --yes` 缺少 `--yes` 会拒绝执行；成功后备份旧 key、写入新 key，并重加密渠道 API key 与敏感 header。
- **范围限制**：轮换只重加密 `channel_configs` 的 API key/secret header；`secret_mask_mappings.db` 与 `credential_mask_audits` 仍用旧 key 加密，轮换后可能无法解密。轮换前先备份这两处；需要继续使用旧映射时，在轮换前导出/保留旧 key。
- 轮换后重启 `serve`，确认渠道探测与代理请求正常；Monitor 的遮蔽映射页如出现“不可解密”，按提示删除对应行或重建映射。

## 5. 演练清单

- [ ] 备份后能在隔离环境恢复 Postgres + secret + cassette，并打开一条历史 trace。
- [ ] `trace_index.secret` 丢失时，明确渠道凭据不可恢复，且已记录轮换/重配流程。
- [ ] 磁盘水位告警已配置（文件系统或监控），并知道先停 `serve`。
- [ ] 密钥轮换在预发环境演练过，且已备份 `secret_mask_mappings.db` 与审计表。