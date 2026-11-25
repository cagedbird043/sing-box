#!/usr/bin/env python3
"""Package verified local cagedbird release artifacts for AUR."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
AUR = "ssh://aur@aur.archlinux.org/sing-box-cagedbird-bin.git"


def run(*args, cwd=ROOT, capture=False):
    result = subprocess.run(args, cwd=cwd, check=True, text=True,
                            stdout=subprocess.PIPE if capture else None)
    return result.stdout.strip() if capture else None


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
    parser = argparse.ArgumentParser(description="使用本机发布产物生成 cagedbird AUR 包并推送到 AUR")
    parser.add_argument("tag")
    parser.add_argument("pkgrel", nargs="?", default="1")
    parser.add_argument("--build-dir", type=Path, required=True, help="发布产物与 .assets.json 所在目录")
    parser.add_argument("--dry-run", action="store_true", help="离线生成并验证 PKGBUILD 到产物目录")
    parser.add_argument("--commit", required=True, help="产物对应的提交")
    args = parser.parse_args()
    if not re.fullmatch(r"v[0-9][0-9A-Za-z.+-]*-cagedbird\.[0-9]{14}\.[0-9a-f]+", args.tag):
        parser.error("需要完整的 cagedbird 发布标签")
    if not re.fullmatch(r"[1-9][0-9]*", args.pkgrel):
        parser.error("pkgrel 必须是正整数")
    tag = args.tag
    version = tag[1:]
    local = args.build_dir.resolve()
    revision = run("git", "rev-parse", "--verify", f"{args.commit}^{{commit}}", capture=True)
    if not version.endswith(f".{revision[:9]}"):
        sys.exit("发布标签不匹配提交")
    assets = json.loads((local / ".assets.json").read_text())

    with tempfile.TemporaryDirectory(prefix="sing-box-aur-") as directory:
        build = Path(directory)
        sums = {}
        for arch in ("amd64", "arm64"):
            base = f"sing-box-cagedbird-linux-{arch}"
            filename = f"{base}.tar.gz"
            path = local / filename
            if not args.dry_run:
                path = Path(shutil.copyfile(path, build / f"{base}-{version}.tar.gz"))
            sums[arch] = sha256(path)
            if sums[arch] != assets[filename]:
                sys.exit(f"{filename} 摘要与构建清单不符")
        mappings = render(revision, version, args.pkgrel, build, sums)
        pkgbuild = build / "PKGBUILD"
        if args.dry_run:
            shutil.copyfile(pkgbuild, local / "PKGBUILD")
            print("PKGBUILD: bash -n 通过")
            return

        env = os.environ | {"SRCDEST": str(build), "PKGDEST": str(build)}
        subprocess.run(("makepkg", "-f", "--cleanbuild", "--noconfirm"), cwd=build, env=env, check=True)
        packages = subprocess.check_output(("makepkg", "--packagelist"), cwd=build,
                                           env=env, text=True).splitlines()
        listing = run("pacman", "-Qlp", packages[0], cwd=build, capture=True)
        installed = {line.split(maxsplit=1)[1] for line in listing.splitlines()}
        for dest in mappings:
            if dest not in installed:
                sys.exit(f"构建包缺少 {dest}")
        print(listing)
        srcinfo = run("makepkg", "--printsrcinfo", cwd=build, capture=True) + "\n"
        aur = build / "aur"
        run("git", "clone", "--branch", "master", AUR, aur)
        if run("git", "remote", "get-url", "--push", "--all", "origin", cwd=aur, capture=True) != AUR:
            sys.exit("AUR 推送地址与 sing-box-cagedbird-bin 不匹配")
        shutil.copyfile(pkgbuild, aur / "PKGBUILD")
        (aur / ".SRCINFO").write_text(srcinfo)
        files = ("PKGBUILD", ".SRCINFO")
        run("git", "diff", "--check", cwd=aur)
        run("git", "add", "--", *files, cwd=aur)
        if not run("git", "diff", "--cached", "--name-only", capture=True, cwd=aur):
            print("AUR 已是该版本")
            return
        message = f"更新至 {version}" if args.pkgrel == "1" else f"修订 {version} 打包（{args.pkgrel}）"
        run("git", "commit", "-m", message, "--", *files, cwd=aur)
        run("git", "push", "--no-follow-tags", "origin", "HEAD:refs/heads/master", cwd=aur)
        print("AUR 提交：", run("git", "rev-parse", "HEAD", cwd=aur, capture=True))


if __name__ == "__main__":
    main()
