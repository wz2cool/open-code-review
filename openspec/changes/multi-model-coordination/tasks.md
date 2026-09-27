# Tasks

## 1. 合并包扩展(`internal/merge`)

- [ ] 1.1 导出协调合并入口:输入为编号发现清单与分组断言(`[][]int`),校验编号界内、union-find 闭包消解重叠分组、每组经既有 collapse 收敛、未覆盖编号原样保留;单测覆盖越界编号、重叠分组、空分组、全覆盖分组。验证:`make test` 全绿
- [ ] 1.2 实现退化熔断判定:任一闭包后分组吸收超过输入总数 60% 时返回退化信号;单测覆盖 60% 边界两侧。验证:`make test` 全绿

## 2. 配置与开关

- [ ] 2.1 `ocr_ext` 自解析顶层 `coordination` 开关(轻量 struct,与 reviewers 同路径;`ocr` 侧 `Config` 不加字段),补缺省 true / 显式 false 两分支单测;回归确认含 `coordination` 的配置经 `ocr` 现有 load/save 往返后原样保留。验证:`make test` 全绿
- [ ] 2.2 同名消歧辅助:对同名模型的不同来源,`found_by` 名称追加 `@<host>` 后缀(host 取该来源 URL);单测覆盖同名与不同名两分支。验证:`make test` 全绿

## 3. 协调 pass(`cmd/ocrext`)

- [ ] 3.1 协调 prompt(英文常量,符合 english-check)与调用封装:输入编号清单(内容单条截断 600 字符),经主模型端点 `llm.NewLLMClient` 发起单次补全;`extractTopLevelJSON` 抠取与 JSON 解析;解析失败返回可回退信号(单次尝试不重试),合法分组交 `internal/merge` 协调入口校验并执行。验证:单测以假 client 注入合法/非法/超长输出,断言分组结果与回退
- [ ] 3.2 触发边界与熔断接线:仅当 `reviewer 条数 ≥ 2` 且确定性合并后存在发现、且 `coordination` 开启时执行协调;熔断信号触发时回退并加警告。验证:单测覆盖触发与不触发分支
- [ ] 3.3 编排重构:配置 reviewers 时子进程仅含 reviewers(主模型评审子进程移除);主模型端点仅用于协调调用;与协调者同端点同模型的条目不再丢弃,彼此重复条目保留首个;重名报错移除;空 reviewers 时主模型作为唯一评审路运行;全部 reviewer 失败时报错退出。验证:假二进制单测断言子进程数量与参数、失败语义矩阵、无 reviewers 时报告透传与 `ocr review` 输出字节级一致(同一假子进程产物对比);原重名场景改断言为合法运行
- [ ] 3.4 同名消歧接线:`found_by` 生成时对同名来源追加 `@<host>`;`sources` 块 reviewer 行照旧、新增协调者行(`role: coordinator`,记录协调 token)。验证:单测断言同名/不同名两分支的 found_by 与 sources

## 4. 输出与报告

- [ ] 4.1 JSON 报告新增 `coordination` 元数据块(`ran`/`model`/`groups`/`input_findings`/`tokens`),协调未发生时记录 `ran: false` 与原因;golden 单测。验证:`make test` 全绿
- [ ] 4.2 text 渲染尾部追加协调摘要行(协调模型、分组数);golden 单测。验证:`make test` 全绿
- [ ] 4.3 回退路径产物验证:协调失败时报告为确定性结果 + warnings 含协调失败警告,exit 码 0;golden 单测。验证:`make test` 全绿

## 5. 文档

- [ ] 5.1 五语言 configuration.md 增补 `coordination` 开关与协调去重行为说明;README 五语言多模型评审小节同步一句。验证:文档审阅,五语言齐全
- [ ] 5.2 cli-reference.md 五语言 `ocr_ext review` 节增补协调行为与开关说明。验证:文档审阅通过

## 6. 端到端与最终闸门

- [ ] 6.1 端到端冒烟:假二进制(可编排各路成功/失败/协调响应)跑全流程,断言并发、found_by 消歧、coordination 块、熔断与回退路径、无临时残留;真实验证(需主模型端点配额可用)记录到 PR 描述。验证:冒烟结论记录
- [ ] 6.2 最终闸门:`make check`、`make test`、`make coverage`(阈值达标)全绿;`git add --renormalize .` 后无行尾差异。验证:命令输出全绿
