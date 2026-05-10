#!/usr/bin/env bash
set -euo pipefail

: "${TARGET_OS:?TARGET_OS is required}"
: "${TARGET_ARCH:?TARGET_ARCH is required}"
: "${ARTIFACT_ID:?ARTIFACT_ID is required}"

version="$(scripts/ci/resolve-cagedbird-version.sh)"
build_tags="${BUILD_TAGS:-$(cat release/DEFAULT_BUILD_TAGS_OTHERS)}"
ldflags_shared="$(cat release/LDFLAGS)"
ldflags="-X 'github.com/sagernet/sing-box/constant.Version=${version}' ${ldflags_shared} -s -w -buildid="

out_root="${OUT_ROOT:-dist/cagedbird-binaries}"
work_dir="${out_root}/${ARTIFACT_ID}"
rm -rf "${work_dir}"
mkdir -p "${work_dir}"

export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"

if [[ "${TARGET_OS}" == "android" ]]; then
  : "${ANDROID_NDK_TRIPLE:?ANDROID_NDK_TRIPLE is required for android builds}"
  go install -v ./cmd/internal/build
  export PATH="${PATH}:$(go env GOPATH)/bin"
  if [[ -n "${ANDROID_NDK_HOME:-}" ]]; then
    export PATH="${ANDROID_NDK_HOME}/toolchains/llvm/prebuilt/linux-x86_64/bin:${PATH}"
  fi
  export CGO_ENABLED=1
  export CC="${ANDROID_NDK_TRIPLE}-clang"
  export CXX="${CC}++"
  GOOS="${TARGET_OS}" \
    GOARCH="${TARGET_ARCH}" \
    GOAMD64="${TARGET_GOAMD64:-}" \
    GOARM="${TARGET_GOARM:-}" \
    GOMIPS="${TARGET_GOMIPS:-}" \
    GOMIPS64="${TARGET_GOMIPS64:-${TARGET_GOMIPS:-}}" \
    build go build -v -trimpath -o "${work_dir}/sing-box" -tags "${build_tags}" -ldflags "${ldflags}" ./cmd/sing-box
else
  export CGO_ENABLED="${CGO_ENABLED:-0}"
  export GOOS="${TARGET_OS}"
  export GOARCH="${TARGET_ARCH}"
  export GOAMD64="${TARGET_GOAMD64:-}"
  export GOARM="${TARGET_GOARM:-}"
  export GOMIPS="${TARGET_GOMIPS:-}"
  export GOMIPS64="${TARGET_GOMIPS64:-${TARGET_GOMIPS:-}}"
  go build -v -trimpath -o "${work_dir}/sing-box" -tags "${build_tags}" -ldflags "${ldflags}" ./cmd/sing-box
fi

cp LICENSE "${work_dir}/"
cat > "${work_dir}/BUILD-INFO.txt" <<INFO
version=${version}
commit=$(git rev-parse HEAD)
target=${TARGET_OS}/${TARGET_ARCH}
goamd64=${TARGET_GOAMD64:-}
tags=${build_tags}
INFO

(
  cd "${out_root}"
  tar -czf "${ARTIFACT_ID}.tar.gz" "${ARTIFACT_ID}"
)

printf 'built %s/%s -> %s/%s.tar.gz\n' "${TARGET_OS}" "${TARGET_ARCH}" "${out_root}" "${ARTIFACT_ID}"
