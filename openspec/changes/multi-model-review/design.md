# Design

## Context

动机与范围见 [proposal.md](proposal.md) — Why。实现要贴合的既有结构与约束:

- 整条 review 管线绑定单一模型:`loadLLMRuntime`(`cmd/opencodereview/shared.go`)解析**一个** endpoint 构造一个 `LLMClient`,`agent.Agent` 与 `llmloop.Runner` 都以单 client + 单 model 运行;多模型需要在其上做编排层,而不是改管线内部。
- 凭据体系已支持共存:`Config.Providers` / `CustomProviders` 是 map,可同时存多家的凭据,只是"激活"只有一个;`llm.ResolveEndpointWithOptions` 已支持按 `--provider` / `--model` 定向解析单个 endpoint。
- 两个现成蓝本:scan 模式的 `DEDUP_TASK`(`internal/scan/agent.go` 的 `maybeRunDedup`,LLM 判重 + `merged_content` 整合 + 全有或全无校验 + 失败保底)与 GitHub Action 脚本的 `sameCommentSpan`(同路径 + 行 span IoU > 0.6,单行/多行不互判)。
- `CommentCollector` 的 `Snapshot` / `Since` / `ReplaceSince` 已提供批次隔离写入能力;对抗性 pass 已确立"第二遍 best-effort、失败只警告、不改退出码"的先例。

## Goals / Non-Goals

**Goals:**

- 召回最大化:多个模型各自独立跑完整管线,并集输出,`found_by` 归因辅助分诊。
- 多模型是内部事务:下游(JSON / SARIF / 文本输出)看到的始终是**一份合并后的列表**,不需要知道多模型的存在。
- 失败降级:任何副环节(额外 reviewer、合并 pass)失败都不降低主结果可用性、不改变退出码。

**Non-Goals:**

- 不做交叉验证 / 挑战式合并——任何"挑战"环节都可能错杀真发现,与召回最大化目标相反。
- v1 不覆盖 scan 模式(它是另一套 agent,待 review 跑通后复制)。
- v1 不支持多模型与 `--resume` 组合(见 Decisions D8)。
- 不做 TUI 管理额外 reviewer,v1 也不为 `additional_reviewers` 增加 `ocr config set` 支持(配置写入路径见 D4);不改 GitHub Action 发帖展示与 `action.yml` 输入(用户主用本地 CLI,下游展示延后)。
- 不做分组级流水线交错(两个 agent 内部各自的并发语义保持现状)。

## Decisions

### D1: 并发执行,聚合预算

每个 reviewer 一个独立的 `agent.Agent`(各自的 Runner、Session、CommentCollector),goroutine 并发执行;所有 Runner 通过共享的原子聚合计数器参与同一个 `--max-tokens-budget` 总账,任何请求发起前先查账,超限后所有 reviewer 都不再发起新请求,在途请求正常完成。

- 替代方案:串行(主先副后)。预算语义更简单、日志自然分节,但墙钟时间 ×2。用户已明确选择并发,接受其代价:D7 的输出前缀与本条的计数器协调。
- 预算耗尽时的语义:已完成的部分全部进入合并,记一条 warning,退出码不变(与现有 flag 语义"partial results are published, review exits 0"一致)。交付"部分贡献"优于丢弃。

### D2: 合并分两层,LLM 层失败保底

1. **确定性预合并**(纯 Go,零成本):同路径 + 行 span IoU > 0.6 **且内容信号一致**(同 category 且归一化文本相似度达到阈值,阈值在实现期定稿并固化进单测)才归组;单行与多行评论不互判重复(GA 脚本先例);无行号的评论不参与确定性匹配(GA `lineSpan` 对无行号返回 null),但仍进入 LLM 整合 pass 的输入。顺带缩小 LLM 层输入。内容护栏是刻意的:GA 对历史评论做纯位置判重是安全的(那是同一模型自己的重复),跨模型纯位置判重会静默吞掉"同位置的不同问题"——两个模型常在同一行范围各锚一个不同发现,合并发生在 LLM 层之前,吞掉的发现无法挽回,与召回优先的目标直接冲突;位置匹配但内容信号不足的对保留给 LLM 层语义判定。
2. **LLM 整合 pass**:全部评论带稳定 `c-N` id 与模型归属打包,输出 `groups`(members + 可选 `merged_content`);**全覆盖校验**(每个 id 恰好出现一次,有未知 id / 重复分配 / 遗漏 → 整体作废),失败保留预合并结果。

代表元规则沿用 scan `DEDUP_TASK`:组内第一条成员为代表,`merged_content` 只覆盖 `content` 字段,`suggestion_code` / 行号 / `severity` / `category` 保持代表元的——v1 不融合代码建议,半融合代码比不融合更危险。模型顺序上主 reviewer 的评论排前,因此默认幸存的是主模型的措辞。

- 备选的纯 LLM 单层方案被否:无 LLM 也能消掉明显重复,且 LLM 失败时仍有产出;备选的纯确定性方案被否:不同锚点、不同措辞的同一问题(跨模型最常见的重复形态)span 匹配不到。

### D3: 合并 pass 用主 reviewer 的模型执行

输入是几十条评论的小 payload,成本可忽略;主模型在编排层已经解析完毕,无需额外 endpoint。不单独为此引入"用哪个模型合并"的配置项。

### D4: 配置用 `{provider, model}` 对象,不用 `provider/model` 字符串

模型名本身常含斜杠(如 `deepseek-ai/DeepSeek-V3`),字符串按斜杠切分有歧义。配置文件用结构化对象消除歧义;`--reviewer` flag 按约定**第一个斜杠**切分,provider 名不允许含斜杠(文档明示)。

`--reviewer` 的值 MUST 通过与配置相同的 provider 存在性校验,校验失败即报错——显式指定的 reviewer 不能静默降级为单模型运行;凭据缺失、连接失败这类运行期问题仍按失败降级语义处理(spec R5:warning、主结果照常、退出码不变)。flag 与配置条目或主模型重复的 (provider, model) 按幂等去重:脚本把已在配置里的 reviewer 再显式传一遍是正常组合,不应报错或重复运行;配置数组内部的重复仍属笔误,维持报错。

v1 的配置写入路径:手动编辑配置文件(或 `--reviewer` 单次追加)。TUI 与 `ocr config set` 对数组字段的写入支持延后(见 Non-Goals),文档按手动编辑路径说明。

### D5: `found_by` 归因,下游单列表无感知

`LlmComment` 增加可选 `found_by`(reviewer 标识数组,组内成员并集;标识默认取模型名,多个 reviewer 同名时附 provider 区分;内部按 reviewer 注册顺序排列——主模型在前——保证并发下输出确定);JSON 输出该字段,SARIF / 文本照常(文本可选地以标注呈现)。合并发生在 collector 之后、`emitRunResult` 之前,下游只见到一份列表。JetBrains 扩展 `ignoreUnknownKeys = true` 天然容忍新字段。

会话侧:每个 reviewer 独立 session 文件(现有 session 格式假定单模型,不改其格式);运行 manifest 增加 reviewers 列表(名字 + 解析来源,不含秘密)——该字段仅在多模型运行时写入(omitempty),单模型 manifest 必须保持逐字节不变:manifest 进入 JSON 输出,无条件新增字段会破坏"默认行为不变"的承诺。

### D6: 合并模板 `REVIEW_MERGE_TASK` + 门槛 + `--no-merge`

review 模板新增可选任务 `REVIEW_MERGE_TASK`,形态对标 scan `DEDUP_TASK`(同一全覆盖校验、`merged_content` 语义、失败保底)。门槛:合并输入评论数小于阈值(对标 `DedupMinComments`,默认 2)时跳过 LLM pass,仅保留预合并结果。`--no-merge` 跳过整个合并阶段,直接拼接各方列表(每条保留自己的 `found_by`),与 scan 的 `--no-dedup` 对称。

### D7: 进度输出按模型加前缀

并发下两个 agent 的 `[ocr] ...` 进度行会交错,各自加 reviewer 前缀(如 `[ocr] [glm] ...`;前缀取模型名,重名时附 provider,与 `found_by` 的标识规则一致)。机器可读模式(JSON / SARIF)下 stdout 抑制的既有行为不变——评论在合并后才输出。

### D8: v1 显式拒绝 `--resume` 与多模型组合

会话与检查点格式假定单模型;按模型分桶的完整 resume 设计工作量大,且"副模型重跑 + 残结果合并"有去重歧义。多模型运行时带 `--resume` 直接报错,错误信息提示两个解除方向(去掉额外 reviewer,或去掉 resume)。后续版本再设计完整方案。

### D9: 输出确定性

合并结果按 path + `start_line` 稳定重排后进入输出,消除并发完成顺序对输出顺序的影响。

### D10: 独立与共享的边界,以及输出的单一骨架

现有单模型装配把若干句柄绑在一起(`review_cmd.go` 的 `buildToolRegistry(rt.Collector, ...)` 把 collector 内嵌进 tool registry;`emitRunResult` 只接受一个 ResultProvider,从中读取 session_id、manifest、token 统计、warnings)。多模型下按下述边界拆分:

- **每 reviewer 独立**:Agent、Runner、Session、CommentCollector、**tool registry**(collector 内嵌其中,必须各自构造)、CommentWorkerPool。缺少独立 registry 会让两个 agent 的评论写进同一个 collector,合并阶段失去归因基础。
- **per-run 共享**:RetryCollector、RawHolder、git runner 的进程限流器、MCP client。共享句柄的并发安全性(尤其 MCP 会话的并发请求)MUST 在实现时验证;验证不通过则改为每 reviewer 复制。
- **输出骨架**:仍以主 reviewer 的 ResultProvider 为骨架——`session_id`、manifest 取主 reviewer(manifest 的 reviewers 字段列出全部),token 统计与 warnings 聚合全部 reviewer。这样下游(JSON / SARIF / 文本)与 `emitRunResult` 的契约不变,合并只体现为评论列表与聚合数值。

- 替代方案:引入一个显式的多模型 ResultProvider 包装层。被否:改动面扩散到全部输出路径,而收益只是"更对称"。

## Risks / Trade-offs

- [并集带来误报增多(召回目标的直接代价)] → 每个 reviewer 管线内已有 REVIEW_FILTER;`found_by` 共识标注辅助人工分诊(两个模型都发现的优先看)。
- [LLM 整合 pass 把不同发现错判为同一(丢失发现)] → 阈值保守(IoU 0.6、单行/多行不混配);错判风险与召回优先的目标权衡后接受;全覆盖校验兜住"静默丢 id"这一最坏形态。
- [并发写 stdout 交错不可读] → D7 前缀;机器可读模式不受影响。
- [预算耗尽截断副 reviewer] → 交付已完成部分 + warning,合并照常;不视为失败。
- [session 文件数量随 reviewer 数翻倍] → v1 接受;manifest 汇总可查。
- [成本 ≈ ×N] → 严格 opt-in,默认单模型;文档写明代价。
- [两模型对同一问题给出不同行号 / category,合并后归因失真] → 行号差异由 IoU 吸收;category 失真(代表元的)不影响评论可用性,`found_by` 不受影响。
- [超大 PR 下合并输入超出 prompt 预算] → 整合 pass 失败即保底:保留确定性预合并结果,评论不丢失;按路径分块的增量合并留待演进。
- [两个 reviewer 共享 MCP client 会话的并发调用] → 实现时验证 SDK 的并发请求安全性(D10);不通过则按 reviewer 复制 MCP client,代价是子进程数翻倍。

## Migration Plan

纯增量,无迁移:未配置 `additional_reviewers` 时行为与现状逐字节一致。回滚 = 删掉配置项(或 git revert);`found_by` 为可选字段,老版本下游自然忽略。

## Open Questions

(无——执行顺序、resume 语义、输出优先级均已在探索中与用户定稿;其余小项按推荐写入上文各 Decision。)
