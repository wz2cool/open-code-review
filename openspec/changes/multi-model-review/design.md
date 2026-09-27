# Design

## Context

单模型评审流程已完整存在且构造高度可注入:`agent.Args` 已支持注入 `LLMClient`、`Session`、`Model`、`Provider`、`MaxTokensBudget` 等参数;`ocr review` 的编排链在 `cmd/opencodereview/review_cmd.go`(runtime 解析 → 会话准入 → `agent.New` → `Run` → `emitRunResult`)。配置文件 `~/.opencodereview/config.json` 已有 `providers`/`custom_providers` 命名端点注册表与 `llm` 显式端点段。`internal/scan` 的 DEDUP_TASK 是一次 LLM pass,与确定性合并无关,不可复用。预算闸门现状:per-run 的 `MaxTokensBudget` 在累计用量加下一组预估超限时停止派发。

本变更是一份重写:同名旧提案(6 需求 / 17 场景 / 22 任务,含 LLM 合并 pass)已整体作废,旧实现从未提交且已被清除;本文件只覆盖缩小后的范围。

## Goals / Non-Goals

**Goals:**

- 配置几个模型 → 并发各扫一遍 → 确定性合并为一份带来源的结果。
- 单模型路径行为与输出字节级不变;多模型为纯增量。
- diff 规模可控(预计约 2200 行,含测试与文档),核心评审流水线(分组、计划、过滤、对抗性 pass、压缩)零改动。

**Non-Goals:**

- 无 CLI flag:`reviewers` 仅经配置文件。"用哪几个模型"是持久偏好而非每次调用的临时决定;flag 以后可增量加入,不破坏任何东西。
- `ocr config` 子命令不提供 `reviewers` 写入:v1 手动编辑 config.json,受众与现在手写 `providers` 条目的是同一批人。
- `ocr llm test` 不校验追加端点连通性:配置装载阶段做结构校验(provider 存在、模型可解析);鉴权/网络类错误留到运行时,由失败降级吸收。
- 不做共识优先排序:"两个模型都报"衡量重叠度而非重要性,severity 才是优先级信号;`found_by` 已让共识肉眼可见。实现上只是一行 comparator,以后想加随时加。
- 不做 LLM 合并 pass,不做向量相似度:v1 接受"措辞差异大的同题发现会重复出现"(宁多不丢)。

## Decisions

### D1: 配置形态与解析

`reviewers` 数组长在现有 provider 注册表上,不引入新概念。条目两种形态:引用式 `{"provider": "deepseek"[, "model": "..."]}`(凭证复用注册表)与内联式(与 `llm` 段同构的端点对象,适合一次性端点)。主模型解析链(`provider`+`model` / `llm` 段 / 环境变量)完全不动。追加条目逐一解析为独立 `LLMClient`;与主模型端点+模型相同的条目幂等丢弃;未知 provider 名在任何 LLM 调用前报错。reviewer 身份 = 模型名,重名追加 provider 后缀;注册顺序 primary 在前,该顺序同时决定 `found_by` 与进度标注的出现次序。备选方案 `--reviewer` flag 被否决(见 Non-Goals)。

### D2: 并发设施

每个 reviewer 一个完整 `agent.Agent`:独立 `LLMClient`、独立 `Session`、独立 `CommentCollector` 与 `CommentWorkerPool`(collector 不得跨 reviewer 共享,否则不同模型的评论会混进同一归属),模板与工具注册表共享。并发编排在 cmd 层新增文件中实现。

共享 token 账本:`internal/llmloop` 新增原子累加的 `TokenCounter`,注入每个 Runner,预算闸门读共享值——配置的预算含义是"这次 review 总共最多花多少",而非每 reviewer 各一份(均分会把每个模型的深度对半砍,与提升召回的初衷相反)。单 reviewer 时账本退化为其自身用量,语义与现状一致。

进度标注:`internal/stdout` 新增 `Prefixed` writer,`internal/agent` 暴露进度 writer 注入点。标注紧随 `[ocr]` 标记之后,使并发交错的每行进度可归属;writer 逐次解析当前 stdout 目标,避免 stdout 被 Quiet/Swap 后仍向已静默的流写入。备选的串行执行可省去这两块(约 500 行、三个核心文件零改动),但墙钟时间 ×N 被用户明确否决。

### D3: 确定性合并(新包 `internal/merge`)

纯函数包,零 LLM 调用,合并永不失败。匹配双条件:位置重叠(多行区间 IoU > 0.6;单行与单行同位置;单行与多行永不匹配)且内容 token 集合相似度 ≥ 0.5。缺一不可:纯位置会吞掉同位置的不同问题(两条单行评论 IoU 恒为 1.0),纯内容会在不同位置误合。组内收敛:正文取 severity 较高者,平手取内容更详尽者;`found_by` 取并集;修复建议与上下文随正文,不跨条拼凑。无法解析行号的发现跳过匹配、原样保留。备选的精确键去重(path+行号+category 完全一致)因 LLM 之间行号常对不齐而几乎去重不了,被否决。

### D4: 输出归因

`model.LlmComment` 增加 `FoundBy []string`(omitempty)。JSON 输出按注册顺序携带;终端文本输出(现有 `renderComment` 渲染路径)在多模型运行时每条发现尾部加 `found by: ...` 行;SARIF 输出在 v1 不携带 `found_by`,报文结构不变(以后可经 property bag 增量加入)。单模型输出不含该字段、不加该行,manifest 也不含 reviewers 字段(omitempty 保证字节级一致)。多模型输出复用 primary 的 session_id,token 用量与警告取聚合值。

### D5: 失败降级与 resume

次要 reviewer 失败 → 记录为警告,其余结果照常合并,退出码 0(部分结果仍是有价值的结果,与预算超限的 best-effort 姿态一致)。主模型失败 → 维持单模型现状语义(报错退出),不产生"无主结果的降级输出"。多模型 + `--resume` → 启动前明确报错:恢复路径的会话准入按单模型设计,v1 不扩展。

## Risks / Trade-offs

- 措辞差异大的同题发现会重复出现 → 确定性相似度的已知盲区,接受:多一条噪音,不丢一条真发现;LLM 合并 pass 是被明确否决的替代(每次运行多一次调用 + 一整个模板面)。
- 同位置、内容巧合相似但确实不同的两个问题可能被误吞 → 相似度阈值(0.5)压低概率,但无 LLM 兜底,接受。
- 并发交错使日志阅读变难 → Prefixed 标注缓解;串行更简单但被否决(墙钟时间)。
- 共享预算下某个"快"模型可能占用量大头 → 聚合语义就是控制总花费,不追求 per-reviewer 公平;manifest 记录各方用量供诊断。
- 两个模型对同一问题给出不同 severity 时取高者,可能整体抬高告警观感 → 与"宁多不丢"一致,接受。

## Migration Plan

纯增量,无迁移:旧配置文件不含 `reviewers`,行为与输出不变;写入新字段即启用。旧提案 artifacts 已删除并由本套替代,旧实现从未提交,无代码迁移对象。回滚 = 从配置中移除 `reviewers` 字段。
