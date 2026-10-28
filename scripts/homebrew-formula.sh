#!/usr/bin/env bash
#
# Prints the Homebrew formula that installs a release of bdb, from the release's tag and its checksums.txt.
#
# Usage:
#   scripts/homebrew-formula.sh <tag> <checksums.txt>
set -euo pipefail

tag="$1"
checksums="$2"
base="https://github.com/alexandremgo/baguettedb/releases/download/${tag}"

# Prints the url and sha256 lines of one platform's archive, or fails when checksums.txt does not list it.
archive() {
  local name="bdb_${tag}_$1_$2.tar.gz" sum
  sum="$(awk -v name="${name}" '$2 == name { print $1 }' "${checksums}")"
  if [ -z "${sum}" ]; then
    echo "${checksums} lists no ${name}" >&2
    return 1
  fi
  printf '      url "%s/%s"\n      sha256 "%s"' "${base}" "${name}" "${sum}"
}

darwin_arm64="$(archive darwin arm64)"
darwin_amd64="$(archive darwin amd64)"
linux_arm64="$(archive linux arm64)"
linux_amd64="$(archive linux amd64)"

cat <<EOF
class Baguettedb < Formula
  desc "Data layer for your AI agents: one structured database per topic"
  homepage "https://baguettedb.com"
  license "FSL-1.1-ALv2"

  on_macos do
    on_arm do
${darwin_arm64}
    end
    on_intel do
${darwin_amd64}
    end
  end

  on_linux do
    on_arm do
${linux_arm64}
    end
    on_intel do
${linux_amd64}
    end
  end

  def install
    bin.install "bdb"
    bin.install_symlink "bdb" => "baguettedb"
    prefix.install "LICENSE.md", "THIRD_PARTY_LICENSES"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/bdb version")
    assert_match version.to_s, shell_output("#{bin}/baguettedb version")
  end
end
EOF
