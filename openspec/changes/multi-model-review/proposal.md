# Proposal

## Why

部分变更集(安全敏感、高风险)需要尽可能高的 issue 召回率——ROADMAP 已把这个方向规划为 Ultra Mode(H2 2026):"用更多 token 消耗和 review 时间换显著更高的召回率",但未规定实现机制。当前 `ocr` 的整条 review 管线(plan、分组、逐组 review、filter、对抗性 pass)绑定在单一激活模型上,而不同模型的盲区互补:已归档的 adversarial-review-defaults 遥测证明,同一个模型跑第二遍就能多找出 2/5 的新发现,两个不同模型的互补收益理应更大。本 change 是 Ultra Mode 的第一个具体实现:多个模型各自独立完整评审,并集合并去重后输出。

## What Changes

- 新增 `additional_reviewers` 配置项:`{provider, model}` 对象数组;主模型沿用现有 `provider` + `model` 字段,零迁移。每个 reviewer 复用 `providers` / `custom_providers` 中同名条目的 URL、协议、凭据,自身不携带任何秘密。配置校验拒绝指向不存在 provider 的条目与重复的 (provider, model) 对。
- 新增可重复 flag `--reviewer provider/model`(按第一个斜杠切分,provider 名不允许含斜杠)用于单次追加额外 reviewer。
- review 运行支持多 reviewer **并发**执行:每个 reviewer 拥有独立的 Runner、Session、CommentCollector;所有 Runner 共享一个原子聚合 token 计数器,`--max-tokens-budget` 按总账生效——超限后所有 reviewer 停止发起新请求(在途请求正常完成),已完成部分照常进入合并。进度输出按模型加前缀(如 `[ocr] [glm] ...`);机器可读输出不受影响(在合并之后生成)。
- 新增跨模型合并阶段,分两层:
  - 确定性预合并(纯 Go):同路径 + 行 span IoU > 0.6 **且内容信号一致**(同 category 与描述相似)才判重(位置判定复用 GitHub Action 脚本 `sameCommentSpan` 的思路;内容护栏防止"同位置的不同问题"被静默合并——GA 的纯位置判重只对同一模型自己的历史评论安全,跨模型会丢发现)。单行与多行评论不互判重复;位置匹配但内容不一致的对交给 LLM 整合 pass;
  - LLM 整合 pass(复刻 scan `DEDUP_TASK` 形态):全部评论带稳定 `c-N` id 与模型归属打包,LLM 输出 `groups`(members + 可选 `merged_content` 融合描述),全覆盖校验(每个 id 恰好出现一次,否则整体作废保留预合并结果),best-effort 失败语义。
- 评论新增可选字段 `found_by`(发现该评论的模型名并集);合并结果按 path + start_line 重排后走既有输出管线(JSON / SARIF / 文本,`found_by` 在 JSON 中输出)。
- 失败降级:某个额外 reviewer 解析失败或中途失败、合并 pass 失败 → 记录 warning、交付其余结果、不改退出码(与对抗性 pass 的 best-effort 哲学一致)。
- 多模型运行与 `--resume` 组合在 v1 显式报错,提示去掉额外 reviewer 或去掉 resume。
- 默认行为完全不变:未配置 `additional_reviewers` 时保持单模型单管线。

## Capabilities

### New Capabilities

- `multi-model-review`: 多 reviewer 的配置与校验(`additional_reviewers`、`--reviewer`)、多模型并发执行与聚合预算、跨模型评论合并(确定性预合并 + LLM 整合 pass)、`found_by` 归因输出、副 reviewer 与合并的失败降级语义。

### Modified Capabilities

(无——`review-prompting` 的需求不变:每个 reviewer 的管线内部行为,包括对抗性 pass 与 filter,完全照旧;本 change 只在其外层新增编排与合并。)

## Impact

- **代码**:
  - `cmd/opencodereview`:配置结构体与校验(`Config` 新增 `AdditionalReviewers`)、`review_cmd` / `shared.go` 的 `loadLLMRuntime`(需支持按 reviewer 逐个解析 endpoint)、flag 注册;
  - `internal/agent`:多 agent 并发编排(每模型一个 Agent)、跨模型合并器;
  - `internal/model`:`LlmComment` 增加可选 `found_by` 字段;
  - `internal/config/template`:新增 review 侧合并任务模板(命名与 prompt 随 design 定稿,形态对标 scan `DEDUP_TASK`);
  - `internal/session`:reviewer 维度的会话归属。
- **下游兼容**:JSON/SARIF schema 增加可选 `found_by`;JetBrains 扩展 `ignoreUnknownKeys = true` 天然容忍新字段;GitHub Action 发帖脚本与 `action.yml` 输入的展示适配延后(用户主用本地 CLI)。
- **成本**:每增加一个 reviewer,token 消耗约 ×N;严格 opt-in,默认单模型不变。
- **遥测**:新增 `merge.completed` / `merge.failed` 事件,运行事件增加 reviewer 维度。
