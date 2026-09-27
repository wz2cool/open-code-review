# Proposal

## Why

用户希望用多个模型交叉扫描同一份 diff 以提升发现召回,但此前立项的全量方案(6 需求 / 17 场景 / 22 任务,含 LLM 合并 pass)相对"配置几个模型一起扫、结果合并"的真实需求明显过重,已被整体推翻。本提案以最小范围重新立项:完整复用现有单模型 review 流程,只新增多 reviewer 配置、并发执行、确定性合并与来源归因。

## What Changes

- `~/.opencodereview/config.json` 新增可选 `reviewers` 数组:每个条目以引用式(`{"provider": "name"}`,复用 `providers` 注册表凭证)或内联式(与 `llm` 段同构的端点对象)声明一个追加 reviewer;数组缺省或为空时,单模型行为与现状完全一致。
- 多 reviewer 时并发执行:每个 reviewer 完整复用现有 review 流程(分组、计划、评审、过滤、对抗性 pass),互不等待。
- 各 reviewer 的发现经确定性规则合并(位置 IoU 与内容相似度双条件),不做任何 LLM 合并调用;同位置同义发现收敛为一条,来源取并集。
- 输出归因:JSON 输出中每条 finding 携带 `found_by`;终端文本输出在多模型运行时为每条发现追加来源标注行;单模型输出保持字节级不变。
- 共享 token 预算:所有 reviewer 共用一个聚合预算账本,超限后全体停止派发新请求、在途请求跑完、已有结果照常合并,警告 + exit 0。
- 并发运行时的进度行按 reviewer 标注,保持可归属。
- 明确不做:无 CLI flag(`reviewers` 仅经配置文件)、`ocr config` 子命令不提供写入、`ocr llm test` 不校验追加端点连通性(理由与边界见 design)。

## Capabilities

### New Capabilities

- `multi-model-review`: 多 reviewer 的配置契约、并发执行与共享预算、确定性结果合并、输出归因与失败降级。

### Modified Capabilities

无。单模型路径的需求不变:现有能力(含 review-prompting 的对抗性 pass)描述的是单次 review 运行的行为,多模型只是把同一流程组合执行多次,不改变任何既有需求。

## Impact

- 代码:
  - `cmd/opencodereview`:`config_cmd.go` 增加 `reviewers` 字段;`review_cmd.go` 接入多 reviewer 编排;新增 `reviewers.go`(条目解析与校验)与 `multi_review.go`(并发编排)。
  - `internal/llmloop`:共享 token 账本与预算闸门接入。
  - `internal/stdout` 与 `internal/agent`:按 reviewer 标注的 progress writer 及其注入点。
  - `internal/session`:manifest 记录 `reviewers` 与各方用量(多模型时才写入)。
  - `internal/model`:`LlmComment` 增加 `FoundBy` 字段。
  - 新增 `internal/merge`:确定性合并纯函数包。
- 兼容性:单模型路径的行为与输出保持字节级一致;多模型与 `--resume` 组合在 v1 明确报错。
- 文档:`pages/src/content/docs/{en,zh,ja,ko,ru}/configuration.md` 增补 `reviewers` 配置说明;`cli-reference.md` 因不新增 flag 基本不变。
- 不改动的部分:评审流水线内部(分组、计划、过滤、对抗性 pass、压缩)、`internal/scan`、工具注册表。
