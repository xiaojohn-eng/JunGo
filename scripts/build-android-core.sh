#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"
export GOROOT="$ROOT/.tools/go"
export PATH="$ROOT/.tools/go/bin:$ROOT/.tools/bin:$PATH"
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export JAVA_HOME="$ROOT/.tools/jdk"
export ANDROID_HOME="$ROOT/.tools/android-sdk"
export ANDROID_NDK_HOME="$ANDROID_HOME/ndk/28.2.13676358"
mkdir -p android/app/libs
WORK=$(mktemp -d "$ROOT/android/app/libs/.jungo-build.XXXXXX")
SOURCE_WORK=
trap 'rm -rf "$WORK"; if [ -n "$SOURCE_WORK" ]; then rm -rf "$SOURCE_WORK"; fi' EXIT HUP INT TERM
BUILD_ROOT=$ROOT
if [ "${JUNGO_RELEASE_SANITIZE:-0}" = 1 ]; then
  if ! git diff --quiet HEAD --; then
    echo 'Release JNI build requires a clean Git HEAD.' >&2
    exit 1
  fi
  SOURCE_WORK=$(mktemp -d "${TMPDIR:-/tmp}/jungo-release-source.XXXXXX")
  mkdir "$SOURCE_WORK/repo"
  git archive --format=tar --output="$SOURCE_WORK/source.tar" HEAD
  tar -xf "$SOURCE_WORK/source.tar" -C "$SOURCE_WORK/repo"
  rm "$SOURCE_WORK/source.tar"
  BUILD_ROOT=$SOURCE_WORK/repo
fi
(cd "$BUILD_ROOT" && gomobile bind -trimpath -tags=cmfa,with_gvisor -target=android/arm64 -androidapi=31 -ldflags='-s -w' -o "$WORK/jungo.aar" ./mobile)
mv "$WORK/jungo.aar" android/app/libs/jungo.aar
