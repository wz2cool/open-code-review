# Spec Delta

## Purpose

定义多 reviewer 评审的契约:用户在 `~/.opencodereview/config.json` 的 `reviewers` 数组中声明追加模型,`ocr review` 并发地为主模型与每个追加 reviewer 各执行一次完整评审流程,将各路发现经确定性规则合并为一份结果并标注来源;单模型路径的行为与输出保持不变。

## ADDED Requirements

### Requirement: 多 Reviewer 配置

`ocr review` 必须(MUST)支持在配置文件的 `reviewers` 数组中声明追加 reviewer,每个条目必须(MUST)支持两种形态:引用式(提供 `provider` 名与可选 `model` 覆盖,凭证复用 `providers`/`custom_providers` 注册表)与内联式(与 `llm` 段同构的端点对象)。`reviewers` 缺省或为空时,运行行为与输出必须(MUST)与单模型现状完全一致。解析出与主模型相同端点与模型的条目必须(MUST)被幂等丢弃。引用了不存在的 provider 名时必须(MUST)在任何 LLM 调用前报错退出,不得(SHALL NOT)静默降级为单模型。

#### Scenario: 引用式条目追加 reviewer

- **WHEN** `reviewers` 含 `{"provider": "deepseek"}` 且 `providers` 注册表存在 `deepseek`
- **THEN** 该条目以注册表凭证解析为一个追加 reviewer,模型缺省取该 provider 的默认模型

#### Scenario: 内联式条目

- **WHEN** `reviewers` 含一个携带 `url`、`auth_token`、`model`、`protocol` 的端点对象
- **THEN** 该条目按内联端点解析为一个追加 reviewer,不要求在注册表中预先命名

#### Scenario: 缺省时行为不变

- **WHEN** 配置中没有 `reviewers` 字段,或其为空数组
- **THEN** 运行行为与单模型现状一致,输出含 manifest 与现状字节级一致

#### Scenario: 与主模型重复的条目被幂等丢弃

- **WHEN** 某条目解析出的端点与模型和主模型完全相同
- **THEN** 该条目被丢弃,不产生第二个相同 reviewer

#### Scenario: 未知 provider 在调用前报错

- **WHEN** 某引用式条目的 `provider` 名在 `providers` 与 `custom_providers` 中均不存在
- **THEN** 命令在任何 LLM 调用前报错退出,错误信息指明该条目

### Requirement: 并发执行与共享预算

解析与去重后仍存在至少一个追加 reviewer 时,`ocr review` 应(SHALL)并发执行主模型与全部追加 reviewer 的评审,不得(SHALL NOT)让任何 reviewer 等待其他 reviewer 完成。每个 reviewer 应(SHALL)拥有独立的会话历史,并完整复用单模型评审流程。进度输出应(SHALL)按 reviewer 标注,使并发交错时每行进度可归属。所有 reviewer 应(SHALL)共用一个聚合 token 预算账本;累计用量加下一组预估超过预算时,所有 reviewer 必须(MUST)停止派发新请求,在途请求跑完,已有结果照常合并,以警告与退出码 0 结束。某个追加 reviewer 失败时,失败应(SHALL)降级为警告,其余 reviewer 的结果照常合并输出;主模型失败时不得(SHALL NOT)降级,应(SHALL)维持单模型现状的失败语义。

#### Scenario: 并发互不等待

- **WHEN** 主模型与一个追加 reviewer 同时运行
- **THEN** 两者的请求交错进行,任一方的完成或失败不阻塞另一方

#### Scenario: 独立会话

- **WHEN** 多 reviewer 运行
- **THEN** 每个 reviewer 拥有独立的会话历史,互不混写

#### Scenario: 进度可归属

- **WHEN** 多个 reviewer 的进度行交错输出
- **THEN** 每行进度带有其 reviewer 的标注

#### Scenario: 共享预算超限

- **WHEN** 共享账本的累计用量使某 reviewer 的下一组派发将超出预算
- **THEN** 所有 reviewer 停止派发新请求,在途请求完成后已有发现照常合并输出,运行以警告结束且退出码为 0

#### Scenario: 次要 reviewer 失败降级

- **WHEN** 某个追加 reviewer 的评审失败(端点错误、鉴权失败等)
- **THEN** 失败记录为警告,其余 reviewer 的结果照常合并输出,退出码不受该失败影响

#### Scenario: 主模型失败不降级

- **WHEN** 主模型的评审失败
- **THEN** 命令维持单模型现状的失败语义(报错退出),不产出由次要 reviewer 拼凑的降级结果

### Requirement: 确定性结果合并

多 reviewer 的发现应(SHALL)仅经确定性规则合并,合并过程不得(SHALL NOT)发起任何 LLM 调用。两条发现在位置重叠(多行区间重叠度超过阈值,或同一单行)且内容相似度达到阈值时应(SHALL)归入同组;同组收敛为一条,正文取 severity 较高者(平手取内容更详尽者),修复建议与上下文随正文走,`found_by` 取组内全部来源的并集。位置不重叠或相似度不足的发现不得(SHALL NOT)被合并。无法解析出行号的发现应(SHALL)跳过确定性匹配、原样保留。合并对任何输入都应(SHALL)产出可用结果,不得(SHALL NOT)因合并失败丢弃发现。

#### Scenario: 同位置同义收敛

- **WHEN** 两个 reviewer 在同一位置报告内容相似的同一问题
- **THEN** 输出中该位置只有一条发现,正文取 severity 较高(平手取更详尽)者,`found_by` 为两个来源

#### Scenario: 同位置不同问题保留

- **WHEN** 两个 reviewer 在同一位置报告内容相似度低于阈值的不同问题
- **THEN** 两条发现都保留,各自标注来源

#### Scenario: 不同位置不合并

- **WHEN** 两条发现的行区间不重叠,或为不同的单行
- **THEN** 两条发现都保留

#### Scenario: 无行号发现保留

- **WHEN** 某发现没有可用的行号
- **THEN** 该发现跳过确定性匹配,原样进入输出并保留其来源

### Requirement: 输出归因

多模型运行的 JSON 输出中每条发现应(SHALL)携带 `found_by`,按 reviewer 注册顺序排列(primary 在前);终端文本输出应(SHALL)为每条发现追加来源标注行。单模型运行的输出不得(SHALL NOT)包含 `found_by` 或来源标注。manifest 应(SHALL)在多模型运行时记录 reviewer 清单及每个 reviewer 与聚合的 token 用量;单模型运行的 manifest 不得(SHALL NOT)包含这些字段。多模型与 `--resume` 组合应(SHALL)在启动评审前明确报错。

#### Scenario: JSON 归因

- **WHEN** 多模型运行产出合并结果
- **THEN** JSON 中每条发现都有 `found_by`,顺序与 reviewer 注册顺序一致

#### Scenario: 终端来源标注

- **WHEN** 多模型运行以终端文本格式呈现结果
- **THEN** 每条发现带来源标注行;单模型运行不出现该行

#### Scenario: manifest 记录

- **WHEN** 多模型运行结束
- **THEN** manifest 记录 reviewers 清单与各方及聚合 token 用量,输出沿用主模型的会话标识;单模型 manifest 不含这些字段

#### Scenario: resume 明确拒绝

- **WHEN** 多模型运行携带 `--resume`
- **THEN** 命令在启动评审前报错退出,提示该组合暂不支持
