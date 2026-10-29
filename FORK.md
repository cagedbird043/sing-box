# cagedbird fork 维护规则

本文件通过 `.omp/config.yml` 的 `contextFiles.extra` 注入会话上下文，与上游规则并列生效；两者冲突时以本文件为准。

## 仓库与分支

- 源仓库固定为 `cagedbird043/sing-box`，不向 SagerNet 推送或发布。remote：`origin` = fork，`upstream` = `SagerNet/sing-box`，`ref1nd` = `reF1nd/sing-box`（仅参考）。
- `cagedbird/alpha` = 一个上游 tag + 按主题组织的线性补丁栈，没有 merge 提交。用 `git log --oneline <基线tag>..cagedbird/alpha` 查看。
- 跟进上游新 tag：用 rebase 把补丁栈搬到新 tag 上，不用 merge。何时跟进由用户决定，不做自动跟进。
- 候选分支 `cagedbird/candidate-*` 用来在晋升 alpha 前跑 CI，内容与将要晋升的 SHA 相同。

## fork 专属路径

只有这些路径属于 fork：

- `FORK.md`、`.omp/`、`.agents/`
- `.github/workflows/cagedbird-*.yml`
- `scripts/ci/`
- `release/config/sing-box-config-pull*`

改上游文件时，改动要小、集中，并落在对应的主题提交里。上游 workflow（`build`、`docker`、`lint`、`linux`、`stale`、`test`）已在仓库设置里停用，文件保持原样，不改也不删。

## 提交与补丁栈

- 提交信息用中文，不带 AI 署名（无 `Co-Authored-By`、无 `Generated with`）。前缀风格不强制统一。
- 提交标题不加 `[skip ci]`。GitHub 只看推送顶端那个提交的标题，而补丁栈的顶端经常是文档主题，加了会让整个候选推送都不构建。纯文档改动靠 workflow 的 paths 过滤跳过。
- 每个主题一个提交：
  - 修改已有主题：`git commit --fixup=<主题提交>`，然后 `git rebase -i --autosquash <基线tag>`。
  - 只有新主题才新开提交。
- 整理补丁栈前后：
  - `git diff <旧HEAD> <新HEAD>` 只能出现有意的改动。
  - 每个提交都要能单独编译，用 `git rebase <基线tag> -x '<下面的构建命令>'` 检查。
- 改写历史前记录旧 SHA。推送一律用带明确旧值的租约，不用无租约的 force：
  `git push --force-with-lease=cagedbird/alpha:<旧SHA> origin HEAD:refs/heads/cagedbird/alpha`。
- 已发布的 tag 不删除、不移动；Homebrew 和 AUR 引用的是 tag。

## 本机构建

```sh
GOTOOLCHAIN=go1.25.5 go build -ldflags "$(cat release/LDFLAGS)" -tags "$(cat release/DEFAULT_BUILD_TAGS_OTHERS)" ./cmd/sing-box
```

- 必须用 `GOTOOLCHAIN=go1.25.5`，系统里更新的 Go 会触发 linkname 兼容错误。
- 必须带 `release/LDFLAGS`，否则链接时报 `runtime.allgs`。
- CI 使用 workflow 里写定的工具链。
- `clients/android`、`clients/apple`、`clients/desktop` 这几个 gitlink 在本地保持未初始化，不递归初始化，也不改 gitlink。

## GitHub 操作

- `gh workflow`、`gh run`、`gh release` 一律带 `-R cagedbird043/sing-box`。
- `gh api` 不支持 `-R`，使用显式端点 `repos/cagedbird043/sing-box/...`。
- 本机直连 GitHub 下载大文件容易被截断，改用 `ssh sjc-edge 'curl -fsSL <url>'` 中转，并核对 sha256。

## 版本号

由 `scripts/ci/resolve-cagedbird-version.sh` 生成：`<最近上游版本>-cagedbird.<提交 UTC 时间 YYYYmmddHHMMSS>.<短 SHA>`，tag 再加前缀 `v`。

- 版本号只由提交决定。同一个 SHA 在候选分支和 alpha 上得到的版本相同，这是复用构建产物的前提。
- 不要手写日期，也不要复用旧 tag。

## CI 与构建产物复用

- 构建步骤只在一处定义：`.github/workflows/cagedbird-build.yml`（`workflow_call`）。Binaries、APK、Release 三个 workflow 都调用它，所以复用的产物和新构建的产物用的是同一套构建步骤。
- 推送到 `cagedbird/candidate-*` 时，Binaries 和 APK 自动构建（带 paths 过滤），也可以手动 dispatch 补跑。Core 在 push 时运行。
- Release 由 `scripts/ci/plan-cagedbird-artifacts.sh` 规划，逐个产物判断，不看 run 的整体结论：
  - 在 Binaries、APK 和以前的 Release run 中按时间从新到旧查找同一 SHA 的产物。找到未过期的，先下载，用 `scripts/ci/verify-cagedbird-artifact.sh` 校验 commit 和 version（APK 还要校验是稳定密钥签名），通过才复用。
  - 只重新构建缺少的产物。Provider core checks 在同一 SHA 已经通过时也跳过。
  - publish 时校验每个产物 `BUILD-INFO.txt` 里的 `commit` 和 `version`；7 个产物缺一个就失败。
- 某个 job 失败：修好后只重跑这个 job（`gh run rerun <id> --failed -R cagedbird043/sing-box`），或者直接发 Release 让它补建。不要整体重跑。

## Android 设备

- Android 17 上 SFA 需要「本地网络」权限（`ACCESS_LOCAL_NETWORK`）。缺了它，系统会静默丢弃 SFA 的局域网收发包，tailscale 只能绕光猫公网 hairpin，同局域网 tailnet 从约 25 MB/s 掉到 0.5 MB/s。
- 开机自启、快捷开关等不经过主界面的启动路径不会弹授权框。新装或重装 SFA 后手动授予：系统设置里给 SFA 打开「附近的设备」，或 `adb shell pm grant io.nekohasekai.sfa android.permission.ACCESS_LOCAL_NETWORK`。授予不需要重启 SFA。
- 检查：`adb shell dumpsys package io.nekohasekai.sfa | grep ACCESS_LOCAL_NETWORK` 应为 `granted=true`。

发布流程见 skill `.agents/skills/cagedbird-release/SKILL.md`。
