# Homebrew formula for shield-agent
# To use: brew tap itdar/tap && brew install shield-agent
# Or copy this to your own homebrew-tap repository.

class ShieldAgent < Formula
  desc "Security middleware proxy for MCP servers and AI agents"
  homepage "https://github.com/itdar/shield-agent"
  version "0.1.0"
  license "MIT"

  on_macos do
    on_intel do
      url "https://github.com/itdar/shield-agent/releases/download/v#{version}/shield-agent_#{version}_darwin_amd64.tar.gz"
      # sha256 "REPLACE_WITH_ACTUAL_SHA256"
    end

    on_arm do
      url "https://github.com/itdar/shield-agent/releases/download/v#{version}/shield-agent_#{version}_darwin_arm64.tar.gz"
      # sha256 "REPLACE_WITH_ACTUAL_SHA256"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/itdar/shield-agent/releases/download/v#{version}/shield-agent_#{version}_linux_amd64.tar.gz"
      # sha256 "REPLACE_WITH_ACTUAL_SHA256"
    end

    on_arm do
      url "https://github.com/itdar/shield-agent/releases/download/v#{version}/shield-agent_#{version}_linux_arm64.tar.gz"
      # sha256 "REPLACE_WITH_ACTUAL_SHA256"
    end
  end

  def install
    bin.install "shield-agent"
  end

  test do
    assert_match "shield-agent", shell_output("#{bin}/shield-agent --help 2>&1")
  end
end
