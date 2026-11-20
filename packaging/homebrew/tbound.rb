# Homebrew formula template for tbound.
#
# This is a template: the release URL and sha256 are placeholders. Releases are
# linux/amd64 only, so the formula is for Homebrew on Linux. To use it in a tap:
#   1. Publish the linux/amd64 tarball (see scripts/build-release.sh).
#   2. Fill in url + sha256 below.
#   3. `brew install --build-from-source ./packaging/homebrew/tbound.rb` (local test).
class Tbound < Formula
  desc "Host-side supervisor that runs the Pi coding agent as an untrusted worker"
  homepage "https://github.com/faetschi/trust-boundary"
  version "0.1.0"
  license "MIT"
  depends_on :linux

  on_linux do
    on_intel do
      url "REPLACE_WITH_PUBLISHED_LINUX_AMD64_RELEASE_URL"
      sha256 "REPLACE_WITH_LINUX_AMD64_SHA256"
    end
    # linux-arm64 is intentionally absent: the runtime does not build for
    # linux/arm64 yet (amd64-only syscall helpers), so it is not advertised.
  end

  def install
    bin.install "bin/tbound"
    bin.install "bin/tbound-doctor"
    bin.install "bin/tbound-profile"
    (libexec/"runtime").install Dir["runtime/*"]
    (libexec/"tbound").install "libexec/tbound/tbound-cell-entrypoint",
                                 "libexec/tbound/cell-entrypoint.json"
  end

  def caveats
    <<~EOS
      tbound installs next to your Pi. Run `tbound-doctor` to check readiness and
      `tbound-profile` to generate/sign the Linux host profile.
      The unsigned D09 TCB entrypoint binary is installed under
      #{libexec}/tbound; it is not launched by the CLI or trusted by package install.
      Governed `tbound serve --pi`
      additionally needs a signed host profile and the Podman/crun containment stack.
    EOS
  end

  test do
    system "#{bin}/tbound-doctor", "--json"
    system "#{bin}/tbound-profile", "--help"
  end
end
