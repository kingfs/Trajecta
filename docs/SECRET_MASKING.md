# 敏感凭据遮蔽（Secret Masking）

本文描述 `pkg/secretmask` 的当前能力，以及代理在请求与响应路径上如何使用它。

## 它解决什么问题

用户（或本地 agent）在对话里粘贴真实 API key 时，这个值会随请求离开本机、进入上游厂商与模型上下文。开启本能力后，请求体在离开本机前会被扫描，命中的凭据被替换为**保形假值**；上游与模型只看到假值，本机则保留真值。模型如果在回复或工具调用里回显了假值，响应在写给客户端前会被换回真值。

它保护的是「值本身不被泄露给上游」。它不承诺的事情见最后的「已知边界」。

## 开关

默认关闭。开启方式二选一：

```yaml
secret_masking:
  enabled: true
```

或环境变量 `TRAJECTA_SECRET_MASKING_ENABLED=true`。

之所以默认关闭：它会改变模型实际看到的内容，属于语义变更，不是日志细节，因此应由运维显式决定，而不是升级后被动生效。

## 假值形态

假值必须同时满足三条性质，缺一条都会出问题。

**一、与真值等长。** 这是承重约束。代理会把上游返回的 `Content-Length` 原样透传给客户端，而请求体长度由 net/http 从「实际交给它的字节切片」推导（`internal/proxy/handler.go` 的 `sendUpstreamRequest` 用 `len(bodyBytes)` 设置 `outreq.ContentLength`）。长度一旦变化，两侧都会静默错位：请求被截断或报传输错误，客户端的 `Content-Length` 与实际响应不符。

**二、保留前缀、标点与分段字母表。** 假值保留真值的前缀（`sk-proj-`、`AIza`、`gsk_` 等）、所有非字母数字字符、以及厂商水印（OpenAI 系四个规则的 `T3BlbkFJ`、Bedrock 短期的 host 串）。字母数字部分按「连续字母数字串」逐段替换：整段都是小写十六进制就仍从小写十六进制取字符，因此 Databricks、智谱、Langfuse、OpenRouter 这类带 hex 段的 key 不会退化成自由文本。这样，只按形状校验的工具仍然接受假值。

**三、确定性。** 假值是 `(seed, 规则名, 真值)` 的纯函数：同一个 key 在每一轮对话里渲染成同一个假值。这既保证多轮对话不自相矛盾，也保证上游的 prompt cache 前缀不发生抖动。

渲染在 `pkg/secretmask/vault.go`：以 HMAC-SHA256 生成密钥流，逐字节替换为同类字符。

## Vault

`Vault` 是唯一能还原假值的反向索引，只存在于内存中：

- 种子默认来自 `crypto/rand`，每个进程不同。因此从线路上抓到的假值离开这个进程就无从还原，重启后旧假值全部失效。只有在测试或需要复现会话时才显式传种子。
- 上限 4096 条，超出按插入顺序淘汰最旧的，避免长驻进程无界增长。
- 遮蔽是幂等的：已经在 vault 里的假值会被跳过，二次遮蔽等价于一次。

## 识别与打分

引擎分四层，越往后越贵，只有前一层放行的候选才进入下一层。

**L1 词法 DFA。** `pkg/secretmask/rules.rl` 是模式的唯一事实源，由 Ragel 编译成 Go 状态机（`rules_ragel.go`，已提交）。扫描器用 `|* ... *|`，因此**最长匹配优先，等长时先声明者优先**，规则顺序具有语义。运行时不做回溯。

长度上下界不在状态机里表达，而在 `scan.go` 的 `collector.emit` 里用原生代码判断。把 `{n,m}` 写进状态机的代价是实测 3364 个状态 / 159703 行生成代码 / 25.7 s 生成 / 单次扫描 1958560 ns，而「`+` 加原生长度检查」是 287 个状态 / 15472 行 / 1.0 s / 836946 ns：状态多 11.7 倍、生成代码多 10 倍、慢 2.34 倍。

**L2 结构校验。** `validators.go` 实现两个离线校验，规则通过 `Validator` 字段引用：

- `jwt`：三段 base64url 全部解码成功，头部是命名了算法的 JSON 对象，载荷是 JSON 对象，且签名解码长度与该算法一致。
- `anthropic_key`：结尾必须是已发布 Anthropic key 家族共有的 `AA`。

带校验器的规则只上报通过校验的匹配，且通过校验本身就足以认定。

**L3 上下文。** 在匹配位置前后各 96 字节窗口内搜索凭据词汇（`context.go` 的全局词表，加上规则自己的 `Keywords`）。

**L4 置信度门限。** 打分权重见 `finding.go`：Strong 70 分、Anchored 35 分、Generic 10 分；规则定长且长度吻合加 10 分；上下文命中加 40 分；校验器通过则分数不低于 70 分；总分封顶 100。分档为 High ≥ 70、Medium ≥ 45、Low < 45。**只有 Medium 及以上允许被自动改写**，Low 仍然上报，让审计能看到「看见了但故意没动」。

`Strength` 的划分依据是「词法匹配本身证明了多少」：Generic 的锚点是普通代码里也会出现的短前缀（`sk-`、`sk_`、`ek_`、`ak-`、`as-`、`st-`、`pa-`、`nk-`、`pk-`、`pl_`、`tl_`、`lf_`、`key_`、`AQ.`、`1//`、`app-`/`dataset-` 等），Anchored 是厂商特有但正文自由，Strong 是形状内含厂商水印或「前缀 + 精确长度」已经足够决定。

## 覆盖范围

规则集共 67 条：Strong 28、Anchored 23、Generic 16。范围刻意只覆盖结构形状（JWT、PEM 头）与 LLM 厂商前缀，即 [密钥与 Token 格式识别参考](./SECRET_FORMAT_REFERENCE.md) 第 3、4 节所述内容，外加 `sk-`/`sk_` 两个通用池。

**未覆盖**：该参考第 5、6、7 节的云、CI、IM 与支付类形状。`internal/analyzer` 里另有 7 条正则（AWS `AKIA`、GitHub `ghp_`、DSN、Cookie、Bearer 等），本引擎目前在这些形状上**弱于**该 analyzer，因此不要把本引擎当作它的替代品。

## 在代理中的接入点

请求侧是「记录原始、发送假值」的拆分，两侧都不复用同一个切片：

- 透传路径：`internal/proxy/handler.go`。`upstreamBodyBytes`（别名改写后）交给 cassette，`sendBodyBytes`（遮蔽后）交给上游。交给路由器、指纹与失败 cassette 的 `bodyBytes` **不被改动**。
- 本地 Responses 路径：`internal/proxy/responses_server.go`。`buildResponsesServerChatRequest` 收到遮蔽后的体，`PrepareLogFileWithOptionsAndBody` 的第三个参数仍是原始体。

响应侧是还原，且还原器必须装在 `UsageSniffer` **之内**而不是之外：`UsageSniffer.Read` 会把读到的 `p[:n]` 直接写入 cassette，如果还原发生在 sniffer 外层，cassette 里留下的就是假值。

体是流式的，因此假值可能跨越两次 `Read`（SSE 事件边界、`io.Copy` 缓冲区边界都是任意的）。`restore.go` 的 `Restorer` 每次读取都扣留「最长假值 − 1」字节，等下一次读取到达才释放，从而保证凡是它已经吐出的首字节所在的位置，假值必然完整。vault 为空时扣留量为 0，未发生遮蔽的流不引入任何延迟。由于假值与真值等长，还原是对调用方缓冲区的原地覆盖，不会移动后续字节。

cassette 记录的是**原始值**，这是有意选择：replay 按请求字节匹配（`pkg/replay`），人读 trace 也需要看到真实请求。代价是 cassette 里存在真实凭据，与 `debug.mask_key` 只处理请求头不同，这一点在 `SECURITY.md` 中有运维提示。

## 审计事件

命中与还原都会写入 cassette prelude 事件，命名遵循 `<domain>.<event>` 约定：

- `secret.masked`：每个被替换的值一条，属性为 `rule`、`vendor`、`kind`、`start`、`end`、`length`、`score`、`confidence`、`reasons`。
- `secret.restored`：一次响应一条，属性为 `count`。

两者都**不携带凭据字节**，只描述改写过程，避免审计记录变成第二份密文。

## 已知边界

- **PEM 私钥正文不覆盖。** 规则只匹配 `-----BEGIN ... PRIVATE KEY-----` 头行，不含密钥材料；真正的 base64 正文没有任何规则覆盖。改写头行既不能保护私钥，又会破坏文件语法，因此该规则**只上报、不改写**。
- **需要真值的操作会失败。** HMAC 签名、AWS SigV4、JWT 签名等必须在真值上运算的操作，拿着假值无法完成。
- **派生与编码形式绕过。** 引擎不做熵估计、不做校验和、不做 base64 解码（除上述结构校验）、不支持 look-behind。被编码、截断、改大小写或重新格式化过的凭据识别不到。
- **不阻止「借刀杀人」。** 它保护值不泄露给上游，但不阻止模型通过工具调用让下游系统去使用该凭据。

## 度量

- 词法扫描约 72 KB 载荷：约 1.16 ms，约 63 MB/s。
- 遮蔽 109 KB 载荷：热态约 211 MB/s，冷态约 198 MB/s（冷态包含渲染假值）。
- 还原：约 242 MB/s；流式还原约 205 MB/s。
- 精度：对仓库内 467 个源文件（13.3 MB）扫描得到 7 处命中，其中 5 处是测试夹具，2 处是 Go 标识符误命中 `ek_` 前缀；该规则最小长度随后从 12 提到 30 字节，这 2 处误命中已消失。
