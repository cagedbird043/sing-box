#!/usr/bin/env bash
set -euo pipefail

: "${RELEASE_VERSION:?RELEASE_VERSION is required}"

SOURCE_REPO="${SOURCE_REPO:-cagedbird043/sing-box}"
SOURCE_URL="${SOURCE_URL:-https://github.com/${SOURCE_REPO}}"
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
  ssh-keyscan github.com >> "${HOME}/.ssh/known_hosts" 2>/dev/null
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

write_homebrew_formula() {
  local formula="$1"
  cat > "${formula}" <<FORMULA
class SingBoxCagedbird < Formula
  desc "Universal proxy platform with native Clash subscription support"
  homepage "${SOURCE_URL}"
  version "${RELEASE_VERSION}"
  license "GPL-3.0-or-later"

  on_macos do
    on_arm do
      url "${release_url}/sing-box-cagedbird-darwin-arm64.tar.gz"
      sha256 "${SHA_DARWIN_ARM64}"
    end

    on_intel do
      odie "sing-box-cagedbird currently publishes macOS arm64 binaries only"
    end
  end

  on_linux do
    on_intel do
      url "${release_url}/sing-box-cagedbird-linux-amd64.tar.gz"
      sha256 "${SHA_LINUX_AMD64}"
    end

    on_arm do
      url "${release_url}/sing-box-cagedbird-linux-arm64.tar.gz"
      sha256 "${SHA_LINUX_ARM64}"
    end
  end

  conflicts_with "sing-box", because: "both install the sing-box binary"

  def install
    bin.install "sing-box"
    prefix.install "BUILD-INFO.txt"
    pkgshare.install "LICENSE"

    generate_completions_from_executable(bin/"sing-box", "completion")
  end

  service do
    run [opt_bin/"sing-box", "-D", var/"lib/sing-box", "-C", etc/"sing-box", "run"]
    keep_alive true
    require_root true
    log_path var/"log/sing-box.log"
    error_log_path var/"log/sing-box.log"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/sing-box version")
    assert_match "with_clash_api", shell_output("#{bin}/sing-box version")
  end
end
FORMULA
}

update_homebrew() {
  local repo_dir="${workdir}/homebrew-tap"
  local formula="${repo_dir}/Formula/sing-box-cagedbird.rb"
  git clone "${HOMEBREW_TAP_REPO}" "${repo_dir}"
  git -C "${repo_dir}" config user.name "${GIT_AUTHOR_NAME}"
  git -C "${repo_dir}" config user.email "${GIT_AUTHOR_EMAIL}"

  write_homebrew_formula "${formula}"

  git -C "${repo_dir}" diff --check
  ruby -c "${formula}"
  if commit_if_changed "${repo_dir}" Formula/sing-box-cagedbird.rb; then
    git -C "${repo_dir}" commit \
      -m "Publish ${RELEASE_VERSION} to Homebrew" \
      -m "Bump the sing-box-cagedbird formula to ${RELEASE_VERSION} from ${SOURCE_REPO}." \
      -m "Tested: downloaded Darwin arm64 and Linux amd64/arm64 release tarballs; verified sha256 values; git diff --check; ruby -c Formula/sing-box-cagedbird.rb"
    push_or_note "${repo_dir}" origin "$(git -C "${repo_dir}" branch --show-current)"
  fi
}

setup_ssh

assets_dir="${workdir}/assets"
mkdir -p "${assets_dir}"

SHA_LINUX_AMD64="$(sha256_url "${release_url}/sing-box-cagedbird-linux-amd64.tar.gz" "${assets_dir}/sing-box-cagedbird-linux-amd64.tar.gz")"
SHA_LINUX_ARM64="$(sha256_url "${release_url}/sing-box-cagedbird-linux-arm64.tar.gz" "${assets_dir}/sing-box-cagedbird-linux-arm64.tar.gz")"
SHA_DARWIN_ARM64="$(sha256_url "${release_url}/sing-box-cagedbird-darwin-arm64.tar.gz" "${assets_dir}/sing-box-cagedbird-darwin-arm64.tar.gz")"

export RELEASE_VERSION SOURCE_URL SOURCE_REPO release_url
export SHA_LINUX_AMD64 SHA_LINUX_ARM64 SHA_DARWIN_ARM64

update_homebrew

echo "Homebrew metadata is up to date for ${RELEASE_VERSION}."
