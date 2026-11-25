#!/usr/bin/env python3
"""Build, verify and publish the AUR wrapper for one cagedbird GitHub release."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request

FORK = "cagedbird043/sing-box"
ROOT = Path(__file__).resolve().parents[4]
AUR = Path(os.environ.get("AUR_REPO", ROOT.parent / "sing-box-cagedbird-bin")).resolve()


def run(*args, cwd=ROOT, capture=False):
    result = subprocess.run(args, cwd=cwd, check=True, text=True,
                            stdout=subprocess.PIPE if capture else None)
    return result.stdout.strip() if capture else None


def download(url, destination):
    try:
        with urllib.request.urlopen(url, timeout=30) as response:
            with destination.open("wb") as output:
                shutil.copyfileobj(response, output)
            expected_length = response.headers.get("Content-Length")
            if expected_length is not None and destination.stat().st_size != int(expected_length):
                raise urllib.error.ContentTooShortError("CDN response ended before Content-Length", None)
    except (urllib.error.URLError, TimeoutError):
        print(f"CDN unavailable; downloading {destination.name} through sjc-edge", flush=True)
        with destination.open("wb") as output:
            subprocess.run(["ssh", "sjc-edge", "curl -fsSL -- " + shlex.quote(url)],
                           check=True, stdout=output)


def sha256(path):
    with path.open("rb") as data:
        return hashlib.file_digest(data, "sha256").hexdigest()


def git_file(revision, path):
    return subprocess.check_output(["git", "show", f"{revision}:{path}"], cwd=ROOT)


def render(revision, version, pkgrel, build, binary_sums):
    mappings = []
    sums = []
    source_lines = []
    install_lines = []
    for line in git_file(revision, ".fpm_systemd").decode().splitlines():
        if not line.startswith("release/config/") or "=" not in line:
            continue
        source, dest = line.split("=", 1)
        name = Path(source).name
        mappings.append(dest)
        # makepkg caches sources by filename, so include the release version.
        payload = git_file(revision, source)
        (build / f"{name}-{version}").write_bytes(payload)
        sums.append(hashlib.sha256(payload).hexdigest())
        source_lines.append(f'"{name}-${{_pkgver}}::${{_raw_url}}/{source}"')
        mode = run("git", "ls-tree", revision, "--", source, capture=True).split()[0]
        install_mode = "755" if mode == "100755" else "644"
        install_lines.append(f'    install -Dm{install_mode} "{name}-${{_pkgver}}" "${{pkgdir}}{dest}"')
    if not mappings:
        sys.exit("发布提交的 .fpm_systemd 缺少 release/config 安装映射")
    pkgbuild = '''# Maintainer: cagedbird043 <cagedbird043@gmail.com>

pkgname=sing-box-cagedbird-bin
_pkgname=sing-box
_pkgver=@VERSION@
pkgver=@ARCH_VERSION@
epoch=1
pkgrel=@PKGREL@
pkgdesc='The universal proxy platform with native Clash subscription support (cagedbird binary build)'
arch=('x86_64' 'aarch64')
url='https://github.com/cagedbird043/sing-box'
license=('LicenseRef-sing-box')
provides=('sing-box')
conflicts=('sing-box' 'sing-box-bin' 'sing-box-alpha' 'sing-box-beta' 'sing-box-beta-bin' 'sing-box-git')
backup=('etc/sing-box/config.json')
options=('!debug')
optdepends=('sing-geosite-rule-set: GeoSite rule sets'
            'sing-geoip-rule-set: GeoIP rule sets')

_release_tag="v${_pkgver}"
_release_url="${url}/releases/download/${_release_tag}"
_raw_url="${url}/raw/${_release_tag}"
source=(@SOURCES@)
source_x86_64=("${_pkgname}-cagedbird-linux-amd64-${_pkgver}.tar.gz::${_release_url}/${_pkgname}-cagedbird-linux-amd64.tar.gz")
source_aarch64=("${_pkgname}-cagedbird-linux-arm64-${_pkgver}.tar.gz::${_release_url}/${_pkgname}-cagedbird-linux-arm64.tar.gz")
sha256sums=(@SUMS@)
sha256sums_x86_64=('@AMD64@')
sha256sums_aarch64=('@ARM64@')

package() {
@INSTALLS@

    declare -A arch_map=([x86_64]='amd64' [aarch64]='arm64')
    cd "${srcdir}/${_pkgname}-cagedbird-linux-${arch_map[$CARCH]}"
    install -Dm755 "${_pkgname}" -t "${pkgdir}/usr/bin"
    install -Dm644 LICENSE "${pkgdir}/usr/share/licenses/${_pkgname}/LICENSE"
    install -Dm644 <("./${_pkgname}" completion bash) "${pkgdir}/usr/share/bash-completion/completions/${_pkgname}.bash"
    install -Dm644 <("./${_pkgname}" completion fish) "${pkgdir}/usr/share/fish/vendor_completions.d/${_pkgname}.fish"
    install -Dm644 <("./${_pkgname}" completion zsh) "${pkgdir}/usr/share/zsh/site-functions/_${_pkgname}"
}
'''
    replacements = {"VERSION": version, "ARCH_VERSION": version.replace("-", "_"), "PKGREL": pkgrel,
                    "SOURCES": "\n        ".join(source_lines),
                    "SUMS": "\n            ".join(f"'{value}'" for value in sums),
                    "AMD64": binary_sums["amd64"], "ARM64": binary_sums["arm64"],
                    "INSTALLS": "\n".join(install_lines)}
    for key, value in replacements.items():
        pkgbuild = pkgbuild.replace(f"@{key}@", value)
    (build / "PKGBUILD").write_text(pkgbuild)
    run("bash", "-n", "PKGBUILD", cwd=build)
    return mappings


def main():
    parser = argparse.ArgumentParser(description="生成、构建并发布 cagedbird AUR 包")
    parser.add_argument("tag")
    parser.add_argument("pkgrel", nargs="?", default="1")
    parser.add_argument("--build-dir", type=Path, help="使用本机发布产物；否则从 GitHub 下载")
    parser.add_argument("--dry-run", action="store_true", help="离线生成并验证 PKGBUILD 到产物目录")
    parser.add_argument("--commit", help="试运行时使用的未发布提交（默认 HEAD）")
    args = parser.parse_args()
    if not re.fullmatch(r"v[0-9][0-9A-Za-z.+-]*-cagedbird\.[0-9]{14}\.[0-9a-f]+", args.tag):
        parser.error("需要完整的 cagedbird 发布标签")
    if not re.fullmatch(r"[1-9][0-9]*", args.pkgrel):
        parser.error("pkgrel 必须是正整数")
    if (args.dry_run and not args.build_dir) or (args.commit and not args.dry_run):
        parser.error("--dry-run 要求 --build-dir；--commit 仅用于 --dry-run")

    tag = args.tag
    version = tag[1:]
    local = args.build_dir.resolve() if args.build_dir else None
    revision = run("git", "rev-parse", "--verify", f"{args.commit or 'HEAD'}^{{commit}}", capture=True)
    release_assets = {}
    if not args.dry_run:
        origin = run("git", "remote", "get-url", "origin", capture=True)
        if origin not in (f"git@github.com:{FORK}.git", f"https://github.com/{FORK}.git"):
            sys.exit("请从 cagedbird fork 运行")
        aur_urls = ("ssh://aur@aur.archlinux.org/sing-box-cagedbird-bin.git",
                    "aur@aur.archlinux.org:sing-box-cagedbird-bin.git")
        if (run("git", "remote", "get-url", "--all", "origin", cwd=AUR, capture=True) not in aur_urls
                or run("git", "remote", "get-url", "--push", "--all", "origin", cwd=AUR, capture=True) not in aur_urls):
            sys.exit("AUR_REPO 必须指向 sing-box-cagedbird-bin AUR 工作树")
        if (run("git", "status", "--porcelain", cwd=AUR, capture=True)
                or run("git", "branch", "--show-current", cwd=AUR, capture=True) != "master"):
            sys.exit("AUR 工作树必须处于干净的 master")
        run("git", "fetch", "origin", "tag", tag)
        revision = run("git", "rev-parse", "--verify", f"refs/tags/{tag}^{{commit}}", capture=True)
        release = json.loads(run("gh", "api", f"repos/{FORK}/releases/tags/{tag}", capture=True))
        if release["tag_name"] != tag or release["draft"]:
            sys.exit("未找到与标签完全匹配的 GitHub Release")
        release_assets = {asset["name"]: asset for asset in release["assets"]}

    with tempfile.TemporaryDirectory(prefix="sing-box-aur-") as directory:
        build = Path(directory)
        sums = {}
        for arch in ("amd64", "arm64"):
            base = f"sing-box-cagedbird-linux-{arch}"
            filename = f"{base}.tar.gz"
            path = build / f"{base}-{version}.tar.gz"
            if args.dry_run:
                sums[arch] = sha256(local / filename)
                continue
            asset = release_assets[filename]
            digest = asset.get("digest", "")
            if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
                sys.exit(f"GitHub Release 缺少 {arch} SHA-256")
            if local:
                shutil.copyfile(local / filename, path)
            else:
                download(asset["browser_download_url"], path)
            sums[arch] = sha256(path)
            if sums[arch] != digest[7:]:
                sys.exit(f"{arch} 资产摘要与 GitHub Release 不符")
        mappings = render(revision, version, args.pkgrel, build, sums)
        pkgbuild = build / "PKGBUILD"
        if args.dry_run:
            shutil.copyfile(pkgbuild, local / "PKGBUILD")
            print("PKGBUILD: bash -n 通过")
            return

        env = os.environ | {"SRCDEST": str(build), "PKGDEST": str(build)}
        for command in (("makepkg", "--verifysource"),
                        ("makepkg", "-f", "--cleanbuild", "--noconfirm")):
            subprocess.run(command, cwd=build, env=env, check=True)
        packages = subprocess.check_output(("makepkg", "--packagelist"), cwd=build,
                                           env=env, text=True).splitlines()
        listing = run("pacman", "-Qlp", packages[0], cwd=build, capture=True)
        installed = {line.split(maxsplit=1)[1] for line in listing.splitlines()}
        for dest in mappings:
            if dest not in installed:
                sys.exit(f"构建包缺少 {dest}")
        print(listing)
        srcinfo = run("makepkg", "--printsrcinfo", cwd=build, capture=True) + "\n"
        if (run("git", "status", "--porcelain", cwd=AUR, capture=True)
                or run("git", "branch", "--show-current", cwd=AUR, capture=True) != "master"):
            sys.exit("打包期间 AUR 工作树已变更")
        run("git", "pull", "--ff-only", "origin", "master", cwd=AUR)
        if run("git", "rev-parse", "HEAD", cwd=AUR, capture=True) != run(
                "git", "rev-parse", "FETCH_HEAD", cwd=AUR, capture=True):
            sys.exit("AUR 工作树有未推送的提交")
        shutil.copyfile(pkgbuild, AUR / "PKGBUILD")
        (AUR / ".SRCINFO").write_text(srcinfo)
        files = ("PKGBUILD", ".SRCINFO")
        run("git", "diff", "--check", cwd=AUR)
        run("git", "add", "--", *files, cwd=AUR)
        if not run("git", "diff", "--cached", "--name-only", capture=True, cwd=AUR):
            print("AUR 已是该版本")
            return
        message = f"更新至 {version}" if args.pkgrel == "1" else f"修订 {version} 打包（{args.pkgrel}）"
        run("git", "commit", "-m", message, "--", *files, cwd=AUR)
        run("git", "push", "--no-follow-tags", "origin", "HEAD:refs/heads/master", cwd=AUR)
        print("AUR 提交：", run("git", "rev-parse", "HEAD", cwd=AUR, capture=True))


if __name__ == "__main__":
    main()
