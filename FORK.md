# cagedbird fork 维护规则

本文件通过 `.omp/config.yml` 的 `contextFiles.extra` 注入会话上下文，与上游规则并列生效；两者冲突时以本文件为准。

## 仓库与分支

- 源仓库固定为 `cagedbird043/sing-box`，不向 SagerNet 推送或发布。remote：`origin` = fork，`upstream` = `SagerNet/sing-box`，`ref1nd` = `reF1nd/sing-box`（仅参考）。
- `cagedbird/alpha` = 一个上游 tag + 按主题组织的线性补丁栈，没有 merge 提交。用 `git log --oneline <基线tag>..cagedbird/alpha` 查看。
- 跟进上游新 tag：用 rebase 把补丁栈搬到新 tag 上，不用 merge。何时跟进由用户决定，不做自动跟进。

## fork 专属路径

只有这些路径属于 fork：

- `FORK.md`、`.omp/`、`.agents/`
- `scripts/cagedbird/`

改上游文件时，改动要小、集中，并落在对应的主题提交里。上游 workflow 已在仓库设置里停用，文件保持原样，不改也不删；fork 不再有自己的 CI。

## 提交与补丁栈

- 提交信息用中文，不带 AI 署名（无 `Co-Authored-By`、无 `Generated with`）。前缀风格不强制统一。
- 平时迭代一律在栈顶新开提交，不 fixup、不改写历史，中间状态可以逐个推送和回看。
- 跟进上游新 tag 变基时，再按主题把迭代提交合并进各自的主题提交（`git rebase -i <基线tag>` 里 squash/fixup），整理成每个主题一个提交。
- `cmd/sing-box/cmd_api_provider*.go`、`cmd_service*.go`、`cmd_update.go`、`config_sync*.go` 是 fork 有意新增的上游路径。
- 整理补丁栈前后：
  - `git diff <旧HEAD> <新HEAD>` 只能出现有意的改动。
  - 每个提交都要能单独编译，用 `git rebase <基线tag> -x '<下面的构建命令>'` 检查。
- 改写历史前记录旧 SHA。推送一律用带明确旧值的租约，不用无租约的 force：
  `git push --force-with-lease=cagedbird/alpha:<旧SHA> origin HEAD:refs/heads/cagedbird/alpha`。
- 已发布的 tag 不删除、不移动；Homebrew 和 AUR 引用的是 tag。

## 本机构建

```sh
GOTOOLCHAIN=go1.26.6 go build -ldflags "$(cat release/LDFLAGS)" -tags "$(cat release/DEFAULT_BUILD_TAGS_OTHERS)" ./cmd/sing-box
```

- 发布构建使用 Go 1.26.6；必须带 `release/LDFLAGS`，否则链接时报 `runtime.allgs`。
- 日常工作树中的 `clients/android`、`clients/apple`、`clients/desktop` 保持未初始化，不改 gitlink。发布工具只在临时检出中初始化 Android。

## GitHub 操作

- `gh release` 一律带 `-R cagedbird043/sing-box`。
- `gh api` 不支持 `-R`，使用显式端点 `repos/cagedbird043/sing-box/...`。

## 本地发布

工作站入口是 `scripts/cagedbird/release`。版本为 `<最近上游版本>-cagedbird.<提交 UTC 时间 YYYYmmddHHMMSS>.<短 SHA>`，发布标签在前面加 `v`。
构建只读取精确提交，全部平台都在工作站构建，不依赖任何指定主机、SSH 别名或个人工作树。macOS 产物不包含依赖 CGO 的可选功能。GitHub、Homebrew 和 AUR 的写入必须有明确授权。

## 系统服务

以管理员权限运行 `sing-box service install --config-url <URL>`。先用原服务管理器停掉正在运行的旧服务；新服务默认名为 `sing-box-managed`。
二进制、配置和数据目录须由管理员管理。macOS 的 Homebrew 前缀如果可被普通用户写入，先把二进制部署到管理员管理的固定目录，再安装服务；升级后同步更新该二进制。

新配置校验通过后会重启 `sing-box run`，连接可能短暂中断；`sing-box update` 可立即拉取并刷新订阅。

## Android 设备

- Android 17 上 SFA 需要「本地网络」权限（`ACCESS_LOCAL_NETWORK`）。缺了它，系统会静默丢弃 SFA 的局域网收发包，tailscale 只能绕光猫公网 hairpin，同局域网 tailnet 从约 25 MB/s 掉到 0.5 MB/s。
- 开机自启、快捷开关等不经过主界面的启动路径不会弹授权框。新装或重装 SFA 后手动授予：系统设置里给 SFA 打开「附近的设备」，或 `adb shell pm grant io.nekohasekai.sfa android.permission.ACCESS_LOCAL_NETWORK`。授予不需要重启 SFA。
- 检查：`adb shell dumpsys package io.nekohasekai.sfa | grep ACCESS_LOCAL_NETWORK` 应为 `granted=true`。

发布流程见 skill `.agents/skills/cagedbird-release/SKILL.md`。
