# Homebrew formula for OpenPanda — installs the prebuilt release binary plus
# its agent adapters from GitHub Releases.
#
#   brew tap Xustalis/openpanda
#   brew install openpanda
#
# This checked-in copy is a development fallback. Tagged releases render a
# checksum-pinned Formula/openpanda.rb from openpanda.rb.tmpl and publish it to
# Xustalis/homebrew-openpanda.

class Openpanda < Formula
  desc "Personal adaptive node-based distributed assistant (agent-of-agents)"
  homepage "https://github.com/Xustalis/OpenPanda"
  license "MIT"
  version "0.0.4"

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
    root = buildpath/"openpanda"
    root = buildpath unless root.directory?
    bin.install root/"bin/panda"
    # adapters must sit beside the real binary (…/../adapters) so the daemon
    # finds claude_code.py etc. once Homebrew symlinks bin/panda onto PATH.
    (prefix/"adapters").install Dir[root/"adapters/*"]
    prefix.install root/"config.example.yaml"
    prefix.install Dir[root/"capabilities.example-*.yaml"]
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/panda version")
  end
end
