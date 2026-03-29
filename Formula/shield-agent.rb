# Homebrew formula for shield-agent
# To use: brew tap itdar/tap && brew install shield-agent

class ShieldAgent < Formula
  desc "Security middleware proxy for MCP servers and AI agents"
  homepage "https://github.com/itdar/shield-agent"
  version "1.0.1"
  license "MIT"

  on_macos do
    on_intel do
      url "https://github.com/itdar/shield-agent/releases/download/v#{version}/shield-agent_#{version}_darwin_amd64.tar.gz"
      sha256 "2f7d5b01954465246ff176438f0a956364fa58218e024f211fe246cf619fe626"
    end

    on_arm do
      url "https://github.com/itdar/shield-agent/releases/download/v#{version}/shield-agent_#{version}_darwin_arm64.tar.gz"
      sha256 "2a513d60f99963a8a2c06da95c6c760eb44a17a62caf5b8177b3a8ed4ce707f8"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/itdar/shield-agent/releases/download/v#{version}/shield-agent_#{version}_linux_amd64.tar.gz"
      sha256 "bda0756d4f006c75335a50b3ba80f377b14a528fe5e8ae416c082cf4cbe2a141"
    end

    on_arm do
      url "https://github.com/itdar/shield-agent/releases/download/v#{version}/shield-agent_#{version}_linux_arm64.tar.gz"
      sha256 "b9333a7f2d80339e382dd89822f2409fb94bbee3deaf0c0ff4e0d18f3a4d10fe"
    end
  end

  def install
    bin.install "shield-agent"
  end

  test do
    assert_match "shield-agent", shell_output("#{bin}/shield-agent --help 2>&1")
  end
end
