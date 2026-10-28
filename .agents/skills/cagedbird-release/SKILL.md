---
name: cagedbird-release
description: 发布和验证 cagedbird043/sing-box 个人分支及其 GitHub Release、Android APK、Homebrew 和 AUR 分发。用户说“发布 cagedbird”“打一个 sing-box 个人版”“更新 Homebrew/AUR”“ship cagedbird release”或要求候选分支 CI 到 alpha 的发布时使用。涉及推送或公开发布前确认用户已授权对应仓库和分支。
compatibility: Git、gh、Go、Python 3、Arch makepkg/pacman、SSH sjc-edge；macOS/Homebrew 检查按平台可用性执行。
---

# cagedbird 发布流程

## 范围与前置条件

- 源仓库固定为 `cagedbird043/sing-box`，不能发布到 SagerNet。
- `gh workflow/run/release` 命令始终传 `-R cagedbird043/sing-box`。
  `gh api` 不支持 `-R`，使用显式 `repos/cagedbird043/sing-box/...` 端点。
- 主工作树通常为 `../sing-box`，候选工作树为 `../wt-candidate`；检查实际 `git worktree list`。
  只在干净的候选工作树准备分支，保留用户未提交内容；不能为了重跑发布而重置已有修改。
- 本机 Go 命令使用 `GOTOOLCHAIN=go1.25.5`，系统新版 Go 可能触发 linkname 兼容错误。
  CI 按现有工作流工具链。不要修改上游工作流或无关 provider/tailscale 实现。
- `clients/android`、`clients/apple`、`clients/desktop` 在本地工作树保持未初始化，gitlink 不变。
  若创建工作树后复制进来的目录带有失效 `.git`，仅在确认其为多余副本且无用户修改后清空目录。
- 提交信息使用中文，无 AI 署名。纯文档、打包提交带 `[skip ci]`；手动 dispatch 不受其影响。
- 发布前记录 `git rev-parse origin/cagedbird/alpha` 为旧 alpha SHA；强推使用这个明确的租约值。

## 本地验收

按本次变更运行相应测试。race DNS 和配置拉取的基本检查：

```sh
GOTOOLCHAIN=go1.25.5 go test -race ./dns/transport ./option -run Race -count=1
GOTOOLCHAIN=go1.25.5 go build -trimpath -ldflags "$(cat release/LDFLAGS) -s -w" -tags "$(cat release/DEFAULT_BUILD_TAGS_OTHERS)" -o /tmp/sing-box-cagedbird ./cmd/sing-box
sh -n release/config/sing-box-config-pull
shellcheck release/config/sing-box-config-pull
python scripts/ci/test-config-pull.py /tmp/sing-box-cagedbird
systemd-analyze verify release/config/sing-box-config-pull.service release/config/sing-box-config-pull.timer release/config/sing-box.service
```

未安装到系统时，verify 会报告 `/usr/lib/sing-box/sing-box-config-pull` 不存在；
主服务 `/usr/bin/sing-box` 不存在也是环境问题，分开记录，不能当作语法通过。
对 DNS 变更用真实 DoH smoke 和错误配置 check，不只依赖 fake transport 测试。

## 候选分支 CI

```sh
git push origin HEAD:refs/heads/cagedbird/candidate-1.15
gh workflow run cagedbird-core.yml -R cagedbird043/sing-box --ref cagedbird/candidate-1.15
gh workflow run cagedbird-binaries.yml -R cagedbird043/sing-box --ref cagedbird/candidate-1.15
gh workflow run cagedbird-android-apk.yml -R cagedbird043/sing-box --ref cagedbird/candidate-1.15
gh run list -R cagedbird043/sing-box --branch cagedbird/candidate-1.15 --event workflow_dispatch --json databaseId,workflowName,headSha,url,status,conclusion
# 对每个与候选 HEAD 相同的 run：
gh run watch RUN_ID -R cagedbird043/sing-box --exit-status
```

候选分支若经过用户授权的重置，推送使用 `--force-with-lease`，不使用无租约的 force。
只在三个工作流全绿且 headSha 正确后晋升。失败时看对应 job 日志并修复，重新跑受影响检查。

## 晋升与 GitHub Release

```sh
git push --force-with-lease=cagedbird/alpha:OLD_ALPHA_SHA origin NEW_SHA:refs/heads/cagedbird/alpha
gh workflow run cagedbird-release.yml -R cagedbird043/sing-box --ref cagedbird/alpha -f version=auto
```

记录 release run ID 并 `gh run watch ... --exit-status`。
自动版本由 `scripts/ci/resolve-cagedbird-version.sh` 决定：
`<最近上游版本>-cagedbird.<提交 UTC 时间 YYYYmmddHHMMSS>.<短 SHA>`，标签加前缀 `v`。
不要猜测日期或复用旧 tag。可在相同 HEAD 本地运行该脚本得到预期值。

检查 Release tag、提交及 assets：Linux amd64、amd64-v4、arm64，Darwin arm64，
Android arm64/amd64 核心，以及 APK/AAR。读取 API 中各 asset 的 `digest`，不是下载后凭空记录。
下载发布版 linux-amd64，核对 sha256，运行 `sing-box version` 及最小 `race` 配置的 `check`。
本机 GitHub CDN 超时可使用 `ssh sjc-edge 'curl -fsSL <精确 release asset URL>'` 转发下载。

## Homebrew

Release 工作流运行 `scripts/ci/publish-cagedbird-distribution.sh` 自动更新
`cagedbird043/homebrew-tap` 的 `Formula/sing-box-cagedbird.rb`。
同步本地 tap 后核对版本、三个平台 tarball sha256 与 Release digest，并运行 `ruby -c`；
有 brew 时运行可用的 audit，平台不适用或工具不可用时明确说明。

公式生成器是源头，不手改远端公式。检查 `config-pull` resource 的 URL 与 sha256、
`libexec/sing-box-config-pull` 和 `share/sing-box-cagedbird/cn.cagedbird.sing-box-config-pull.plist`。
macOS URL 文件为 `$(brew --prefix)/etc/sing-box/config-url`，目标为同目录 `config.json`；
主服务通过 `sudo brew services start sing-box-cagedbird` 管理，辅助 job 由用户按 README
安装到 `/Library/LaunchDaemons` 并 bootstrap。Linux URL 文件为 `/etc/sing-box/config-url`，
启用 `sing-box-config-pull.timer`。不要在发布机上替用户启用系统服务或填入私密 URL。

## AUR：一个命令

在 fork 工作树执行：

```sh
.agents/skills/cagedbird-release/scripts/update-aur.py v完整发布标签
```

默认 AUR checkout 为 fork 的同级目录 `sing-box-cagedbird-bin`；可用 `AUR_REPO` 指定。
这个命令会更新并推送 AUR，因此只在用户已授权 AUR 发布时运行。

脚本确认 fork/AUR remote、干净 checkout、精确已发布标签，从 Release asset digest 取
amd64/arm64 SHA256，从标签中的 `.fpm_systemd` 自动派生配置文件来源、安装路径及校验和。
版本规则保留 `epoch=1`，`_pkgver` 保持上游 release 版本，`pkgver` 将 `-` 改为 `_`，新版本
`pkgrel=1`。同版本单独修订打包时需要递增 pkgrel，不能冒充新上游版本。

脚本在独立临时目录下载并校验资产，CDN 不可用时走 sjc-edge；执行
`makepkg --verifysource`、`makepkg -f --cleanbuild --noconfirm`，用 `pacman -Qlp` 检查映射
（包括 pull 脚本、service、timer），生成 `.SRCINFO`，仅提交 PKGBUILD/.SRCINFO 并推送 master。
不将下载资产、src/pkg 或构建包提交到 AUR 仓库。任何步骤失败都停止，不继续发布。

## 主工作树同步与交付

确认主工作树仍干净并在 `cagedbird/alpha`，再执行用户授权的最终同步：

```sh
git fetch origin
git reset --hard origin/cagedbird/alpha
```

保留未初始化的子模块状态，不递归初始化或改动 gitlink。
中文简要报告：变更提交 SHA/标题、设计要点、平台路径与启用方式、实际测试/烟测输出、
三个候选 CI 与 Release run URL、Release tag、Homebrew/AUR 提交 SHA、skill 路径。
只报告实际执行过的验证，失败或受环境限制的检查单列。
