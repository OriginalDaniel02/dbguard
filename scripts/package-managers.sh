#!/usr/bin/env bash
# Generates the Homebrew formula and the Scoop manifest for a release from its checksums.txt.
#
#   scripts/package-managers.sh v0.5.0 path/to/checksums.txt outdir
#
# Writes outdir/dbguard.rb (Homebrew) and outdir/dbguard.json (Scoop). Both download the plain
# release binaries, so no archives are needed, and both pin each file by its SHA-256.
set -euo pipefail

tag="${1:?usage: package-managers.sh <tag, e.g. v0.5.0> <checksums.txt> <outdir>}"
sums="${2:?checksums.txt}"
out="${3:?outdir}"
version="${tag#v}"
repo="OriginalDaniel02/dbguard"
base="https://github.com/${repo}/releases/download/${tag}"

sha() {
  local h
  h=$(awk -v f="$1" '$2 == f { print $1 }' "$sums")
  [ -n "$h" ] || { echo "no checksum for $1 in $sums" >&2; exit 1; }
  echo "$h"
}

mkdir -p "$out"

cat > "$out/dbguard.rb" <<EOF
class Dbguard < Formula
  desc "Catch database migrations that will lock production, and schema drift"
  homepage "https://github.com/${repo}"
  version "${version}"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "${base}/dbguard_darwin_arm64"
      sha256 "$(sha dbguard_darwin_arm64)"
    end
    on_intel do
      url "${base}/dbguard_darwin_amd64"
      sha256 "$(sha dbguard_darwin_amd64)"
    end
  end

  on_linux do
    on_arm do
      url "${base}/dbguard_linux_arm64"
      sha256 "$(sha dbguard_linux_arm64)"
    end
    on_intel do
      url "${base}/dbguard_linux_amd64"
      sha256 "$(sha dbguard_linux_amd64)"
    end
  end

  def install
    # A plain (non-archive) download is staged under its own file name.
    bin.install Dir["dbguard_*"].first => "dbguard"
  end

  test do
    assert_match "dbguard v#{version}", shell_output("#{bin}/dbguard version")
  end
end
EOF

cat > "$out/dbguard.json" <<EOF
{
    "version": "${version}",
    "description": "Catch database migrations that will lock production, and schema drift",
    "homepage": "https://github.com/${repo}",
    "license": "Apache-2.0",
    "architecture": {
        "64bit": {
            "url": "${base}/dbguard_windows_amd64.exe#/dbguard.exe",
            "hash": "$(sha dbguard_windows_amd64.exe)"
        }
    },
    "bin": "dbguard.exe",
    "checkver": "github",
    "autoupdate": {
        "architecture": {
            "64bit": {
                "url": "https://github.com/${repo}/releases/download/v\$version/dbguard_windows_amd64.exe#/dbguard.exe",
                "hash": {
                    "url": "https://github.com/${repo}/releases/download/v\$version/checksums.txt",
                    "regex": "\$sha256\\\\s+dbguard_windows_amd64\\\\.exe"
                }
            }
        }
    }
}
EOF

echo "wrote $out/dbguard.rb and $out/dbguard.json for ${tag}"
