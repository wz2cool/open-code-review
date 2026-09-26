# Tasks

## 1. 配置与解析

- [ ] 1.1 `Config` 新增 `AdditionalReviewers` 字段与解析;实现校验:拒绝指向不存在 provider 的条目、拒绝重复的 (provider, model) 对(含与主模型重复);单测覆盖合法配置、未知 provider、重复对三种路径
- [ ] 1.2 实现 `--reviewer provider/model` 可重复 flag:按第一个斜杠切分,无斜杠或空 model 报错,复用 1.1 的 provider 存在性校验(flag 指定的 provider 不存在即报错,不降级),与配置条目或主模型重复的值幂等去重;单测覆盖切分规则、两类报错路径与去重
- [ ] 1.3 实现按 reviewer 列表逐个解析 endpoint 并构建各自的 `LLMClient` / model(复用 `ResolveEndpointWithOptions`);单测验证多 endpoint 各自解析正确、单个解析失败产生可归因错误
- [ ] 1.4 在 pages/src/content/docs/{en,zh,ja,ko,ru} 的 configuration.md 记录 `additional_reviewers`(含手动编辑路径)与 cli-reference.md 记录 `--reviewer`,并验证 `npm --prefix pages run build` 通过

## 2. 多 agent 编排

- [ ] 2.1 实现每 reviewer 独立的 Agent / Runner / Session / CommentCollector / tool registry(collector 内嵌于 registry,必须各自构造)/ CommentWorkerPool,以及跨 Runner 的原子聚合 token 计数器接入 `--max-tokens-budget`;单测验证聚合计数与"超限后所有 Runner 均停止发起新请求、在途请求正常完成"
- [ ] 2.2 实现 goroutine 并发执行与汇合,启动时打印 reviewer 名单并标注主模型,进度输出按 reviewer 加前缀;单测验证两个 agent 并发完成、名单输出且前缀出现在进度输出中;并验证共享 MCP client 会话在并发调用下安全(不安全则按 reviewer 复制 client)
- [ ] 2.3 实现失败降级三条路径:额外 reviewer 启动失败(警告 + 主结果照常)、中途失败(已完成部分参与合并)、主 reviewer 失败(退出语义与单模型一致);单测覆盖三条路径
- [ ] 2.4 实现多模型与 `--resume` 组合的显式报错,错误信息提示两个解除方向;单测覆盖
- [ ] 2.5 运行 manifest 增加 reviewers 列表(名字 + 解析来源,不含秘密,仅多模型运行写入),遥测事件增加 reviewer 维度;单测验证 manifest 内容,并验证单模型运行 manifest 逐字节不变
- [ ] 2.6 输出以主 reviewer 为骨架:`session_id` 与 manifest 取主 reviewer,token 统计与 warnings 聚合全部 reviewer;单测验证聚合数值,并验证单模型路径输出不变

## 3. 跨模型合并器

- [ ] 3.1 实现确定性预合并:同路径 + 行 span IoU > 0.6 且内容护栏(同 category 且文本相似度达到阈值)才归组,单行与多行不互判,位置匹配但内容信号不足的对不归组;单测移植 GitHub Action 脚本 `sameCommentSpan` 的判定用例并补跨模型边界(不同锚点不合并、同位置不同问题不合并)
- [ ] 3.2 新增 review 模板任务 `REVIEW_MERGE_TASK`(prompt 与 `Template` 结构,形态对标 scan `DEDUP_TASK`,`ApplyLanguage` 覆盖之);单测验证模板加载与语言注入
- [ ] 3.3 实现 LLM 整合 pass:稳定 `c-N` id 与模型归属打包、`groups` / `merged_content` 解析、全覆盖校验(缺失 / 未知 / 重复 id 整体作废)、失败保底;单测对标 `internal/scan/dedup_test.go` 的畸形输入用例
- [ ] 3.4 实现 `found_by` 并集计算(按 reviewer 注册顺序排列,主模型在前)与代表元规则(首成员为代表,`merged_content` 仅覆盖 `content`);实现评论数门槛与 `--no-merge` 开关(跳过合并仅拼接);单测覆盖归因、顺序稳定性、代表元、门槛、开关
- [ ] 3.5 合并结果按 path + `start_line` 稳定重排;单测验证输出顺序与 reviewer 完成顺序无关
- [ ] 3.6 在 pages/src/content/docs/{en,zh,ja,ko,ru} 的 cli-reference.md 记录 `--no-merge`,telemetry.md 记录合并阶段事件,并验证 `npm --prefix pages run build` 通过

## 4. 输出与下游兼容

- [ ] 4.1 `LlmComment` 新增可选 `found_by` 字段并进入 JSON 输出;单测验证 JSON 包含该字段且单模型运行时不出现在输出中
- [ ] 4.2 文本输出按 `found_by` 呈现标注(如共识标记),SARIF 输出保持兼容;单测验证两种格式的渲染
- [ ] 4.3 在 pages/src/content/docs/{en,zh,ja,ko,ru} 的对应输出文档中记录 `found_by` 字段,并验证 `npm --prefix pages run build` 通过

## 5. 集成验证

- [ ] 5.1 端到端测试(双 fake LLM client):多模型并发、合并去重、`found_by` 归因、副模型失败降级在一条链路中验证
- [ ] 5.2 本地冒烟:双 reviewer 配置跑 `ocr review` 验证 reviewer 名单输出与合并结果;同时验证未配置 `additional_reviewers` 时行为与引入前一致(默认路径回归)
- [ ] 5.3 `make check`、`make test`、`make coverage` 全部通过
