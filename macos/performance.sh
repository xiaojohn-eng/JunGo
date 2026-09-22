#!/bin/bash
set -euo pipefail
macos_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mkdir -p "$macos_root/.build/checks"
swiftc -O -parse-as-library -target "$(uname -m)-apple-macosx13.0" \
  "$macos_root/Sources/JunGoMenu/Models.swift" \
  "$macos_root/Sources/JunGoMenu/LocalAgentClient.swift" \
  "$macos_root/Sources/JunGoMenu/PresentationIndex.swift" \
  "$macos_root/Tests/JunGoMenuTests/PerformanceBench.swift" \
  -o "$macos_root/.build/checks/performance"
printf 'Legacy query and decode baseline\n'
"$macos_root/.build/checks/performance" --legacy
printf 'Current implementation\n'
"$macos_root/.build/checks/performance"
