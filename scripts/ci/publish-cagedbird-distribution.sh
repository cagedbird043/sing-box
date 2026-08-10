#!/usr/bin/env bash
set -euo pipefail

: "${RELEASE_VERSION:?RELEASE_VERSION is required}"

SOURCE_REPO="${SOURCE_REPO:-cagedbird043/sing-box}"
SOURCE_URL="${SOURCE_URL:-https://github.com/${SOURCE_REPO}}"
AUR_REPO="${AUR_REPO:-ssh://aur@aur.archlinux.org/sing-box-cagedbird-bin.git}"
HOMEBREW_TAP_REPO="${HOMEBREW_TAP_REPO:-git@github.com:cagedbird043/homebrew-tap.git}"
DISTRIBUTION_DRY_RUN="${DISTRIBUTION_DRY_RUN:-0}"
DISTRIBUTION_SSH_PRIVATE_KEY="${DISTRIBUTION_SSH_PRIVATE_KEY:-${CAGEDBIRD_DISTRIBUTION_SSH_PRIVATE_KEY:-}}"
GIT_AUTHOR_NAME="${GIT_AUTHOR_NAME:-cagedbird release bot}"
GIT_AUTHOR_EMAIL="${GIT_AUTHOR_EMAIL:-cagedbird043@users.noreply.github.com}"
GIT_COMMITTER_NAME="${GIT_COMMITTER_NAME:-${GIT_AUTHOR_NAME}}"
GIT_COMMITTER_EMAIL="${GIT_COMMITTER_EMAIL:-${GIT_AUTHOR_EMAIL}}"

case "${DISTRIBUTION_DRY_RUN,,}" in
  1|true|yes|y|on) dry_run=1 ;;
  *) dry_run=0 ;;
esac

tag="v${RELEASE_VERSION}"
release_url="${SOURCE_URL}/releases/download/${tag}"
raw_url="${SOURCE_URL}/raw/${tag}/release/config"
workdir="$(mktemp -d)"
ssh_key_path=""

cleanup() {
  rm -rf "${workdir}"
  if [[ -n "${ssh_key_path}" ]]; then
    rm -f "${ssh_key_path}"
  fi
}
trap cleanup EXIT

setup_ssh() {
  mkdir -p "${HOME}/.ssh"
  chmod 700 "${HOME}/.ssh"
  ssh-keyscan github.com aur.archlinux.org >> "${HOME}/.ssh/known_hosts" 2>/dev/null
  chmod 600 "${HOME}/.ssh/known_hosts"

  if [[ -n "${DISTRIBUTION_SSH_PRIVATE_KEY}" ]]; then
    ssh_key_path="${workdir}/distribution_key"
    printf '%s\n' "${DISTRIBUTION_SSH_PRIVATE_KEY}" > "${ssh_key_path}"
    chmod 600 "${ssh_key_path}"
    export GIT_SSH_COMMAND="ssh -i ${ssh_key_path} -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes"
  else
    export GIT_SSH_COMMAND="ssh -o StrictHostKeyChecking=yes"
  fi
}

sha256_url() {
  local url="$1"
  local output="$2"
  curl -LfsS --retry 3 --retry-delay 2 -o "${output}" "${url}"
  sha256sum "${output}" | awk '{print $1}'
}

push_or_note() {
  local repo_dir="$1"
  local remote="$2"
  local branch="$3"
  if (( dry_run )); then
    echo "[dry-run] would push ${repo_dir} to ${remote} ${branch}"
  else
    git -C "${repo_dir}" push "${remote}" "${branch}"
  fi
}

commit_if_changed() {
  local repo_dir="$1"
  shift
  if git -C "${repo_dir}" diff --quiet --cached && git -C "${repo_dir}" diff --quiet; then
    echo "No changes in ${repo_dir}; already at ${RELEASE_VERSION}."
    return 1
  fi
  git -C "${repo_dir}" add "$@"
  if git -C "${repo_dir}" diff --cached --quiet; then
    echo "No staged changes in ${repo_dir}; already at ${RELEASE_VERSION}."
    return 1
  fi
  return 0
}

write_aur_srcinfo() {
  local path="$1"
  cat > "${path}" <<SRCINFO
pkgbase = sing-box-cagedbird-bin
	pkgdesc = The universal proxy platform with native Clash subscription support (cagedbird binary build)
	pkgver = ${AUR_PKGVER_UNDERSCORE}
	pkgrel = 1
	epoch = ${AUR_EPOCH}
	url = ${SOURCE_URL}
	arch = x86_64
	arch = aarch64
	license = LicenseRef-sing-box
	optdepends = sing-geosite-rule-set: GeoSite rule sets
	optdepends = sing-geoip-rule-set: GeoIP rule sets
	provides = sing-box
	conflicts = sing-box
	conflicts = sing-box-bin
	conflicts = sing-box-alpha
	conflicts = sing-box-beta
	conflicts = sing-box-beta-bin
	conflicts = sing-box-git
	options = !debug
	backup = etc/sing-box/config.json
	source = sing-box.service::${raw_url}/sing-box.service
	source = sing-box@.service::${raw_url}/sing-box@.service
	source = sing-box-network-recover::${raw_url}/sing-box-network-recover
	source = sing-box.sysusers::${raw_url}/sing-box.sysusers
	source = sing-box.rules::${raw_url}/sing-box.rules
	source = sing-box-split-dns.xml::${raw_url}/sing-box-split-dns.xml
	source = config.json::${raw_url}/config.json
	sha256sums = ${SHA_SERVICE}
	sha256sums = ${SHA_SERVICE_AT}
	sha256sums = ${SHA_RECOVER}
	sha256sums = ${SHA_SYSUSERS}
	sha256sums = ${SHA_RULES}
	sha256sums = ${SHA_SPLIT_DNS}
	sha256sums = ${SHA_CONFIG}
	source_x86_64 = sing-box-cagedbird-linux-amd64-${RELEASE_VERSION}.tar.gz::${release_url}/sing-box-cagedbird-linux-amd64.tar.gz
	sha256sums_x86_64 = ${SHA_LINUX_AMD64}
	source_aarch64 = sing-box-cagedbird-linux-arm64-${RELEASE_VERSION}.tar.gz::${release_url}/sing-box-cagedbird-linux-arm64.tar.gz
	sha256sums_aarch64 = ${SHA_LINUX_ARM64}

pkgname = sing-box-cagedbird-bin
SRCINFO
}

update_aur() {
  local repo_dir="${workdir}/aur"
  git clone "${AUR_REPO}" "${repo_dir}"
  git -C "${repo_dir}" config user.name "${GIT_AUTHOR_NAME}"
  git -C "${repo_dir}" config user.email "${GIT_AUTHOR_EMAIL}"

  python3 - "${repo_dir}/PKGBUILD" <<'PY'
import os
import re
import sys
from pathlib import Path
path = Path(sys.argv[1])
text = path.read_text()
text = re.sub(r'^_pkgver=.*$', f'_pkgver={os.environ["RELEASE_VERSION"]}', text, flags=re.M)
text = re.sub(r'^pkgver=.*$', f'pkgver={os.environ["AUR_PKGVER_UNDERSCORE"]}', text, flags=re.M)
if re.search(r'^epoch=.*$', text, flags=re.M):
    text = re.sub(r'^epoch=.*$', f'epoch={os.environ["AUR_EPOCH"]}', text, flags=re.M)
else:
    text = re.sub(
        r'^(pkgver=.*)$',
        rf'\1\nepoch={os.environ["AUR_EPOCH"]}',
        text,
        count=1,
        flags=re.M,
    )
text = re.sub(
    r"source=\([\s\S]*?\n\)",
    "source=(\"${_pkgname}.service::${_raw_url}/release/config/sing-box.service\"\n" +
    "        \"${_pkgname}@.service::${_raw_url}/release/config/sing-box@.service\"\n" +
    "        \"${_pkgname}-network-recover::${_raw_url}/release/config/sing-box-network-recover\"\n" +
    "        \"${_pkgname}.sysusers::${_raw_url}/release/config/sing-box.sysusers\"\n" +
    "        \"${_pkgname}.rules::${_raw_url}/release/config/sing-box.rules\"\n" +
    "        \"${_pkgname}-split-dns.xml::${_raw_url}/release/config/sing-box-split-dns.xml\"\n" +
    "        \"config.json::${_raw_url}/release/config/config.json\")",
    text,
    count=1,
)
text = re.sub(
    r"sha256sums=\([\s\S]*?\n\)",
    "sha256sums=(" +
    f"'{os.environ['SHA_SERVICE']}'\n" +
    f"            '{os.environ['SHA_SERVICE_AT']}'\n" +
    f"            '{os.environ['SHA_RECOVER']}'\n" +
    f"            '{os.environ['SHA_SYSUSERS']}'\n" +
    f"            '{os.environ['SHA_RULES']}'\n" +
    f"            '{os.environ['SHA_SPLIT_DNS']}'\n" +
    f"            '{os.environ['SHA_CONFIG']}')",
    text,
    count=1,
)
text = re.sub(r"sha256sums_x86_64=\('[0-9a-f]+'\)", f"sha256sums_x86_64=('{os.environ['SHA_LINUX_AMD64']}')", text)
text = re.sub(r"sha256sums_aarch64=\('[0-9a-f]+'\)", f"sha256sums_aarch64=('{os.environ['SHA_LINUX_ARM64']}')", text)
path.write_text(text)
PY
  write_aur_srcinfo "${repo_dir}/.SRCINFO"

  git -C "${repo_dir}" diff --check
  if commit_if_changed "${repo_dir}" PKGBUILD .SRCINFO; then
    git -C "${repo_dir}" commit \
      -m "Publish ${RELEASE_VERSION} to AUR" \
      -m "Bump sing-box-cagedbird-bin to ${RELEASE_VERSION} from ${SOURCE_REPO}." \
      -m "Tested: downloaded release config and Linux tarball assets; verified sha256 values; git diff --check"
    push_or_note "${repo_dir}" origin master
  fi
}

update_homebrew() {
  local repo_dir="${workdir}/homebrew-tap"
  local formula="${repo_dir}/Formula/sing-box-cagedbird.rb"
  git clone "${HOMEBREW_TAP_REPO}" "${repo_dir}"
  git -C "${repo_dir}" config user.name "${GIT_AUTHOR_NAME}"
  git -C "${repo_dir}" config user.email "${GIT_AUTHOR_EMAIL}"

  python3 - "${formula}" <<'PY'
import os
import re
import sys
from pathlib import Path
path = Path(sys.argv[1])
text = path.read_text()
version = os.environ['RELEASE_VERSION']
sha = os.environ['SHA_DARWIN_ARM64']
source_url = os.environ['SOURCE_URL']
text = re.sub(r'^  version ".*"$', f'  version "{version}"', text, flags=re.M)
text = re.sub(
    r'https://github\.com/cagedbird043/sing-box/releases/download/v[^/]+/sing-box-cagedbird-darwin-arm64\.tar\.gz',
    f'{source_url}/releases/download/v{version}/sing-box-cagedbird-darwin-arm64.tar.gz',
    text,
)
text = re.sub(r'^(      sha256 ")[0-9a-f]+("$)', rf'\g<1>{sha}\g<2>', text, count=1, flags=re.M)
path.write_text(text)
PY

  git -C "${repo_dir}" diff --check
  ruby -c "${formula}"
  if commit_if_changed "${repo_dir}" Formula/sing-box-cagedbird.rb; then
    git -C "${repo_dir}" commit \
      -m "Publish ${RELEASE_VERSION} to Homebrew" \
      -m "Bump the sing-box-cagedbird formula to ${RELEASE_VERSION} from ${SOURCE_REPO}." \
      -m "Tested: downloaded darwin arm64 release tarball; verified sha256 value; git diff --check; ruby -c Formula/sing-box-cagedbird.rb"
    push_or_note "${repo_dir}" origin "$(git -C "${repo_dir}" branch --show-current)"
  fi
}

setup_ssh

assets_dir="${workdir}/assets"
mkdir -p "${assets_dir}"

SHA_SERVICE="$(sha256_url "${raw_url}/sing-box.service" "${assets_dir}/sing-box.service")"
SHA_SERVICE_AT="$(sha256_url "${raw_url}/sing-box@.service" "${assets_dir}/sing-box@.service")"
SHA_RECOVER="$(sha256_url "${raw_url}/sing-box-network-recover" "${assets_dir}/sing-box-network-recover")"
SHA_SYSUSERS="$(sha256_url "${raw_url}/sing-box.sysusers" "${assets_dir}/sing-box.sysusers")"
SHA_RULES="$(sha256_url "${raw_url}/sing-box.rules" "${assets_dir}/sing-box.rules")"
SHA_SPLIT_DNS="$(sha256_url "${raw_url}/sing-box-split-dns.xml" "${assets_dir}/sing-box-split-dns.xml")"
SHA_CONFIG="$(sha256_url "${raw_url}/config.json" "${assets_dir}/config.json")"
SHA_LINUX_AMD64="$(sha256_url "${release_url}/sing-box-cagedbird-linux-amd64.tar.gz" "${assets_dir}/sing-box-cagedbird-linux-amd64.tar.gz")"
SHA_LINUX_ARM64="$(sha256_url "${release_url}/sing-box-cagedbird-linux-arm64.tar.gz" "${assets_dir}/sing-box-cagedbird-linux-arm64.tar.gz")"
SHA_DARWIN_ARM64="$(sha256_url "${release_url}/sing-box-cagedbird-darwin-arm64.tar.gz" "${assets_dir}/sing-box-cagedbird-darwin-arm64.tar.gz")"

derive_aur_pkgver() {
  local short_commit rev_count
  short_commit="$(git rev-parse --short HEAD 2>/dev/null || true)"
  rev_count="$(git rev-list --count HEAD 2>/dev/null || true)"

  if [[ "${RELEASE_VERSION}" =~ ^(.+)-cagedbird\.([0-9a-f]+)$ && -n "${short_commit}" && -n "${rev_count}" ]]; then
    printf '%s-cagedbird.r%s.g%s\n' "${BASH_REMATCH[1]}" "${rev_count}" "${short_commit}"
  else
    printf '%s\n' "${RELEASE_VERSION}"
  fi
}

AUR_EPOCH="${AUR_EPOCH:-1}"
AUR_PKGVER="$(derive_aur_pkgver)"
AUR_PKGVER_UNDERSCORE="${AUR_PKGVER//-/_}"

export RELEASE_VERSION SOURCE_URL SOURCE_REPO release_url raw_url AUR_EPOCH AUR_PKGVER_UNDERSCORE
export SHA_SERVICE SHA_SERVICE_AT SHA_RECOVER SHA_SYSUSERS SHA_RULES SHA_SPLIT_DNS SHA_CONFIG
export SHA_LINUX_AMD64 SHA_LINUX_ARM64 SHA_DARWIN_ARM64

aur_status=0
update_aur || aur_status=$?
homebrew_status=0
update_homebrew || homebrew_status=$?

if (( aur_status != 0 )); then
  echo "::error::AUR publish failed for ${RELEASE_VERSION}" >&2
fi
if (( homebrew_status != 0 )); then
  echo "::error::Homebrew publish failed for ${RELEASE_VERSION}" >&2
fi
if (( aur_status != 0 || homebrew_status != 0 )); then
  exit 1
fi

echo "Distribution metadata is up to date for ${RELEASE_VERSION}."
