#!/usr/bin/env bash
set -euo pipefail

version="$(scripts/ci/resolve-cagedbird-version.sh)"

if [[ -z "${JAVA_HOME:-}" && -d /usr/lib/jvm/java-17-openjdk ]]; then
  export JAVA_HOME=/usr/lib/jvm/java-17-openjdk
fi

build_tag="v${version}"
if git rev-parse -q --verify "refs/tags/${build_tag}" >/dev/null; then
  build_tag_existed=1
else
  build_tag_existed=0
fi
cleanup_build_tag() {
  if [[ "${CAGEDBIRD_KEEP_BUILD_TAG:-0}" != "1" && "${build_tag_existed}" == "0" ]]; then
    git tag -d "${build_tag}" >/dev/null 2>&1 || true
  fi
}
trap cleanup_build_tag EXIT
git tag "${build_tag}" -f

android_app_branch="${CAGEDBIRD_ANDROID_APP_BRANCH:-dev}"
echo "Using Android app branch: ${android_app_branch}"
git -C clients/android fetch --depth 1 origin "${android_app_branch}"
git -C clients/android checkout --detach FETCH_HEAD
git -C clients/android submodule update --init --recursive

signing_mode="secret-stable-key"
if [[ -n "${LOCAL_PROPERTIES:-}" || -n "${CAGEDBIRD_ANDROID_RELEASE_KEYSTORE_BASE64:-}" ]]; then
  if [[ -z "${LOCAL_PROPERTIES:-}" || -z "${CAGEDBIRD_ANDROID_RELEASE_KEYSTORE_BASE64:-}" ]]; then
    echo "LOCAL_PROPERTIES and CAGEDBIRD_ANDROID_RELEASE_KEYSTORE_BASE64 must be configured together for stable Android signing." >&2
    exit 1
  fi
  echo "Using stable Android signing material from repository secrets."
  printf '%s' "${CAGEDBIRD_ANDROID_RELEASE_KEYSTORE_BASE64}" | base64 -d > clients/android/app/release.keystore
  # LOCAL_PROPERTIES is intentionally left in the environment: the Android Gradle
  # project decodes that base64-encoded properties blob itself. Validate that it is
  # decodable here so CI fails before Gradle if the secret is malformed.
  printf '%s' "${LOCAL_PROPERTIES}" | base64 -d >/dev/null
else
  signing_mode="ephemeral-ci-key"
  echo "Stable Android signing secrets are empty; generating an ephemeral CI signing key."
  signing_pass="${CAGEDBIRD_APK_KEYSTORE_PASS:-cagedbird-ci-password}"
  signing_alias="${CAGEDBIRD_APK_KEY_ALIAS:-cagedbird-ci}"
  signing_key_pass="${CAGEDBIRD_APK_KEY_PASS:-${signing_pass}}"
  rm -f clients/android/app/release.keystore
  keytool -genkeypair -v \
    -keystore clients/android/app/release.keystore \
    -storepass "${signing_pass}" \
    -keypass "${signing_key_pass}" \
    -alias "${signing_alias}" \
    -keyalg RSA \
    -keysize 2048 \
    -validity 10000 \
    -dname "CN=Cagedbird sing-box CI, OU=Cagedbird, O=Cagedbird, L=Unknown, ST=Unknown, C=US" >/dev/null
  cat > clients/android/local.properties <<PROPS
KEYSTORE_PASS=${signing_pass}
ALIAS_NAME=${signing_alias}
ALIAS_PASS=${signing_key_pass}
PROPS
fi

make lib_install
export PATH="${PATH}:$(go env GOPATH)/bin"
go run -v ./cmd/internal/build_libbox -target android -platform android/arm64,android/amd64

mkdir -p clients/android/app/libs
cp -f ./libbox.aar clients/android/app/libs/

go run -v ./cmd/internal/update_android_version --ci --nightly

(
  cd clients/android
  ./gradlew :app:assembleOtherRelease --configuration-cache --build-cache --parallel
)

out_dir="${OUT_DIR:-dist/cagedbird-android}"
rm -rf "${out_dir}"
mkdir -p "${out_dir}"

cp -f ./libbox.aar "${out_dir}/"
find clients/android/app/build/outputs/apk/other/release \
     -type f -name '*.apk' -exec cp -f {} "${out_dir}/" \;

if [[ -f clients/android/version.properties ]]; then
  cp -f clients/android/version.properties "${out_dir}/SFA-version.properties"
fi

cat > "${out_dir}/BUILD-INFO.txt" <<INFO
version=${version}
commit=$(git rev-parse HEAD)
artifact=android-apk-aar
signing=${signing_mode}
INFO

ls -lh "${out_dir}"
