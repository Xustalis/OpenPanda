# Homebrew formula for OpenPanda — installs the prebuilt release binary plus
# its agent adapters from GitHub Releases.
#
#   brew tap Xustalis/openpanda
#   brew install openpanda
#
# NOTE: `sha256 :no_check` keeps `brew install` working for any tagged release
# without manually tracking hashes, at the cost of skipping checksum
# verification. For a hardened tap, replace each `:no_check` with the real
# value from `dist/checksums.txt` (or run `brew bump-formula-pr` after tagging)
# and bump `version` to match.

class Openpanda < Formula
  desc "Personal adaptive node-based distributed assistant (agent-of-agents)"
  homepage "https://github.com/Xustalis/OpenPanda"
  license "MIT"
  version "0.0.2"

  depends_on "python@3.12"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/Xustalis/OpenPanda/releases/download/v#{version}/panda-#{version}-darwin-arm64.tar.gz"
      sha256 :no_check
    else
      url "https://github.com/Xustalis/OpenPanda/releases/download/v#{version}/panda-#{version}-darwin-amd64.tar.gz"
      sha256 :no_check
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/Xustalis/OpenPanda/releases/download/v#{version}/panda-#{version}-linux-arm64.tar.gz"
      sha256 :no_check
    else
      url "https://github.com/Xustalis/OpenPanda/releases/download/v#{version}/panda-#{version}-linux-amd64.tar.gz"
      sha256 :no_check
    end
  end

  def install
    bin.install "openpanda/bin/panda"
    # adapters must sit beside the real binary (…/../adapters) so the daemon
    # finds claude_code.py etc. once Homebrew symlinks bin/panda onto PATH.
    (prefix/"adapters").install Dir["openpanda/adapters/*"]
    prefix.install "openpanda/config.example.yaml"
    prefix.install Dir["openpanda/capabilities.example-*.yaml"]
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/panda version")
  end
end