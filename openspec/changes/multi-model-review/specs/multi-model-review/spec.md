# Spec Delta

## Purpose

让 review 运行支持多个 LLM 模型并发协同评审:每个模型独立完整评审同一 diff,跨模型合并去重后输出单一带 `found_by` 归因的评论列表,以并集换更高的 issue 召回率(Ultra Mode 的第一个实现);副 reviewer 与合并环节均为 best-effort,失败不降低主结果可用性。

## ADDED Requirements

### Requirement: 多 reviewer 配置与校验

配置文件 MUST 支持可选数组字段 `additional_reviewers`,每项为仅包含 `provider` 与 `model` 两个字段的对象;endpoint 的 URL、协议与凭据 MUST 复用 `providers` 或 `custom_providers` 中同名条目的对应值,不在 reviewer 条目内定义。配置加载时 MUST 拒绝指向不存在 provider 名的条目,并 MUST 拒绝重复的 (provider, model) 对(含与主模型的重复)。主模型 MUST 仍由现有 `provider` + `model` 字段表达,其字段与语义不变。

#### Scenario: 合法配置触发多模型评审

- **WHEN** 配置了主模型 `provider: zai` / `model: glm-5.3-flash` 与 `additional_reviewers: [{provider: deepseek, model: deepseek-v4.1-flash}]`,且 `providers` 中同时存在两家的凭据
- **THEN** 两个模型各自独立完整评审同一 diff,输出为合并后的单一评论列表

#### Scenario: 拒绝未知 provider

- **WHEN** `additional_reviewers` 中某项的 `provider` 在 `providers` 与 `custom_providers` 中均不存在
- **THEN** 配置加载报错,错误信息指明缺失的 provider 名,评审不启动

#### Scenario: 拒绝重复的 provider 与 model 对

- **WHEN** `additional_reviewers` 含有与主模型相同的 (provider, model),或数组内部存在重复对
- **THEN** 配置加载报错,评审不启动

### Requirement: CLI 临时追加 reviewer

`ocr review` MUST 支持可重复 flag `--reviewer provider/model`,按**第一个**斜杠切分为 provider 与 model,与配置的 `additional_reviewers` 合并生效;值中不含斜杠时 MUST 报错。provider 名约定 MUST NOT 含斜杠。flag 的值 MUST 通过与配置相同的 provider 存在性校验,校验失败 MUST 报错(不得降级为单模型运行)。合并时,与配置条目或主模型重复的 (provider, model) MUST 按幂等去重,同一组合 MUST NOT 重复运行。

#### Scenario: flag 追加额外 reviewer

- **WHEN** 以 `--reviewer deepseek/deepseek-v4.1-flash` 运行 review
- **THEN** 该 (provider, model) 作为额外 reviewer 加入本次运行,与配置中的 `additional_reviewers` 并集生效

#### Scenario: 无斜杠的 flag 值报错

- **WHEN** 以 `--reviewer deepseek-v4.1-flash` 运行 review
- **THEN** 命令报错,提示 `--reviewer` 需要 `provider/model` 形式

#### Scenario: flag 指定的 provider 不存在

- **WHEN** 以 `--reviewer typo-provider/some-model` 运行 review,而该 provider 未配置
- **THEN** 命令报错并指明缺失的 provider 名,不降级为单模型运行

### Requirement: 多模型并发执行与聚合预算

配置了多个 reviewer 时,运行 MUST 为每个 reviewer 建立完全独立的执行上下文(各自的工具循环、会话记录、评论收集器),各 reviewer 的管线内部行为(分组、逐组 review、filter、对抗性 pass)与单模型语义一致;各 reviewer 的执行 MUST NOT 等待其他 reviewer 完成(并发执行)。`--max-tokens-budget` MUST 作为全部 reviewer 的聚合总账生效:聚合用量超过预算后,所有 reviewer MUST 停止发起新的 LLM 请求,在途请求正常完成。输出的 token 统计 MUST 为全部 reviewer 的聚合值。启动输出 MUST 打印 reviewer 名单并标注主模型。

#### Scenario: 两个 reviewer 并发完成各自管线

- **WHEN** 配置了主模型与一个额外 reviewer
- **THEN** 两者并发执行,进度输出各自带模型前缀,互不依赖对方的完成

#### Scenario: 聚合预算耗尽

- **WHEN** 全部 reviewer 的聚合 token 用量超过 `--max-tokens-budget`
- **THEN** 各 reviewer 停止发起新请求,已完成的部分照常进入合并,并记录一条预算 warning

### Requirement: 跨模型合并与归因

多模型运行的评论 MUST 在输出前合并为单一列表,分两层执行:(1) 确定性预合并——同路径、行 span IoU 大于 0.6 且内容信号一致(类别一致且描述相似)的评论才归为同一组;单行与多行评论 MUST NOT 互判重复;仅位置重叠而内容信号不一致的评论对 MUST NOT 在预合并归组,交由整合 pass 判定;(2) LLM 整合 pass——以稳定 id 与模型归属打包全部评论,输出分组与可选的 `merged_content`,且 MUST 实施全覆盖校验:每个输入 id 恰好出现一次,否则放弃该 pass 的全部结果、保留预合并结果。每组 MUST 保留一条代表评论,其 `content` 可被该组的 `merged_content` 覆盖,其余字段保持代表评论原值;该评论的 `found_by` MUST 为组内成员 reviewer 标识的并集(标识默认取模型名,多个 reviewer 同名时附 provider 区分)。合并结果 MUST 按 path 与 `start_line` 稳定重排后输出。LLM 整合 pass 在输入评论数低于门槛时 MUST 跳过;`--no-merge` MUST 跳过整个合并阶段(仅拼接各方列表,每条保留自己的 `found_by`)。

#### Scenario: 两模型发现同一问题合并为一条

- **WHEN** 两个模型在同一路径的重叠行范围上各自提交了对同一问题的评论
- **THEN** 输出中该问题只出现一条评论,其 `found_by` 为两个模型名的并集

#### Scenario: 同位置的不同问题不被预合并吞并

- **WHEN** 两个模型在同一路径的高度重叠行范围上提交了指向不同问题的评论(类别或描述不一致)
- **THEN** 预合并不归组该评论对,两条评论均保留并进入整合 pass,由整合 pass 判定是否为同一问题

#### Scenario: LLM 整合 pass 输出畸形时保留预合并结果

- **WHEN** 整合 pass 的响应缺失某个输入 id 或包含未知 id
- **THEN** 放弃整合结果,输出确定性预合并后的列表,并记录一条 warning

#### Scenario: 合并被跳过

- **WHEN** 以 `--no-merge` 运行多模型 review
- **THEN** 各 reviewer 的评论直接拼接输出,重复项保留,每条 `found_by` 为其来源模型

### Requirement: 失败降级语义

额外 reviewer 或合并环节的失败 MUST 为 best-effort:记录 warning、交付其余结果、不改变退出码。具体地:额外 reviewer 在启动时解析或连接失败,主 reviewer 的结果照常交付;额外 reviewer 中途失败,其已完成部分仍参与合并;LLM 整合 pass 或预合并失败,保留可得的评论列表。主 reviewer 失败时的整体失败语义 MUST 与单模型运行一致。

#### Scenario: 额外 reviewer 启动失败

- **WHEN** 额外 reviewer 的 endpoint 解析失败(如凭据缺失)
- **THEN** 记录 warning,主 reviewer 的评审照常执行并输出,退出码不变

#### Scenario: 额外 reviewer 中途失败

- **WHEN** 额外 reviewer 在部分分组完成后失败
- **THEN** 其已完成的评论参与合并输出,失败记为 warning,退出码不变

#### Scenario: 主 reviewer 失败

- **WHEN** 主 reviewer 的运行失败
- **THEN** 整体运行按单模型相同的失败语义报错退出,不因额外 reviewer 成功而改变

### Requirement: 默认行为与组合限制

未配置 `additional_reviewers` 且未使用 `--reviewer` 时,运行 MUST 保持现有单模型行为不变。多模型运行与 `--resume` 组合时 MUST 显式报错,错误信息 MUST 同时提示两个解除方向:去掉额外 reviewer,或去掉 `--resume`。

#### Scenario: 未配置额外 reviewer 时行为不变

- **WHEN** 配置中无 `additional_reviewers` 且未使用 `--reviewer`
- **THEN** 运行行为、输出与退出码与引入本能力之前完全一致

#### Scenario: 多模型与 resume 组合被拒绝

- **WHEN** 配置了额外 reviewer 且以 `--resume <session-id>` 运行
- **THEN** 命令报错,信息提示去掉额外 reviewer 或去掉 `--resume`
