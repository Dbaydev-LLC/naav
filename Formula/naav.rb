# Copy into your tap (homebrew-naav/Formula/naav.rb). After each release, update
# `version` and the two sha256 values from the release's checksums.txt.
class Naav < Formula
  desc "Local personal URL alias router: type `nav gh` in the address bar"
  homepage "https://github.com/Dbaydev-LLC/naav"
  version "0.1.0"
  license "MIT"

  on_arm do
    url "https://github.com/Dbaydev-LLC/naav/releases/download/v#{version}/naavd_v#{version}_darwin_arm64.tar.gz"
    sha256 "656759dfbc5e1f223f8521463a5ea07373d8a76cae6a0eae351f0342b1e2599a"
  end
  on_intel do
    url "https://github.com/Dbaydev-LLC/naav/releases/download/v#{version}/naavd_v#{version}_darwin_amd64.tar.gz"
    sha256 "1767495c0be2baa8dae7d20d12e979413b39fc45f489c469fe7c49a24764c499"
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
