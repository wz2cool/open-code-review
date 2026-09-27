# Tasks

## 1. 合并核心(`internal/merge`)

- [x] 1.1 创建 `internal/merge` 包:位置重叠判定(多行区间 IoU > 0.6;单行与单行同位置;单行与多行永不匹配),表驱动单测覆盖阈值两侧边界;新文件跑 `make license-add` 补 SPDX 头。验证:`make test` 全绿
- [x] 1.2 实现内容 token 集合相似度(阈值 0.5)与双条件分组,单测覆盖"同位置异内容保留"与"同内容异位置保留"。验证:`make test` 全绿
- [x] 1.3 实现同组收敛(severity 高者胜、平手取更详尽、修复建议与上下文随正文、来源并集按注册顺序)与无行号发现原样保留;空输入、单路输入、全部无行号输入均产出可用结果。验证:`make test` 全绿,`make coverage` 该包达标

## 2. 模型字段与 `--config` flag

- [x] 2.1 `internal/model` 的 `LlmComment` 增加 `FoundBy []string`(omitempty),补 JSON 序列化单测(空值时字段不出现,单模型输出字节级不变)。验证:`make test` 全绿
- [x] 2.2 `ocr review` 新增可选 `--config <path>` flag:默认路径与现状一致;该路径须贯穿 review 路径的全部配置读取点(端点解析 `loadLLMRuntime` 与 `previewMaxTokens` 的 `LoadAppConfig`,后者还供 language/telemetry/max_tokens),不能只作用于端点解析;单测覆盖带自定义配置的运行读取该路径、不带时行为不变。验证:`make test` 全绿,`ocr review --config` 冒烟读取指定文件

## 3. `ocr_ext` 编排(`cmd/ocrext`)

- [x] 3.1 新建 main 包 `cmd/ocrext`:`ocr_ext review` 命令骨架,自带 flag 镜像,覆盖 `ocr review` 完整 flag 集(tools/rule/repo/from/to/commit/exclude/format/audience/output/并发/token 预算/background/background-file/effort/no-filter);`--resume`、`--preview` 与 `-f sarif` 在派发前报错;跑 `make license-add`。验证:`go build ./cmd/ocrext` 成功,三类组合报错信息可读
- [x] 3.2 实现 `reviewers` 读取与校验(轻量 struct 自解析,不经 `cmd/opencodereview` 的 `Config`):空数组或缺失报错并提示用 `ocr review`;缺 `url`/`model` 的条目在任何派发前报错并指明条目;与主模型端点+模型相同的条目幂等丢弃(主端点经 `llm.ResolveEndpoint` 取得后比较)。验证:单测覆盖各分支
- [x] 3.3 实现临时配置生成:用户配置拷贝 + `llm` 段替换为 reviewer 条目 + 清除 provider 选择;权限 0600、运行结束即删;未知字段原样透传。验证:单测断言生成内容、权限与删除
- [x] 3.4 实现子进程派发:`ocr` 二进制定位(同目录 `ocr` → 同目录 `opencodereview` → PATH,找不到报错)、`ocr version` 配对警告、N+1 路并发派发(主模型不带 `--config` 与 `--provider`/`--model`;reviewer 带 `--config <临时配置>`、固定 `-f json -o <临时文件>`,其余 flag 原样转发;`ocr_ext` 自身 `--provider`/`--model` 只转发给主模型子进程)。验证:以 fake 二进制脚本做单测,覆盖定位与转发
- [x] 3.5 实现 stderr 逐行来源前缀转发与临时文件读取后清理。验证:单测断言行前缀;冒烟观察无残留临时文件
- [x] 3.6 实现失败语义:reviewer 子进程非零退出记为警告且退出码 0;主模型子进程非零退出报错退出。验证:fake 子进程注入失败的单测

## 4. 合并输出

- [x] 4.1 组装合并 JSON(`ocr_ext` 自定义结构):每条发现注入 `found_by`(来源并集、注册顺序),`sources` 块携带各来源 provider/model/发现数/token 用量/会话标识;不携带顶层 session_id、manifest、retry report。验证:golden 文件单测
- [x] 4.2 text 渲染(`ocr_ext` 自带渲染器,简化版):路径+行号+category/severity+正文,多来源发现尾部追加 `found by:` 行。验证:golden 文件单测

## 5. 构建与安装

- [x] 5.1 Makefile:`build` 与 `BUILD_PLATFORM` 宏每平台产出 `opencodereview` 与 `ocrext` 双产物。验证:`make build` 产出两个可执行文件
- [x] 5.2 `install.sh` 与 `install.ps1` 以 `ocr` / `ocr_ext` 之名同目录安装两者。验证:本地执行安装脚本后两文件到位且可运行
- [x] 5.3 `.github/workflows/release.yml` 四处同步:Build 步骤加第二个 `go build ./cmd/ocrext`;`Generate checksums` 的 `sha256sum opencodereview-*` 补 `ocrext-*`(否则安装脚本报 no checksum entry);`Create GitHub Release` 的 files 与 attest 的 subject-path 各补 `ocrext-*`(否则不发布、无 provenance);`.github/workflows/ci.yml` 冒烟覆盖 `ocrext --version` / `--help`。验证:CI 全绿,release 资产含两二进制,`sha256sum.txt` 含两者校验项,安装脚本可完整走通 `ocr_ext` 下载+校验+安装

## 6. 端到端验证与交付文档

- [x] 6.1 端到端冒烟:以两个模型配置并行运行 `ocr_ext review`,验证并发互不等待、stderr 前缀、reviewer 子进程端点确为条目 url(检查子进程输出 llm 身份)、合并输出来源归因与 `sources` 统计、各来源独立 session(`ocr session list` 可见)、reviewer 失败降级路径、临时配置与临时文件无残留。验证:冒烟结论记录在 PR 描述
- [x] 6.2 交付文档:README 五语言同步(`docs/i18n/README.{zh-CN,ja-JP,ko-KR,ru-RU}.md`),`pages/src/content/docs/` 各语言补 `ocr_ext review` 用法与 `reviewers` 配置说明。验证:文档审阅通过,五语言齐全
- [x] 6.3 最终闸门:`make check`、`make test`、`make coverage`(阈值达标)全绿;`git add --renormalize .` 后无行尾差异(新文件均为 LF)。验证:命令输出全绿
