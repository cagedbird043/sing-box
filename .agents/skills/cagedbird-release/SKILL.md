---
name: cagedbird-release
description: 发布和验证 cagedbird043/sing-box 个人版及其 GitHub Release、Android APK、Homebrew 和 AUR 分发。用户要求发布 cagedbird、打个人版或更新 Homebrew/AUR 时使用。推送或公开发布前确认各目标已获授权。
compatibility: Arch Linux、Git、gh、Python 3、Ruby、Go、makepkg/pacman、Android SDK/NDK、SSH mac。
---

# cagedbird 发布流程

按 `FORK.md` 整理并提交补丁栈。主工作树有用户未提交的改动时，不为发布而 stash、重置或丢弃。
确认用户已授权向 alpha 推送后，先 fetch 核对远端未出现别人的提交，再使用明确租约：

```sh
git fetch origin cagedbird/alpha
old_alpha=$(git rev-parse origin/cagedbird/alpha)
git push --force-with-lease="cagedbird/alpha:$old_alpha" origin HEAD:refs/heads/cagedbird/alpha
scripts/cagedbird/release
scripts/cagedbird/release publish --dry-run
```

正式执行 `scripts/cagedbird/release publish` 前，确认 GitHub Release、Homebrew tap 和 AUR 均已获授权。
默认 AUR 工作树为 fork 同级目录 `sing-box-cagedbird-bin`，可用 `AUR_REPO` 指定；发布使用本机账户与 SSH 凭据。

用户另行授权本机安装时，用 `paru -S sing-box-cagedbird-bin` 从 AUR 安装，再用 `sing-box version` 核对版本。
不替用户启动、重启或重新配置现有系统服务。
