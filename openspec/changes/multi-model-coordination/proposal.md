# Proposal

## Why

已交付的多模型评审(主规范 `multi-model-review`)使用纯确定性合并,存在两个已验证的短板:措辞差异大的同题发现会重复出现(宁多不丢的已知盲区),且主模型亲自下场评审、没有裁决角色——同名模型走不同线路(如 glm 同时走 ark 与公司内网网关,用于配额容灾)会被重名检查拒绝。用户实际使用中明确提出了"主模型做协调、统一出报告"的架构诉求。

## What Changes

- **角色切换**:配置了 reviewers 时,主模型不再作为评审子进程派发,角色切换为**协调者**——子进程只有 reviewers,并行各跑一次标准 `ocr review`;主模型负责对合并结果做语义去重并统一产出报告。无 reviewers 时主模型全量评审,行为与单模型现状字节级不变。
- **协调 pass(新)**:确定性合并(骨架)产出的编号发现清单发给主模型端点,主模型返回**分组断言**(哪些编号是同一个问题);代码侧校验(编号界内、JSON 合法)、union-find 闭包消解重叠分组、按既有 collapse 规则执行合并。**主模型只能分组,不能改写、发明或删除发现内容**——分组之外的一切由代码执行。
- **失败回退**:协调调用失败(网络/配额/输出不合法)→ 整体回退到确定性合并结果 + warning,exit 码不变;单次尝试,不重试。
- **触发边界与熔断**:仅当 `reviewers ≥ 2` 且合并后存在发现时协调;单个分组吸收超过 60% 的发现判定为退化输出,整体回退。
- **同名模型放开**:reviewer 与协调者同名合法;两个 reviewer 同名(不同端点,如配额容灾双线路)允许,`found_by` 自动追加 `@<host>` 消歧;与协调者端点+模型完全相同的 reviewer 条目保留首个、丢弃后续。
- **报告增强**:JSON 报告新增 `coordination` 元数据块(`ran`/`model`/`groups`/`input_findings`/`tokens`),协调过程可审计;text 报告尾部附加协调摘要行。
- **配置开关**:顶层 `"coordination"`(缺省 `true`);`false` 时多模型走纯确定性合并,不发起协调调用。
- 主模型明确不做:调度(保持代码确定性派发,保证全覆盖)、改写发现内容、删除发现、执行合并——分组之外的一切由代码完成。
- 明确不做:`ocr_ext scan` 的协调化;LLM 智能化任务分配(按主题给 reviewer 派活,Phase 2 另议);独立裁判模型字段(后续增量)。

## Capabilities

### Modified Capabilities

- `multi-model-review`:多处需求实质修订——并行编排(子进程只剩 reviewers;新增协调 pass 与其失败回退)、确定性合并(骨架定位,"不得发起 LLM 调用"限定于该阶段)、多 Reviewer 配置(同名放开、与协调者相同条目的处理)、输出归因(found_by 消歧、coordination 块);新增"主模型协调去重"需求(分组断言契约、校验、熔断、触发边界)。

### New Capabilities

无。协调架构是 `multi-model-review` 能力内行为的演进,不产生新能力。

## Impact

- 代码:
  - `cmd/ocrext/review.go`:编排重构——主模型子进程移除、协调触发判断、协调调用与校验、@host 消歧;重名报错移除。
  - `internal/merge`:导出分组合并入口(校验 + union-find + collapse 复用),供协调阶段使用。
  - `cmd/ocrext` 新增协调 prompt(英文常量,符合 english-check)与 coordination 元数据块。
  - `cmd/ocrext`:自解析顶层 `coordination` 开关(`ocr` 侧 `Config` 不加字段,配置往返保留由既有机制保证)。
- 行为变更(非 BREAKING):多模型 JSON 报告新增 `coordination` 块;同名模型的 `found_by` 值可能带 `@host` 后缀(字段格式不变,无存量严格消费者);主模型不再出现在 `sources` 的评审行,改为协调角色行。
- 兼容性:无 reviewers 时单模型路径字节级不变;`coordination: false` 时多模型行为回退到本次变更前的确定性合并。
- 文档:`pages/src/content/docs/{en,zh,ja,ko,ru}/configuration.md` 增补协调开关与行为说明,README 五语言同步。
- 测试:假二进制 + 假协调响应的编排测试、分组校验/union-find/熔断单测、回退路径测试。
- 不改动:`internal/scan`、评审流水线内部、`--config` 机制、临时配置生成、安装与发布面。
