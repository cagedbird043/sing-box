#!/usr/bin/env python3
"""Build, verify and publish the AUR wrapper for one cagedbird GitHub release."""
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


def main():
    if len(sys.argv) not in (2, 3) or not re.fullmatch(r"v[0-9][0-9A-Za-z.+-]*-cagedbird\.[0-9]+\.[0-9a-f]+", sys.argv[1]):
        sys.exit("Usage: update-aur.py v<version>-cagedbird.<UTC timestamp>.<commit> [pkgrel]")
    if len(sys.argv) == 3 and not re.fullmatch(r"[1-9][0-9]*", sys.argv[2]):
        sys.exit("pkgrel must be a positive integer")
    tag = sys.argv[1]
    pkgrel = sys.argv[2] if len(sys.argv) == 3 else "1"
    version = tag[1:]
    arch_version = version.replace("-", "_")
    origin = run("git", "remote", "get-url", "origin", capture=True)
    if origin not in (f"git@github.com:{FORK}.git", f"https://github.com/{FORK}.git"):
        sys.exit("Run from the cagedbird fork, not upstream")
    aur_origin = run("git", "remote", "get-url", "origin", cwd=AUR, capture=True)
    if aur_origin not in ("ssh://aur@aur.archlinux.org/sing-box-cagedbird-bin.git",
                           "aur@aur.archlinux.org:sing-box-cagedbird-bin.git"):
        sys.exit("AUR_REPO must be the sing-box-cagedbird-bin AUR checkout")
    if run("git", "status", "--porcelain", cwd=AUR, capture=True):
        sys.exit("AUR checkout must be clean")
    run("git", "pull", "--ff-only", cwd=AUR)
    run("git", "fetch", "origin", "tag", tag)
    # gh api does not accept -R: the explicit endpoint pins the fork instead.
    release = json.loads(run("gh", "api", f"repos/{FORK}/releases/tags/{tag}", capture=True))
    if release["tag_name"] != tag or release["draft"]:
        sys.exit("Expected a published release with the exact tag")
    assets = {asset["name"]: asset for asset in release["assets"]}
    manifest = run("git", "show", f"{tag}:.fpm_systemd", capture=True)
    mappings = []
    for line in manifest.splitlines():
        if line.startswith("release/config/") and "=" in line:
            source, target = line.split("=", 1)
            mappings.append((source, target))
    required = {"sing-box-config-pull", "sing-box-config-pull.service", "sing-box-config-pull.timer", "sing-box-update"}
    if not required.issubset({Path(source).name for source, _ in mappings}):
        sys.exit("Release is missing config-pull or update package mappings")
    with tempfile.TemporaryDirectory(prefix="sing-box-aur-") as directory:
        build = Path(directory)
        sums = []
        for source, _ in mappings:
            payload = subprocess.check_output(["git", "show", f"{tag}:{source}"], cwd=ROOT)
            # Versioned local names: AUR helpers keep the build directory between
            # releases, and an unversioned name would reuse the previous file.
            (build / f"{Path(source).name}-{version}").write_bytes(payload)
            sums.append(hashlib.sha256(payload).hexdigest())
        binary_sums = {}
        for arch in ("amd64", "arm64"):
            asset = assets[f"sing-box-cagedbird-linux-{arch}.tar.gz"]
            digest = asset.get("digest", "")
            if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
                sys.exit(f"GitHub release lacks a SHA256 digest for {arch}")
            binary_sums[arch] = digest[7:]
            path = build / f"sing-box-cagedbird-linux-{arch}-{version}.tar.gz"
            download(asset["browser_download_url"], path)
            if hashlib.sha256(path.read_bytes()).hexdigest() != digest[7:]:
                sys.exit(f"Release digest mismatch for {arch}")
        source_lines = []
        install_lines = []
        for source, target in mappings:
            name = Path(source).name
            source_lines.append(f'"{name}-${{_pkgver}}::${{_raw_url}}/{source}"')
            mode = "755" if name in ("sing-box-config-pull", "sing-box-network-recover", "sing-box-update") else "644"
            install_lines.append(f'    install -Dm{mode} "{name}-${{_pkgver}}" "${{pkgdir}}{target}"')
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
depends=('curl')
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
        replacements = {"VERSION": version, "ARCH_VERSION": arch_version, "PKGREL": pkgrel,
                        "SOURCES": "\n        ".join(source_lines),
                        "SUMS": "\n            ".join(f"'{value}'" for value in sums),
                        "AMD64": binary_sums["amd64"], "ARM64": binary_sums["arm64"],
                        "INSTALLS": "\n".join(install_lines)}
        for key, value in replacements.items():
            pkgbuild = pkgbuild.replace(f"@{key}@", value)
        (build / "PKGBUILD").write_text(pkgbuild)
        run("makepkg", "--verifysource", cwd=build)
        run("makepkg", "-f", "--cleanbuild", "--noconfirm", cwd=build)
        packages = run("makepkg", "--packagelist", cwd=build, capture=True).splitlines()
        listing = run("pacman", "-Qlp", packages[0], cwd=build, capture=True)
        for _, target in mappings:
            if target not in listing:
                sys.exit(f"Built package is missing {target}")
        print(listing)
        srcinfo = run("makepkg", "--printsrcinfo", cwd=build, capture=True) + "\n"
        (AUR / "PKGBUILD").write_text(pkgbuild)
        (AUR / ".SRCINFO").write_text(srcinfo)
        run("git", "add", "PKGBUILD", ".SRCINFO", cwd=AUR)
        if not run("git", "diff", "--cached", "--name-only", cwd=AUR, capture=True):
            print("AUR already matches this release")
            return
        message = f"chore(release): 更新至 {version}" if pkgrel == "1" else f"fix(packaging): {version} 打包修订 {pkgrel}"
        run("git", "commit", "-m", f"{message} [skip ci]", cwd=AUR)
        run("git", "push", "origin", "master", cwd=AUR)
        print("AUR commit:", run("git", "rev-parse", "HEAD", cwd=AUR, capture=True))


if __name__ == "__main__":
    main()
