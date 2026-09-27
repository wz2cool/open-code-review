# Spec Delta

## MODIFIED Requirements

### Requirement: 多 Reviewer 配置

`ocr_ext review` 必须(MUST)支持在配置文件的 `reviewers` 数组中声明追加 reviewer;每个条目必须(MUST)与 `llm` 段同构,自带 `url`、凭证与 `model`,不要求在 providers 注册表中预先命名。`reviewers` 缺省或为空数组时,主模型应(SHALL)作为唯一评审路执行完整单模型评审,报告输出必须(MUST)与 `ocr review` 对同一次运行的结果字节级一致。`ocr review` 不得(SHALL NOT)读取 `reviewers` 字段,且现有配置读写不得(SHALL NOT)将该字段从配置文件中丢弃。配置了 reviewers 时,主模型不得(SHALL NOT)作为评审子进程派发,其角色为协调者(见"主模型协调去重")。彼此端点与模型完全相同的 reviewer 条目必须(MUST)保留首个、丢弃后续;**模型名相同但端点不同的条目必须(MUST)被允许并行运行**,输出归因时自动消歧。条目缺少 `url`、`model` 或凭证(`auth_token` / `auth_token_cmd` 两者皆无)时,`ocr_ext review` 必须(MUST)在任何子进程派发前报错退出,不得(SHALL NOT)静默跳过。

#### Scenario: 条目自含端点

- **WHEN** `reviewers` 含一个携带 `url`、`auth_token`、`model`、`protocol` 的条目
- **THEN** 该条目按自身端点解析为一个追加 reviewer,baseUrl 与凭证均取自条目本身

#### Scenario: 空 reviewers 时拒绝运行

- **WHEN** 配置中没有 `reviewers` 字段,或其为空数组,且用户运行 `ocr_ext review`
- **THEN** 不派发任何 reviewer 子进程,主模型作为唯一评审路执行完整单模型评审,报告输出与 `ocr review` 对同一次运行的结果字节级一致

#### Scenario: ocr review 不感知该字段

- **WHEN** 配置文件含 `reviewers` 字段且用户仅运行 `ocr review`,随后任一配置写回发生
- **THEN** 运行行为与单模型现状一致,且 `reviewers` 字段在配置文件中被原样保留

#### Scenario: 与主模型重复的条目被幂等丢弃

- **WHEN** 多个 reviewer 条目的端点与模型完全相同
- **THEN** 仅保留首个条目,其余被丢弃,不产生重复评审(协调者自身不产出发现,故与协调者同端点同模型的单个条目合法保留)

#### Scenario: 缺字段在派发前报错

- **WHEN** 某条目缺少 `url`、`model` 或凭证
- **THEN** 命令在任何子进程派发前报错退出,错误信息指明该条目

### Requirement: 并行编排与失败降级

存在至少一条有效 reviewer 时,`ocr_ext review` 应(SHALL)将每条 reviewer 派发为一个独立的 `ocr review` 子进程并行执行,不得(SHALL NOT)让任何一路等待其他一路;主模型不得(SHALL NOT)作为评审子进程派发。每个子进程应(SHALL)是一次标准单模型评审(完整复用现有评审流程与 flag,拥有独立的会话历史)。并发交错的进度输出应(SHALL)按来源标注,使每行进度可归属。全部 reviewer 子进程结束后,当触发条件满足(见"主模型协调去重")时执行协调,否则直接输出确定性合并结果。某个 reviewer 子进程失败时,失败应(SHALL)降级为警告,其余来源的结果照常进入合并;**全部** reviewer 失败时,命令必须(MUST)报错退出。协调失败(任何原因)必须(MUST)降级为确定性合并结果加警告,不得(SHALL NOT)影响退出码。`ocr_ext review` 与 `--resume` 或 `--preview` 组合应(SHALL)在启动前报错;以 SARIF 格式请求输出时也应(SHALL)在启动前报错并说明 v1 不支持。

#### Scenario: 并发互不等待

- **WHEN** 两个 reviewer 同时运行
- **THEN** 两者的请求交错进行,任一方的完成或失败不阻塞另一方

#### Scenario: 主模型不占用评审子进程

- **WHEN** 配置了 reviewers 且用户运行 `ocr_ext review`
- **THEN** 派发的子进程数量等于 reviewer 条数,主模型仅参与其后的协调调用

#### Scenario: 子进程为标准单模型评审

- **WHEN** 一次多模型评审完成
- **THEN** 每个来源拥有独立的会话历史,且单模型流程的既有 flag(如 `--effort`)对每个子进程同样生效

#### Scenario: 进度可归属

- **WHEN** 多个子进程的进度行交错输出
- **THEN** 每行进度带有其来源的标注

#### Scenario: reviewer 失败降级

- **WHEN** 某个 reviewer 子进程失败(端点错误、鉴权失败等)
- **THEN** 失败记录为警告,其余来源的结果照常进入合并输出,退出码为 0

#### Scenario: 主模型失败不降级

- **WHEN** 所有 reviewer 子进程全部失败(此时不存在可协调的发现,协调不会触发)
- **THEN** 命令报错退出,不产出任何降级拼凑的报告

#### Scenario: resume 与 preview 组合报错

- **WHEN** `ocr_ext review` 与 `--resume` 或 `--preview` 同时使用
- **THEN** 命令在启动前报错,说明该组合不受支持

#### Scenario: SARIF 请求被拒绝

- **WHEN** `ocr_ext review` 以 `-f sarif` 请求输出
- **THEN** 命令在任何子进程派发前报错,说明 v1 不支持 SARIF 输出

### Requirement: 确定性结果合并

多 reviewer 的发现应(SHALL)先经确定性规则合并,作为协调阶段的骨架:两条发现在位置重叠(多行区间重叠度超过阈值,或同一单行)且内容相似度达到阈值时应(SHALL)归入同组;同组收敛为一条,正文取 severity 较高者(平手取内容更详尽者),修复建议与上下文随正文走,来源取组内全部来源的并集。位置不重叠或相似度不足的发现不得(SHALL NOT)被合并。无法解析出行号的发现应(SHALL)跳过确定性匹配、原样保留。**确定性合并阶段不得(SHALL NOT)发起任何 LLM 调用**——LLM 的使用仅限于"主模型协调去重"需求所定义的协调阶段。确定性合并对任何输入都应(SHALL)产出可用结果,不得(SHALL NOT)因合并失败丢弃发现。

#### Scenario: 主模型与 reviewer 同位置同义收敛

- **WHEN** 两个来源在同一位置报告内容相似的同一问题
- **THEN** 输出中该位置只有一条发现,正文取 severity 较高(平手取更详尽)者,来源包含全部报告它的来源

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

- **WHEN** 确定性合并阶段执行
- **THEN** 该阶段全程不发起任何 LLM 请求

### Requirement: 输出归因与来源统计

多模型运行时,JSON 输出中每条发现必须(MUST)携带 `found_by`:值为产出该发现的来源名称;当多个来源的模型名相同(不同端点的同名模型)时,名称必须(MUST)追加 `@<host>` 后缀消歧。JSON 输出必须(MUST)携带 `sources` 块,为每个 reviewer 来源记录 model、role、发现数、token 用量与会话标识。JSON 输出必须(MUST)携带 `coordination` 元数据块,记录协调是否执行、执行的模型、分组数、输入发现数与协调调用的 token 用量。合并输出不得(SHALL NOT)携带单次运行语义的字段(顶层 session_id、manifest、retry report)。终端文本输出应(SHALL)在多模型运行时为每条发现追加 `found by:` 标注行,并在尾部追加协调摘要行(协调模型与分组数)。单模型运行的输出必须(MUST)与现状字节级一致:不含 `found_by`、`sources` 与 `coordination` 块。

#### Scenario: JSON 归因与来源统计

- **WHEN** 两个 reviewer 的结果经协调后以 JSON 格式输出
- **THEN** 每条发现携带 `found_by`,`sources` 块列出每个 reviewer 的统计,`coordination` 块记录协调执行情况

#### Scenario: 同名模型自动消歧

- **WHEN** 两个 reviewer 配置了同名模型但端点不同
- **THEN** `found_by` 中二者的名称携带各自 `@<host>` 后缀,可区分

#### Scenario: text 归因行

- **WHEN** 合并结果以 text 格式输出
- **THEN** 多来源发现的末尾带有 `found by:` 标注行,列出全部来源名称

#### Scenario: 单模型输出字节级不变

- **WHEN** 配置中没有 `reviewers` 时运行 `ocr review`
- **THEN** JSON 与 text 输出均与现状字节级一致,不含 `found_by`、`sources` 与 `coordination` 块

#### Scenario: 合并输出不含单次运行字段

- **WHEN** 合并结果以 JSON 格式输出
- **THEN** 输出不携带顶层 session_id、manifest 与 retry report

## ADDED Requirements

### Requirement: 主模型协调去重

配置了 reviewers 且触发条件满足(`reviewer 条数 ≥ 2` 且确定性合并后存在至少一条发现)时,`ocr_ext review` 应(SHALL)向主模型端点发起**一次**协调调用:输入为确定性合并结果的编号清单(每条含路径、行号区间、severity、内容,单条内容超长时截断),要求主模型输出分组断言——严格 JSON 的数组的数组,元素为发现编号,表示"同一问题的编号归入同组"。主模型不得(SHALL NOT)被要求或允许改写、发明或删除发现内容。代码侧必须(MUST)校验输出:JSON 可解析、编号均为界内整数;校验失败或调用失败时,必须(MUST)整体回退到确定性合并结果并附加警告,退出码不受影响。合法分组经并查集闭包消解重叠后,每组必须(MUST)按确定性合并的既有规则收敛为一条;未被任何分组覆盖的编号必须(MUST)原样保留。单个分组吸收的发现数超过输入总数的 60% 时,必须(MUST)判定为退化输出并整体回退。协调调用为单次尝试,不得(SHALL NOT)自动重试。顶层配置 `coordination` 为 `false` 时不得(SHALL NOT)发起协调;缺省为 `true`。协调调用的 token 用量必须(MUST)记入报告的 `coordination` 元数据块。

#### Scenario: 分组断言驱动的语义去重

- **WHEN** 确定性合并后的编号清单中,主模型将编号 1 与 4 断言为同一问题
- **THEN** 两条发现按既有规则合并为一条,`found_by` 为二者来源的并集

#### Scenario: 输出不合法时回退

- **WHEN** 主模型返回的分组包含越界编号或无法解析
- **THEN** 协调整体作废,输出确定性合并结果并附加警告,退出码不变

#### Scenario: 退化分组熔断

- **WHEN** 主模型返回的某个分组吸收了超过输入总数 60% 的发现
- **THEN** 协调整体作废,输出确定性合并结果并附加警告

#### Scenario: 触发边界外直接输出

- **WHEN** reviewer 只有 1 条,或确定性合并后不存在任何发现
- **THEN** 不发起协调调用,直接输出确定性结果

#### Scenario: 开关关闭时不协调

- **WHEN** 配置 `coordination: false` 且运行多模型评审
- **THEN** 不发起协调调用,输出确定性合并结果

#### Scenario: 协调元数据可审计

- **WHEN** 协调成功执行
- **THEN** 报告的 `coordination` 块记录执行的模型、分组数、输入发现数与协调调用的 token 用量
