#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"
export PATH="$ROOT/.tools/go/bin:$ROOT/.tools/bin:$PATH"
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export JAVA_HOME="$ROOT/.tools/jdk"
export ANDROID_HOME="$ROOT/.tools/android-sdk"
export ANDROID_NDK_HOME="$ANDROID_HOME/ndk/28.2.13676358"
mkdir -p android/app/libs
WORK=$(mktemp -d "$ROOT/android/app/libs/.jungo-build.XXXXXX")
trap 'rm -rf "$WORK"' EXIT HUP INT TERM
gomobile bind -tags=cmfa,with_gvisor -target=android/arm64 -androidapi=31 -ldflags='-s -w' -o "$WORK/jungo.aar" ./mobile
mv "$WORK/jungo.aar" android/app/libs/jungo.aar
