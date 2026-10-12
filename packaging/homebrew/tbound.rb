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
      url "REPLACE_WITH_PUBLISHED_DARWIN_ARM64_RELEASE_URL"
      sha256 "REPLACE_WITH_DARWIN_ARM64_SHA256"
    end
    on_intel do
      url "REPLACE_WITH_PUBLISHED_DARWIN_AMD64_RELEASE_URL"
      sha256 "REPLACE_WITH_DARWIN_AMD64_SHA256"
    end
  end

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
    if File.exist?("libexec/tbound/tbound-cell-entrypoint")
      (libexec/"tbound").install "libexec/tbound/tbound-cell-entrypoint",
                                   "libexec/tbound/cell-entrypoint.json"
    end
  end

  def caveats
    <<~EOS
      tbound installs next to your Pi. Run `tbound-doctor` to check readiness and
      `tbound-profile` to generate/sign the Linux host profile (Linux-only).
      On linux/amd64 the unsigned D09 TCB entrypoint binary is installed under
      #{libexec}/tbound; it is not launched by the CLI or trusted by package install.
      Governed `tbound serve --pi`
      additionally needs a signed host profile and the Podman/crun containment stack.
    EOS
  end

  test do
    system "#{bin}/tbound-doctor", "--json"
    assert_predicate bin/"tbound-profile", :executable?
    if OS.linux? && Hardware::CPU.intel?
      system "#{bin}/tbound-profile", "--help"
    end
  end
end
