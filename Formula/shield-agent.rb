# Homebrew formula for shield-agent
# To use: brew tap itdar/tap && brew install shield-agent
#
# NOTE: If the repo is private, Homebrew cannot download assets directly.
# Use `go install` or the install script with GITHUB_TOKEN instead.
# This formula works once the repo and releases are public.

class ShieldAgent < Formula
  desc "Security middleware proxy for MCP servers and AI agents"
  homepage "https://github.com/itdar/shield-agent"
  version "1.0.0"
  license "MIT"

  on_macos do
    on_intel do
      url "https://github.com/itdar/shield-agent/releases/download/v#{version}/shield-agent_#{version}_darwin_amd64.tar.gz"
      sha256 "87147aa535a910c596a0abe564135016573ba3b16fd09722b1f4751049447e14"
    end

    on_arm do
      url "https://github.com/itdar/shield-agent/releases/download/v#{version}/shield-agent_#{version}_darwin_arm64.tar.gz"
      sha256 "6ad4cc8281f1c176f564db8049b154a86bfba5686108d28b31fd768f5959d140"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/itdar/shield-agent/releases/download/v#{version}/shield-agent_#{version}_linux_amd64.tar.gz"
      sha256 "ef714b75b42f789912850add5c6b5c16459a364eb861f602b486d565565ce24b"
    end

    on_arm do
      url "https://github.com/itdar/shield-agent/releases/download/v#{version}/shield-agent_#{version}_linux_arm64.tar.gz"
      sha256 "2391bcb96f5f2eae3cc753c48689a71a475acd9c2f46a1b892c849d8f515bb6e"
    end
  end

  def install
    bin.install "shield-agent"
  end

  test do
    assert_match "shield-agent", shell_output("#{bin}/shield-agent --help 2>&1")
  end
end
