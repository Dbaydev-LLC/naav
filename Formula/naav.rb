# Copy into your tap (homebrew-naav/Formula/naav.rb). After each release, update
# `version` and the two sha256 values from the release's checksums.txt.
class Naav < Formula
  desc "Local personal URL alias router: type `nav gh` in the address bar"
  homepage "https://github.com/Dbaydev-LLC/naav"
  version "0.1.0"
  license "MIT"

  on_arm do
    url "https://github.com/Dbaydev-LLC/naav/releases/download/v#{version}/naavd_v#{version}_darwin_arm64.tar.gz"
    sha256 "REPLACE_ARM64_SHA256"
  end
  on_intel do
    url "https://github.com/Dbaydev-LLC/naav/releases/download/v#{version}/naavd_v#{version}_darwin_amd64.tar.gz"
    sha256 "REPLACE_AMD64_SHA256"
  end

  def install
    bin.install "naavd"
  end

  service do
    run [opt_bin/"naavd"]
    keep_alive true
    log_path var/"log/naavd.log"
    error_log_path var/"log/naavd.log"
  end

  test do
    assert_match "naavd", shell_output("#{bin}/naavd --version")
  end
end
