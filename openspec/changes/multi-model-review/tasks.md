# Tasks

## 1. 配置与解析(cmd 层)

- [ ] 1.1 `Config` 增加 `Reviewers` 字段与条目类型:引用式(`provider` + 可选 `model`)与内联式(`llm` 段同构)两种形态的解析,装载期校验(未知 provider 名在任何 LLM 调用前报错)与幂等去重(与主模型端点+模型相同的条目丢弃),reviewer 身份生成(模型名,重名加 provider 后缀)。验证:单元测试覆盖两种形态、去重、未知 provider 报错路径
- [ ] 1.2 单模型兼容回归:配置缺 `reviewers` 或为空数组时,`ocr review` 行为与输出不变。验证:现有测试全绿 + 新增字节级一致的回归用例

## 2. 并发设施(internal 层)

- [ ] 2.1 `internal/llmloop` 新增原子共享 `TokenCounter` 并接入预算闸门:所有 reviewer 用量记入同一账本,超限全体停止派发新请求;单 reviewer 时语义与现状一致。验证:并发累加与超限截断的单元测试(含 `-race`)
- [ ] 2.2 `internal/stdout` 新增 `Prefixed` writer,`internal/agent` 暴露进度 writer 注入点:每行进度标注 reviewer,标注紧随 `[ocr]` 标记之后,stdout 被 Quiet/Swap 后不向旧目标写入。验证:标注格式与静默不外泄的单元测试
- [ ] 2.3 每 reviewer 独立装配:独立 `Session`、`CommentCollector`、`CommentWorkerPool`,模板与工具注册表共享。验证:装配测试断言无跨 reviewer 共享的可变 collector

## 3. 编排与降级(cmd 层)

- [ ] 3.1 新增多 reviewer 编排:并发运行全部 reviewer、收集各自发现与警告、聚合计数;`review_cmd.go` 接入——`reviewers` 为空走原路径,非空走编排;多模型 + `--resume` 启动前报错。验证:编排单元测试(并发完成、交错无阻塞)+ resume 拒绝用例
- [ ] 3.2 失败降级与遥测:次要 reviewer 失败降级为警告、其余结果照常合并、退出码 0;遥测记录 per-reviewer 用量、合并阶段与降级事件。验证:次要失败与主模型失败两种路径的单元测试

## 4. 确定性合并(internal/merge 新包)

- [ ] 4.1 实现位置匹配(多行区间 IoU > 0.6、单行同位置、单行与多行不匹配、无行号跳过)与内容相似度(≥ 0.5)双条件归组,及组内收敛(正文取 severity 高者、平手取更详尽者、`found_by` 并集、建议随正文)。验证:逐条规则的表驱动单元测试
- [ ] 4.2 边界与永不失败契约:空输入、全部无行号、极端重叠、severity 缺失;任何输入产出可用结果且零 LLM 调用。验证:边界用例单元测试

## 5. 输出归因

- [ ] 5.1 `model.LlmComment` 增加 `FoundBy`(omitempty):JSON 输出按注册顺序携带;终端文本输出在多模型运行时每条发现尾部加来源标注行。验证:JSON 字段顺序与文本标注的单元测试
- [ ] 5.2 manifest 在多模型运行时记录 reviewers 清单与各方/聚合 token 用量;单模型运行不含这些字段。验证:两种运行各一条 manifest 断言

## 6. 端到端与文档

- [ ] 6.1 e2e 冒烟:双 reviewer 并发跑通完整流程,核对合并、归因、进度标注、降级与共享预算截断各场景。验证:e2e 测试 + 本地 `ocr review` 实跑一次双模型配置
- [ ] 6.2 工程收尾:`make check`、`make test`、`make coverage`(90% 门槛)全绿;`make license-add` 覆盖全部新文件。验证:三条 make 命令的输出
- [ ] 6.3 文档:`pages/src/content/docs/{en,zh,ja,ko,ru}/configuration.md` 增补 `reviewers` 配置说明(引用式/内联式、示例、预算与降级行为);`telemetry.md` 同步五个语言,增补 per-reviewer 用量、合并与降级事件;核对 `cli-reference.md` 因无新 flag 无需变更。验证:文档文件就位且示例一致
