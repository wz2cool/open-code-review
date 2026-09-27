# multi-model-review Specification

## Purpose
定义多模型评审的契约:用户在配置文件的 `reviewers` 数组中声明若干追加 reviewer,`ocr_ext review` 将主模型与每条 reviewer 各派发为一次标准 `ocr review` 子进程并行执行——reviewer 端点经独立临时配置文件传递——并经确定性规则合并为一份带来源归因的结果;`ocr review` 单模型路径在不使用新增 `--config` flag 时行为与输出保持不变。

## Requirements

### Requirement: 多 Reviewer 配置

`ocr_ext review` 必须(MUST)支持在配置文件的 `reviewers` 数组中声明追加 reviewer;每个条目必须(MUST)与 `llm` 段同构,自带 `url`、凭证与 `model`,不要求在 providers 注册表中预先命名。`reviewers` 缺省或为空数组时,`ocr_ext review` 必须(MUST)在任何子进程派发前报错并提示使用 `ocr review`,不得(SHALL NOT)静默执行单模型评审。`ocr review` 不得(SHALL NOT)读取 `reviewers` 字段,且现有配置读写不得(SHALL NOT)将该字段从配置文件中丢弃。解析出与主模型相同端点与模型的条目必须(MUST)被幂等丢弃,不产生重复 reviewer。条目缺少 `url` 或 `model` 时,`ocr_ext review` 必须(MUST)在任何子进程派发前报错退出,不得(SHALL NOT)静默跳过。

#### Scenario: 条目自含端点

- **WHEN** `reviewers` 含一个携带 `url`、`auth_token`、`model`、`protocol` 的条目
- **THEN** 该条目按自身端点解析为一个追加 reviewer,baseUrl 与凭证均取自条目本身

#### Scenario: 空 reviewers 时拒绝运行

- **WHEN** 配置中没有 `reviewers` 字段,或其为空数组,且用户运行 `ocr_ext review`
- **THEN** 命令在任何子进程派发前报错,错误信息提示使用 `ocr review` 进行单模型评审

#### Scenario: ocr review 不感知该字段

- **WHEN** 配置文件含 `reviewers` 字段且用户仅运行 `ocr review`,随后任一配置写回发生
- **THEN** 运行行为与单模型现状一致,且 `reviewers` 字段在配置文件中被原样保留

#### Scenario: 与主模型重复的条目被幂等丢弃

- **WHEN** 某条目解析出的端点与模型和主模型完全相同
- **THEN** 该条目被丢弃,不产生第二个相同 reviewer

#### Scenario: 缺字段在派发前报错

- **WHEN** 某条目缺少 `url` 或 `model`
- **THEN** 命令在任何子进程派发前报错退出,错误信息指明该条目

### Requirement: 并行编排与失败降级

存在至少一条有效 reviewer 时,`ocr_ext review` 应(SHALL)将主模型与每条 reviewer 各派发为一个独立的 `ocr review` 子进程并行执行,不得(SHALL NOT)让任何一路等待其他一路;每个子进程应(SHALL)是一次标准单模型评审(完整复用现有评审流程与 flag,拥有独立的会话历史)。并发交错的进度输出应(SHALL)按来源标注,使每行进度可归属。某个 reviewer 子进程失败时,失败应(SHALL)降级为警告,其余来源的结果照常合并输出,退出码为 0;主模型子进程失败时不得(SHALL NOT)降级,应(SHALL)报错退出。`ocr_ext review` 与 `--resume` 或 `--preview` 组合应(SHALL)在启动前报错;以 SARIF 格式请求输出时也应(SHALL)在启动前报错并说明 v1 不支持。

#### Scenario: 并发互不等待

- **WHEN** 主模型与一个追加 reviewer 同时运行
- **THEN** 两者的请求交错进行,任一方的完成或失败不阻塞另一方

#### Scenario: 子进程为标准单模型评审

- **WHEN** 一次多模型评审完成
- **THEN** 每个来源拥有独立的会话历史,且单模型流程的既有 flag(如 `--effort`)对每个子进程同样生效

#### Scenario: 进度可归属

- **WHEN** 多个子进程的进度行交错输出
- **THEN** 每行进度带有其来源的标注

#### Scenario: reviewer 失败降级

- **WHEN** 某个 reviewer 子进程失败(端点错误、鉴权失败等)
- **THEN** 失败记录为警告,其余来源的结果照常合并输出,退出码为 0

#### Scenario: 主模型失败不降级

- **WHEN** 主模型子进程失败
- **THEN** 命令报错退出,不产出由次要 reviewer 拼凑的降级结果

#### Scenario: resume 与 preview 组合报错

- **WHEN** `ocr_ext review` 与 `--resume` 或 `--preview` 同时使用
- **THEN** 命令在启动前报错,说明该组合不受支持

#### Scenario: SARIF 请求被拒绝

- **WHEN** `ocr_ext review` 以 `-f sarif` 请求输出
- **THEN** 命令在任何子进程派发前报错,说明 v1 不支持 SARIF 输出

### Requirement: 确定性结果合并

合并范围必须(MUST)覆盖主模型与全部 reviewer 的发现。两条发现在位置重叠(多行区间重叠度超过阈值,或同一单行)且内容相似度达到阈值时应(SHALL)归入同组;同组收敛为一条,正文取 severity 较高者(平手取内容更详尽者),修复建议与上下文随正文走,来源取组内全部来源的并集。位置不重叠或相似度不足的发现不得(SHALL NOT)被合并。无法解析出行号的发现应(SHALL)跳过确定性匹配、原样保留。合并应(SHALL)仅经确定性规则完成,不得(SHALL NOT)发起任何 LLM 调用;合并对任何输入都应(SHALL)产出可用结果,不得(SHALL NOT)因合并失败丢弃发现。

#### Scenario: 主模型与 reviewer 同位置同义收敛

- **WHEN** 主模型与某个 reviewer 在同一位置报告内容相似的同一问题
- **THEN** 输出中该位置只有一条发现,正文取 severity 较高(平手取更详尽)者,来源包含主模型与该 reviewer

#### Scenario: 位置不同不合并

- **WHEN** 两个来源在不同位置报告内容相似的发现
- **THEN** 两条发现均原样保留,各自携带其来源

#### Scenario: 内容不相似不合并

- **WHEN** 两个来源在同一位置报告内容明显不同的两个问题
- **THEN** 两条发现均原样保留,不因位置相同而被吞并

#### Scenario: 无行号发现原样保留

- **WHEN** 某条发现无法解析出行号
- **THEN** 该发现跳过确定性匹配、原样保留在输出中

#### Scenario: 合并零 LLM 调用

- **WHEN** 多路结果合并执行
- **THEN** 合并全程不发起任何 LLM 请求

### Requirement: 输出归因与来源统计

多模型运行时,JSON 输出中每条发现必须(MUST)携带 `found_by`(组内全部来源的模型名,按注册顺序);合并输出必须(MUST)携带 `sources` 块,记录每个来源的 provider、model、发现数、token 用量与会话标识。合并输出不得(SHALL NOT)携带单次运行语义的字段(顶层 session_id、manifest、retry report)。终端文本输出应(SHALL)在多模型运行时为每条发现追加 `found by:` 标注行。单模型运行的输出必须(MUST)与现状字节级一致:不含 `found_by` 字段与来源块。

#### Scenario: JSON 归因与来源统计

- **WHEN** 主模型与两个 reviewer 的结果合并后以 JSON 格式输出
- **THEN** 每条发现携带 `found_by`,`sources` 块列出三个来源各自的 provider、model、发现数、token 用量与会话标识

#### Scenario: text 归因行

- **WHEN** 合并结果以 text 格式输出
- **THEN** 多来源发现的末尾带有 `found by:` 标注行,列出全部来源模型名

#### Scenario: 单模型输出字节级不变

- **WHEN** 配置中没有 `reviewers` 时运行 `ocr review`
- **THEN** JSON 与 text 输出均与现状字节级一致,不含 `found_by` 与来源块

#### Scenario: 合并输出不含单次运行字段

- **WHEN** 合并结果以 JSON 格式输出
- **THEN** 输出不携带顶层 session_id、manifest 与 retry report

### Requirement: Reviewer 端点传递

`ocr review` 必须(MUST)支持可选 `--config <path>` flag 指定配置文件路径;未指定时必须(MUST)使用默认路径,行为与输出与现状完全一致。`ocr_ext review` 应(SHALL)为每条 reviewer 生成一份临时配置文件——内容为用户配置的拷贝,以该 reviewer 条目替换 `llm` 段并清除 provider 选择——并必须(MUST)以该配置运行对应子进程;主模型子进程不得(SHALL NOT)携带 `--config`。临时配置含有凭证,创建时权限必须(MUST)仅限属主读写,运行结束后必须(MUST)删除。

#### Scenario: reviewer 子进程以其条目端点运行

- **WHEN** 某条 reviewer 携带 `url`、`auth_token`、`model`、`protocol`
- **THEN** 该 reviewer 子进程经 `--config` 临时配置运行,评审请求发往条目 `url` 并使用条目 `model`,子进程输出中的 llm 身份与条目一致

#### Scenario: 未指定 --config 时行为不变

- **WHEN** `ocr review` 不带 `--config` 运行
- **THEN** 配置读取路径、运行行为与输出均与现状完全一致

#### Scenario: 临时配置的生命周期

- **WHEN** 一次多模型运行结束
- **THEN** 各 reviewer 的临时配置文件已被删除,存在期间权限仅限属主读写
