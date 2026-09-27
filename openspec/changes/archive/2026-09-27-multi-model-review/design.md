# Design

## Context

单模型评审流程完整存在且高度自包含:`ocr review` 的编排链在 `cmd/opencodereview/review_cmd.go`(端点解析 → 会话准入 → `agent.New` → `Run` → `emitRunResult`),`--provider`/`--model` flag 已支持按 run 覆盖模型,`-f json -o <file>` 已支持机器可读落盘输出;session 存储按 UUID 每次运行一份(`$HOME/.opencodereview/sessions/<repo>/<uuid>.jsonl`),并行运行天然不冲突。配置端,`internal/llm/resolver.go` 的解析链(config `provider`+注册表 / `llm` 段 / 环境变量)与 `LlmConfig` 类型(`url`/`auth_token`/`model`/`protocol`/`auth_header`/`extra_body`)现成可用;`Config` 的 `unknownJSONFields` 机制(UnmarshalJSON/MarshalJSON 实际接线,config_cmd.go:450-472)保证旧二进制读写 config 不丢弃新字段。

架构约束:`registerReviewFlags`、`renderComment`、`jsonOutput`、`Config` 等均在 `cmd/opencodereview`(package main);Go 不允许跨 main 包 import,`ocr_ext` 无法复用其中任何符号——flag 集、配置解析、输出渲染在 `ocr_ext` 内自含,只通过 CLI 参数、临时配置文件与 JSON 文件接口与 `ocr` 交互。构建产物名:`Makefile` 的 `BINARY_NAME := opencodereview`(dist 产物名),install.sh / install.ps1 将其安装为 `ocr`;`ocr_ext` 同理(构建产物 `ocrext`,安装名 `ocr_ext`)。

本变更是一份重写:同名旧提案(进程内并发、共享 token 账本、进度 writer 注入,约 2200 行)已整体作废删除,旧实现从未提交;本文件只覆盖缩小后的子进程编排形态。动机见 proposal.md。

## Goals / Non-Goals

**Goals:**

- 一个命令完成多模型交叉评审:`ocr_ext review` 派发、等待、合并、输出,用户无感。
- 每个子进程是一次 100% 标准 `ocr review`:评审流水线(分组、计划、过滤、对抗性 pass、压缩)、session、flag 全部被原样复用;`ocr` 侧唯一改动是新增可选 `--config` flag(不使用时行为不变)。
- 确定性合并与来源归因:`internal/merge` 纯函数包,零 LLM 调用,永不失败。
- 配置最简:`reviewers` 是 `llm` 段的数组,每条自带 baseUrl,一种形态、无注册表引用。
- 单模型路径行为不变:`ocr review` 不读 `reviewers`、不进任何编排分支;不使用 `--config` 时输出字节级一致。

**Non-Goals:**

- 无 `ocr_ext scan`:v1 只做 review,scan 以后按同一模式增量加入。
- SARIF 输出 v1 不支持:现有 SARIF 转换在 `cmd/opencodereview` 内不可跨 main 包复用,抽取重构会扩大 `ocr` 侧 diff;text + JSON 已覆盖"人 + agent"的全部消费方;以后需要时把转换抽到 internal 包再增量加入。
- npm 包与 action.yml 不接入 `ocr_ext`:CI 场景 v1 维持单模型。
- 多模型 + `--resume` / `--preview` 报错:恢复是单模型会话语义,preview 无合并对象。
- 无共享 token 预算:每个子进程各自预算(`--max-tokens-budget` 按 run 生效),总花费不设联合上限。
- 不做 LLM 合并 pass、不做向量相似度:v1 接受"措辞差异大的同题发现会重复出现"(宁多不丢)。
- 不做共识优先排序:"两个模型都报"衡量重叠度而非重要性,severity 才是优先级信号;`found_by` 已让共识肉眼可见。

## Decisions

### D1: 二进制形态与子进程统一派发

`ocr_ext` 是独立二进制(新 main 包 `cmd/ocrext`),与 `ocr` 同仓库同模块。父进程将主模型与每条 reviewer **一律**派发为标准 `ocr review` 子进程(N+1 路,`os/exec`),自身不含任何评审流水线代码——主模型不走"进程内直跑"的特例,编排器保持纯薄,进度转发与失败语义对所有来源一致。

子进程定位:优先在 `ocr_ext` 同目录找 `ocr`,找不到再找 `opencodereview`(安装后目录里是 `ocr`,开发环境 dist 旁边是 `opencodereview`),其次 PATH(两个名字都尝试);都找不到报错。找到后运行一次 `ocr version`,与自身版本不一致时警告但继续。

备选方案均被否决:`ocr review` 子命令自编排(需环境变量抑制递归,把防递归变成运行时补丁);进程内 fan-out(旧提案形态,需向 `internal/stdout`/`internal/agent` 注入进度 writer、实现共享账本,约 2200 行且触碰核心包)。独立二进制的分发面代价(Makefile 与安装脚本各加一个产物)是机械改动,用户明确选择此形态。

### D2: 配置形态与 reviewer 端点传递

`reviewers` 只有内联一种形态:每个条目与 `llm` 段同构,必须自含 `url`、凭证与 `model`;数组顺序即输出中的来源顺序。与主模型解析结果端点+模型完全相同的条目幂等丢弃——主端点经 exported 的 `llm.ResolveEndpoint`(子进程同样用它解析 llm 段与 `--config` 配置)取得后比较。校验(缺 `url`/`model`、空数组)在任何子进程派发前完成;空数组直接报错并提示用 `ocr review`(`ocr_ext` 是显式多模型入口,空配置属用法错误)。

reviewer 端点传递是本形态的核心机制,经 `ocr review` 新增的可选 `--config <path>` flag 完成:`ocr_ext` 为每条 reviewer 生成一份临时配置——用户配置的拷贝,`llm` 段替换为该条目、provider 选择清除(已验证 resolver.go:390 `cfg.Provider != ""` 时走注册表路径,必须清才能让 llm 段生效)——reviewer 子进程携带该 `--config` 运行;主模型子进程不带 `--config`(走用户配置的主解析链)。选这条路而非环境变量的原因:resolver 的策略序是 config 文件 → OCR env → CC env → shell rc(resolver.go:127-148),用户 config 必有完整主端点,`OCR_LLM_*` 环境变量永远轮不到;且 `llm` 段的 protocol 语义(含 anthropic legacy 默认)在配置文件中原样生效。临时配置含凭证:权限 0600、运行结束即删。

`ocr_ext` 不经 `Config` 结构读 `reviewers`(那在 `cmd/opencodereview` 的 main 包里,不可 import):自己解析 JSON(轻量 struct,只取所需字段,其余字段原样透传进临时配置,保持用户设置)。`ocr` 侧 `Config` 不新增字段——混版本安全已由现有 `unknownJSONFields` 机制承担。备选的引用式条目(复用 providers 注册表凭证,`--provider/--model` 现成可用)被用户否决:配置要简单,接受同一 provider 的 key 在文件里出现两遍。

### D3: 子进程编排

参数转发:`ocr_ext` 自带 flag 镜像,覆盖 `ocr review` 的完整 flag 集(tools/rule/repo/from/to/commit/exclude/format/audience/output/concurrency/timeout/max-tools/max-git-procs/max-tokens/max-tokens-budget/background/background-file/effort/no-filter),逐项原样转发;例外只有三处:`--resume` 与 `--preview` 在 `ocr_ext` 层拒绝,`-f sarif` 派发前报错(v1 不支持),`--provider`/`--model` 若用户提供则只转发给主模型子进程。reviewer 子进程额外带 `--config <临时配置>` 且固定 `-f json -o <临时文件>`;主模型子进程两者都不带。

`--config` 的选择必须贯穿 review 路径的全部配置读取点(端点解析 `loadLLMRuntime` 与 `previewMaxTokens` 都调用 `defaultConfigPath()` 后 `LoadAppConfig`,shared.go:86/246;后者同时供 language/telemetry/max_tokens),而非只作用于端点解析——否则同一次运行的端点与语言/遥测会来自两份不同文件。

子进程 JSON 写 `os.TempDir()` 下带前缀的临时文件,合并读完即删。stderr 逐行转发并加来源前缀(如 `[gpt-5]`),父进程读管道转发即可,不触碰 `internal/stdout`。等待全部退出后统一合并。

失败语义:主模型子进程非零退出 → 报错退出(无主结果即无"这次评审");reviewer 非零退出 → 记为警告,其余结果照常合并,exit 0(部分结果仍是有价值的结果,与预算超限的 best-effort 姿态一致)。

备选的共享 token 账本(旧提案 D2 的一部分)随进程形态一并放弃:各子进程预算独立,聚合语义由 `sources` 块的各方用量记录补偿,用户如需总控可在派发前自行分预算。

### D4: 确定性合并(`internal/merge`)

纯函数包,零 LLM 调用,合并永不失败。匹配双条件,缺一不可:位置重叠(多行区间 IoU > 0.6;单行与单行同位置;单行与多行永不匹配)且内容 token 集合相似度 ≥ 0.5。纯位置会吞掉同位置的不同问题(两条单行评论 IoU 恒为 1.0),纯内容会在不同位置误合。组内收敛:正文取 severity 较高者,平手取内容更详尽者;`found_by` 取组内全部来源并集,顺序按来源注册顺序;修复建议与上下文随正文,不跨条拼凑。无法解析行号的发现跳过匹配、原样保留。合并范围覆盖主模型——主模型在合并中没有任何特殊地位,它只是 sources[0]。

备选方案被否决:精确键去重(path+行号+category 完全一致)因 LLM 之间行号常对不齐而几乎去重不了;LLM 合并 pass 每次运行多一次调用 + 一整个模板面;向量相似度引入依赖且不可解释。

### D5: 输出归因

`model.LlmComment` 增加 `FoundBy []string`(omitempty;`internal/model` 是共享包,`ocr` 与 `ocr_ext` 均可 import)。合并 JSON 由 `ocr_ext` 自定义结构输出(不复用 `cmd/opencodereview` 的 `jsonOutput`,跨 main 包不可 import,且 `sources` 本就是新形状):每条发现带 `found_by`;`sources` 块记录每个来源的 provider、model、发现数、token 用量、session_id;不携带顶层 `session_id`/`manifest`/retry report——它们是"一次运行"的语义,合并产物里是假的。终端 text 输出由 `ocr_ext` 自带渲染器输出(简化版:路径+行号+category/severity+正文+`found by:` 行)。单模型输出不含该字段、不加该行(omitempty 保证 `ocr` 侧字节级一致)。

### D6: 构建与发布面

`Makefile`:`build` 与 `BUILD_PLATFORM` 宏每平台产出双产物(`opencodereview` 与 `ocrext`,Windows 为 `.exe`)。`.github/workflows/release.yml` 需要四处同步修改,全部源于硬编码的 `opencodereview-*` glob:Build 步骤加第二个 `go build ./cmd/ocrext`(产物 `ocrext-<os>-<arch>[.exe]`,与第一产物并入同一 upload-artifact);"Generate checksums" 的 `sha256sum opencodereview-*` 补上 `ocrext-*`——install 脚本强制校验,清单缺项会以 "no checksum entry" 失败;"Create GitHub Release" 的 files 与 attest 的 subject-path 各补 `ocrext-*`——否则第二二进制不会被发布、无构建 provenance。npm-publish 按显式 PLATFORMS 表复制资产,不受新增文件影响,v1 无需改动。`install.sh`/`install.ps1` 把"下载单资产 + sha256 校验 + 安装"线性流程循环化,将两者分别以 `ocr` / `ocr_ext` 之名安装到同一目录(版本天然配对;两脚本均已强制校验,install.ps1 逻辑同构)。`.github/workflows/ci.yml` 的冒烟同步覆盖 `ocrext --version` / `--help`。npm 包与 `action.yml` v1 不动。文档:README 五语言同步,`pages/src/content/docs/` 补 `ocr_ext review` 用法与 `reviewers` 配置说明。

## Risks / Trade-offs

- 措辞差异大的同题发现会重复出现 → 确定性相似度的已知盲区,接受:多一条噪音,不丢一条真发现;LLM 合并 pass 是被明确否决的替代。
- 同位置、内容巧合相似但确实不同的两个问题可能被误合 → 相似度阈值(0.5)压低概率,但无 LLM 兜底,接受。
- 无共享预算,总花费上不封顶 → 聚合语义让位于实现简单;`sources` 记录各方用量供诊断,用户可按需分预算。
- 多进程 stderr 交错影响可读性 → 来源前缀缓解;各子进程进度本就交错的本质由并行决定。
- 子进程二进制缺失或版本漂移 → sibling 优先定位(双名) + `ocr version` 配对警告。
- 临时配置文件短暂落盘含凭证 → 0600 权限 + 用后即删;与现有 `api_key_cmd` 辅助等敏感能力同级,进程内存中同样存在,攻击面不扩大。
- `--config` flag 是 `ocr` 唯一改动,理论上是新增行为面 → 默认值不变、不使用即零影响;以 flag 兼容性测试兜底。
- 多一层进程开销(N+1 个子进程、临时文件序列化)→ 相对 LLM 调用耗时可忽略。

## Migration Plan

纯新增,无迁移:旧配置文件不含 `reviewers`,行为与输出不变;不安装 `ocr_ext` 的用户零感知。启用 = 安装 `ocr_ext` 并在 config 写入 `reviewers`。回滚 = 停用 `ocr_ext`(删除二进制)或移除 `reviewers` 字段;`ocr review` 无需回滚(`--config` flag 留存无害,或随分支回滚)。旧同名提案 artifacts 已删除并由本套替代,旧实现从未提交,无代码迁移对象。
