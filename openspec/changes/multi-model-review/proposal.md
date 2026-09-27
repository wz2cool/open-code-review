# Proposal

## Why

用户希望用多个模型交叉扫描同一份 diff 以提升发现召回,并要求"一个命令完成所有"。此前同名提案(进程内并发 + 共享 token 账本 + 进度注入,约 2200 行)已被整体推翻删除;本提案以更小的形态重新立项:并行交给操作系统(每个模型一个标准 `ocr review` 子进程),工具只补编排入口、reviewer 端点传递与确定性合并,估约 1000 行(含测试)。

## What Changes

- 新增独立二进制 `ocr_ext`(新 main 包):`ocr_ext review` 读取配置中的追加 reviewer,将主模型与每条 reviewer **一律**派发为标准 `ocr review` 子进程并行执行,全部结束后合并为单一输出。`ocr_ext` 本体零评审流水线代码,纯编排器;flag 集与输出渲染自含(与 `ocr` 的 main 包无共享代码,Go 不允许跨 main 包 import)。
- `ocr review` 新增可选 `--config <path>` flag:指定配置文件路径,默认仍为 `~/.opencodereview/config.json`。这是 `ocr review` 侧唯一的改动;不使用该 flag 时行为与输出与现状完全一致。它是 reviewer 端点传递的载体(`ocr_ext` 为每个 reviewer 生成临时配置)。
- `~/.opencodereview/config.json` 新增可选 `reviewers` 数组:每个条目与 `llm` 段同构(`url`/`auth_token`/`model`/`protocol`/`auth_header`/`extra_body`…),每条自带 baseUrl 与凭证;只有 `ocr_ext` 读它,`ocr review` 不感知该字段。`ocr_ext review` 在 `reviewers` 缺省或为空时于派发前报错(它是显式的多模型入口,不静默降级为单模型)。
- 新增 `internal/merge` 纯函数包:确定性合并(位置重叠 + 内容相似度双条件),零 LLM 调用,合并永不失败;同题收敛为一条,来源取并集。合并范围覆盖主模型与全部 reviewer。
- `model.LlmComment` 增加 `FoundBy []string`(omitempty):合并 JSON 每条发现按来源归因;合并 text 输出每条发现尾部加 `found by:` 行;单模型输出保持字节级不变。
- 合并 JSON 增加 `sources` 块:每个来源(主模型 + 各 reviewer)的 provider/model、发现数、token 用量、session_id,供 agent 追溯。
- 构建与发布面:Makefile 每平台构建双产物(`opencodereview` 与 `ocrext`);`.github/workflows/release.yml` 随之构建、校验、发布第二个平台资产(`ocrext-<os>-<arch>`,其硬编码的 checksum/release/attest glob 均需补齐,否则安装脚本校验失败或资产缺失),`.github/workflows/ci.yml` 冒烟覆盖新二进制;install.sh / install.ps1 以 `ocr` / `ocr_ext` 之名同目录安装;README 五语言与 docs 站点同步补文档。
- 明确不做:无 `ocr_ext scan`(v1 只做 review);SARIF 输出 v1 不支持(`ocr_ext review -f sarif` 于派发前报错,以后经 internal 包增量引入);npm 包与 action.yml 不接入 `ocr_ext`;多模型 + `--resume` / `--preview` 报错;无共享 token 预算(每个子进程各自预算);无 LLM 合并 pass。

## Capabilities

### New Capabilities

- `multi-model-review`: 多模型评审的配置契约(`reviewers` 数组)、reviewer 端点传递(`--config` 临时配置)、子进程编排与失败降级、确定性结果合并、输出归因与来源统计。

### Modified Capabilities

无。单模型路径的需求不变:现有能力(含 review-prompting)描述的是单次 `ocr review` 运行的行为;`ocr review` 仅新增可选 `--config` flag 且不使用时行为不变,多模型只是把同一流程并行执行 N+1 次再合并。

## Impact

- 代码:
  - 新增 `cmd/ocrext`(main 包):自带 review 子集 flag 镜像、`reviewers` 读取与校验、临时配置生成、子进程派发与 stderr 前缀转发、合并调用、最终渲染(JSON/text 自定义实现)。
  - 新增 `internal/merge`:确定性合并纯函数包。
  - `internal/model`:`LlmComment` 增加 `FoundBy` 字段(omitempty)。
  - `cmd/opencodereview`:新增 `--config` flag(默认路径与既有行为不变);`Config` 不新增字段(混版本保护由现有 `unknownJSONFields` 机制承担)。
- 兼容性:`ocr review` 不使用 `--config` 时行为与输出零变化(结构性保证:不读 `reviewers`、不进任何编排分支);config 混版本安全——旧版 `ocr` 读写 config 不丢弃 `reviewers` 字段(现有 `unknownJSONFields` 机制已实际接线);旧配置文件不含 `reviewers`,行为与输出不变。
- 构建与发布:`Makefile` 双产物、`.github/workflows/release.yml` 第二平台资产与 `.github/workflows/ci.yml` 冒烟、`install.sh` / `install.ps1` 同目录同装。
- 文档:`docs/i18n/README.{zh-CN,ja-JP,ko-KR,ru-RU}.md` 同步;`pages/src/content/docs/{en,zh,ja,ko,ru}/` 补 `ocr_ext` 用法与 `reviewers` 配置说明。
- 不改动:评审流水线内部(分组、计划、过滤、对抗性 pass、压缩)、`internal/scan`、工具注册表、`action.yml`、npm 包。
