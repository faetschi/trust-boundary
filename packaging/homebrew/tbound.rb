# Homebrew formula template for tbound.
#
# This is a template: the release host, version, and per-platform sha256 values
# are placeholders until releases are published. To use it in a tap:
#   1. Publish the platform tarballs (see scripts/build-release.sh).
#   2. Fill in url + sha256 below.
#   3. `brew install --build-from-source ./packaging/homebrew/tbound.rb` (local test).
class Tbound < Formula
  desc "Host-side supervisor that runs the Pi coding agent as an untrusted worker"
  homepage "https://github.com/faetschi/trust-boundary"
  version "0.1.0"
  license "MIT"

  on_macos do
    on_arm do
      url "https://get.tbound.dev/releases/tbound-#{version}-darwin-arm64.tar.gz"
      sha256 "REPLACE_WITH_DARWIN_ARM64_SHA256"
    end
    on_intel do
      url "https://get.tbound.dev/releases/tbound-#{version}-darwin-amd64.tar.gz"
      sha256 "REPLACE_WITH_DARWIN_AMD64_SHA256"
    end
  end

  on_linux do
    on_arm do
      url "https://get.tbound.dev/releases/tbound-#{version}-linux-arm64.tar.gz"
      sha256 "REPLACE_WITH_LINUX_ARM64_SHA256"
    end
    on_intel do
      url "https://get.tbound.dev/releases/tbound-#{version}-linux-amd64.tar.gz"
      sha256 "REPLACE_WITH_LINUX_AMD64_SHA256"
    end
  end

  def install
    bin.install "bin/tbound"
    bin.install "bin/tbound-doctor"
    (libexec/"runtime").install Dir["runtime/*"]
  end

  def caveats
    <<~EOS
      tbound installs next to your Pi. Run `tbound-doctor` to check readiness.
      Governed `tbound serve --pi` additionally needs a signed host profile and the
      Podman/crun containment stack on Linux.
    EOS
  end

  test do
    system "#{bin}/tbound-doctor", "--json"
  end
end
