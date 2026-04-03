# shield-agent

AI Agentの時代のセキュリティミドルウェア。
AgentとServerの間に透過的に位置し、**認証、防御、ロギング、モニタリング**を提供します。

Goで書かれた **~10MB の単一バイナリ**。インストール30秒、設定1分。

```
┌──────────┐         ┌──────────────────────────┐         ┌──────────────┐
│ AI Agent │ ──────> │      shield-agent        │ ──────> │ Target Server│
│ MCP/A2A  │ <────── │  [auth] [guard] [log]    │ <────── │ MCP/A2A/API  │
└──────────┘         │  monitor :9090 /metrics  │         └──────────────┘
                     │  Web UI  :9090 /ui       │
                     └──────────────────────────┘
```

🌐 [English](README.md) | [한국어](README.ko.md) | [日本語](README.ja.md) | [中文](README.zh.md)

---

## 目次

- [どんな場面で使うの？](#どんな場面で使うの)
- [インストール](#インストール)
- [ユースケース別クイックスタート](#ユースケース別クイックスタート)
- [認証方式の選択ガイド](#認証方式の選択ガイド)
- [デプロイパターン](#デプロイパターン)
- [設定リファレンス](#設定リファレンス)
- [モニタリング](#モニタリング)
- [Web UI](#web-ui)
- [ロードマップ](#ロードマップ)

---

## どんな場面で使うの？

| 状況 | shield-agentができること |
|------|------------------------|
| MCPサーバーを外部公開するが、誰でもアクセスさせたくない | Ed25519署名 / トークンベース認証 |
| エージェントがAPIを呼び過ぎている | レート制限 + 時間/月単位のクォータ |
| 誰がいつ何を呼んだか記録が必要 | SQLite監査ログ + Prometheusメトリクス |
| 特定のIPからのリクエストをブロックしたい | IPブロックリスト/許可リスト |
| 複数のMCPサーバーを単一エンドポイントにまとめたい | Gatewayモード（ホスト/パスベースルーティング） |
| Web UIで簡単に管理したい | 組み込みダッシュボード `/ui` |

### 保護対象

| 通信経路 | プロトコル | モード |
|----------|-----------|--------|
| Agent → MCP Server | JSON-RPC (stdio / SSE / Streamable HTTP) | stdio, proxy |
| Agent → Agent (A2A) | Google A2Aプロトコル | HTTPミドルウェア |
| Agent → API Server | REST / GraphQL | HTTPミドルウェア |

---

## インストール

```bash
# Homebrew (macOS / Linux)
brew tap itdar/tap && brew install shield-agent

# curlインストールスクリプト
curl -sSL https://raw.githubusercontent.com/itdar/shield-agent/main/scripts/install.sh | sh

# Go install
go install github.com/itdar/shield-agent/cmd/shield-agent@latest

# Docker
docker pull ghcr.io/itdar/shield-agent:latest

# ソースビルド
git clone https://github.com/itdar/shield-agent.git
cd shield-agent && go build -o shield-agent ./cmd/shield-agent
```

---

## ユースケース別クイックスタート

### ケース1: ローカルMCPサーバーの保護（stdioモード）

> Python/Node.js MCPサーバーをラップして認証とロギングを追加したいとき

```bash
# 最もシンプルな使い方 — MCPサーバープロセスをラップするだけ
shield-agent python my_mcp_server.py

# verboseモードでデバッグ
shield-agent --verbose node server.js --port 8080
```

**動作原理:**
```
MCP Client ──stdin──> shield-agent ──stdin──> MCP Server (child process)
MCP Client <─stdout── shield-agent <─stdout── MCP Server
                          │
                     ミドルウェアチェーン
                   [auth] [guard] [log]
```

- shield-agentがMCPサーバーを子プロセスとして起動
- stdin/stdoutをインターセプトしてミドルウェアチェーンを適用
- stderrはそのまま通過
- SIGINT/SIGTERMを自動転送、終了コードを伝播

### ケース2: リモートMCPサーバーの前にプロキシ（proxyモード）

> 既に動いているMCPサーバーの前にセキュリティレイヤーを追加したいとき

```bash
# Streamable HTTP（デフォルト）
shield-agent proxy --listen :8888 --upstream http://localhost:8000

# SSEトランスポート
shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport sse

# HTTPS
shield-agent proxy --listen :8888 --upstream http://localhost:8000 \
  --tls-cert cert.pem --tls-key key.pem
```

**動作原理:**
```
MCP Client ──HTTP──> shield-agent :8888 ──HTTP──> MCP Server :8000
                          │
                     ミドルウェアチェーン
                     モニタリング :9090
```

### ケース3: 複数サーバーを単一Gatewayに（Gatewayモード）

> 複数のMCP/APIサーバーを単一のshield-agentエンドポイントの背後に置きたいとき

`shield-agent.yaml`:
```yaml
upstreams:
  - name: mcp-server-a
    url: http://10.0.1.1:8000
    match:
      host: mcp-a.example.com       # Hostヘッダーベースのルーティング
    transport: sse

  - name: api-server-b
    url: http://10.0.2.1:3000
    match:
      path_prefix: /api-b           # パスベースのルーティング
      strip_prefix: true

  - name: default-mcp
    url: http://10.0.3.1:8000       # マッチしない場合のフォールバック
```

```bash
shield-agent proxy --listen :8888
```

**動作原理:**
```
Agent A ──mcp-a.example.com──> shield-agent :8888 ──> MCP Server A
Agent B ──/api-b/v1/data─────> shield-agent :8888 ──> API Server B (/v1/data)
Agent C ──その他のリクエスト──> shield-agent :8888 ──> Default MCP
```

- HostヘッダーまたはURLパスプレフィックスによるルーティング
- マッチ優先度: Host+Path > Hostのみ > Pathのみ > フォールバック
- `strip_prefix: true` の場合、upstreamへはプレフィックスを除去して転送
- Web UI（`/ui`）からupstreamの動的追加/削除が可能

### ケース4: Docker Composeでデプロイ

```yaml
services:
  shield:
    image: ghcr.io/itdar/shield-agent:latest
    command: proxy --listen :8888 --upstream http://mcp-server:8000
    ports:
      - "8888:8888"
      - "9090:9090"
    volumes:
      - ./shield-agent.yaml:/shield-agent.yaml:ro
      - ./keys.yaml:/keys.yaml:ro
      - shield-data:/data
    environment:
      - SHIELD_AGENT_DB_PATH=/data/shield-agent.db

  mcp-server:
    image: your-mcp-server:latest

volumes:
  shield-data:
```

### ケース5: トークンでエージェントのアクセス制御

> 外部エージェントにAPIキーのようなトークンを発行し、クォータを管理したいとき

```bash
# トークン発行
shield-agent token create --name "partner-agent" --quota-hourly 1000
# → Token: a3f8c1...  (一度だけ表示されます — 安全に保管してください)

# トークン一覧確認
shield-agent token list

# 使用量確認
shield-agent token stats <token-id> --since 24h

# トークン失効（即時有効）
shield-agent token revoke <token-id>
```

エージェントはリクエスト時にヘッダーにトークンを含めます:
```
Authorization: Bearer a3f8c1...
```

---

## 認証方式の選択ガイド

shield-agentは **3つの認証方式**をサポートしています。状況に応じて選択してください:

```
┌─────────────────────────────────────────────────────────────────┐
│ Ed25519 + keys.yaml    — 高セキュリティ、少数のエージェント         │
│ Ed25519 + DID          — オープンエコシステム、事前登録不要          │
│ Bearer Token           — シンプル、APIキーのように、クォータ管理可能 │
└─────────────────────────────────────────────────────────────────┘
```

| | Ed25519 + keys.yaml | Ed25519 + DID | Bearer Token |
|---|---|---|---|
| **誰がキーを作成するか** | エージェントが生成、管理者が登録 | エージェントが生成、登録不要 | 管理者が発行 |
| **事前登録** | 必要（keys.yamlまたはWeb UI） | 不要 | トークンの配布が必要 |
| **リクエストごとの署名** | あり | あり | なし |
| **トークン盗取リスク** | 秘密鍵の盗取が必要（困難） | 秘密鍵の盗取が必要 | トークン盗取で十分 |
| **クォータ/有効期限** | なし | なし | あり（時間/月単位） |
| **即時失効** | keys.yamlから削除 | ブロックリストに追加 | `token revoke`（即時） |
| **推奨シーン** | 内部エージェント5〜10個 | 不特定多数、オープンエコシステム | 外部パートナー、APIキー方式 |

### keys.yaml登録方式

```yaml
# keys.yaml
keys:
  - id: "agent-1"
    key: "base64エンコードされたEd25519公開鍵"
```

エージェントはリクエスト時にJSON-RPCのparamsに含めます:
```json
{
  "method": "tools/list",
  "params": {
    "_mcp_agent_id": "agent-1",
    "_mcp_signature": "hexエンコードされた署名値"
  }
}
```

### Web UIでキー登録（keys.yamlの代わりに）

Web UIの `/ui` から **Agent Keys** メニューで公開鍵の登録/削除が可能です。
keys.yamlとDBの両方からキーを検索するので、どちらに登録しても動作します。

### DID方式（事前登録不要）

エージェントが `did:key:z6Mk...` 形式のIDを使用すると、IDから直接公開鍵を抽出します。
keys.yamlへの登録が不要なため、大規模なエージェント環境に適しています。

### セキュリティモード

| モード | 動作 |
|--------|------|
| `open`（デフォルト） | 署名なしでも通過、警告のみログ記録 |
| `verified` | 有効な署名が必須、未登録のDIDもOK（ただし差別化レート制限が可能） |
| `closed` | 登録済みエージェントのみアクセス可能 |

```yaml
# shield-agent.yaml
security:
  mode: verified
  did_blocklist:            # 悪意のあるDIDのみブロック（許可リストではない）
    - "did:key:z6Mk..."
```

---

## デプロイパターン

### パターン1: サイドカー（サーバーごとに1つ）

```
[Agent] → [shield-agent :8881] → [MCP Server A]
[Agent] → [shield-agent :8882] → [MCP Server B]
```

- 最もシンプルな構成
- サービス2〜3個のときに推奨
- 各shield-agentが独立して認証/ロギングを処理

### パターン2: Gateway（中央に1つ）

```
[Agent A] ──> [shield-agent :8888] ──> [MCP Server A]
[Agent B] ──>       (gateway)     ──> [API Server B]
```

- サービス5個以上のときに推奨
- 認証/トークン/ログを一箇所で管理
- `upstreams`設定でホスト/パスルーティング

### パターン3: nginx + shield-agent

```
[Agent] → [nginx (TLS)] → [shield-agent :8888 (HTTP)] → [upstream]
```

- nginxでTLSターミネーションを処理
- shield-agentはHTTPのみで動作
- `--tls-cert/--tls-key`の設定不要

### モニタリング統合（サイドカーでも中央観測）

```
[shield-agent A :9090/metrics] ──┐
[shield-agent B :9091/metrics] ──┼── Prometheus ──> Grafana
[shield-agent C :9092/metrics] ──┘
```

Prometheusが各shield-agentの `/metrics` をスクレイプすることで、
サイドカー構成でも全サービスのリクエスト数/エラー率/レイテンシを中央で確認できます。

---

## 設定リファレンス

**優先度:** CLIフラグ > 環境変数（`SHIELD_AGENT_*`） > YAML設定 > デフォルト値

`shield-agent.example.yaml` を `shield-agent.yaml` にコピーして始めてください。

### 主要設定

| 設定 | デフォルト | 環境変数 | 説明 |
|------|-----------|---------|------|
| `security.mode` | `open` | `SHIELD_AGENT_SECURITY_MODE` | `open` / `verified` / `closed` |
| `security.key_store_path` | `keys.yaml` | `SHIELD_AGENT_KEY_STORE_PATH` | 公開鍵ファイルのパス |
| `security.did_blocklist` | `[]` | — | ブロックするDIDのリスト |
| `storage.db_path` | `shield-agent.db` | `SHIELD_AGENT_DB_PATH` | SQLite DBのパス |
| `storage.retention_days` | `30` | `SHIELD_AGENT_RETENTION_DAYS` | ログ保持日数 |
| `server.monitor_addr` | `127.0.0.1:9090` | `SHIELD_AGENT_MONITOR_ADDR` | モニタリング/UIアドレス |
| `logging.level` | `info` | `SHIELD_AGENT_LOG_LEVEL` | `debug`/`info`/`warn`/`error` |
| `logging.format` | `json` | `SHIELD_AGENT_LOG_FORMAT` | `json` / `text` |

### ミドルウェア設定

```yaml
middlewares:
  - name: auth
    enabled: true
  - name: guard
    enabled: true
    config:
      rate_limit_per_min: 60          # メソッドごとの分あたりリクエスト制限
      max_body_size: 65536            # 最大リクエストサイズ（バイト）
      ip_blocklist: ["203.0.113.0/24"]
      ip_allowlist: ["10.0.0.0/8"]    # 空の場合はすべて許可
      brute_force_max_fails: 5        # 5回連続失敗で自動ブロック
      validate_jsonrpc: true          # 不正なJSON-RPCを拒否
  - name: token
    enabled: false                    # トークンミドルウェア（必要時に有効化）
  - name: log
    enabled: true
```

### CLIフラグ

| フラグ | 説明 |
|--------|------|
| `--config <path>` | 設定ファイルのパス（デフォルト: `shield-agent.yaml`） |
| `--log-level <level>` | ログレベル |
| `--verbose` | `--log-level debug` のエイリアス |
| `--monitor-addr <addr>` | モニタリングアドレス |
| `--disable-middleware <name>` | ミドルウェアを無効化 |
| `--enable-middleware <name>` | ミドルウェアを有効化 |

### SIGHUPホットリロード

プロセス再起動なしで設定を変更:

```bash
kill -HUP $(pgrep shield-agent)
```

ミドルウェアチェーン、keys.yaml、DIDブロックリストがリロードされます。

---

## モニタリング

### エンドポイント

| パス | 説明 |
|------|------|
| `/healthz` | ヘルスチェック（`healthy` / `degraded`） |
| `/metrics` | Prometheusメトリクス |
| `/ui` | Web UIダッシュボード |

### Prometheusメトリクス

| メトリクス | タイプ | ラベル |
|-----------|--------|--------|
| `shield_agent_messages_total` | Counter | `direction`, `method` |
| `shield_agent_auth_total` | Counter | `status` |
| `shield_agent_message_latency_seconds` | Histogram | `method` |
| `shield_agent_child_process_up` | Gauge | — |
| `shield_agent_rate_limit_rejected_total` | Counter | `method` |

### ログ照会CLI

```bash
shield-agent logs                              # 直近50件
shield-agent logs --last 100                   # 直近100件
shield-agent logs --agent <id> --since 1h      # 特定エージェント、直近1時間
shield-agent logs --method tools/call           # 特定メソッド
shield-agent logs --format json                # JSON出力
```

---

## Web UI

`http://localhost:9090/ui` にアクセス。

- **初期パスワード**: `admin`（初回ログイン時に変更を強制）
- **ダッシュボード**: リアルタイムのリクエスト数、エラー率、平均レイテンシ
- **ログ**: フィルタリング可能な監査ログテーブル
- **トークン管理**: トークンの発行/失効/使用量統計
- **ミドルウェア**: on/offトグル（再起動後も維持）
- **Agent Keys**: 公開鍵の登録/削除（keys.yamlなしでWeb UIで管理）
- **Upstreams**: Gatewayモードのupstream登録/編集/削除

---

## ロードマップ

詳細は [ROADMAP.md](ROADMAP.md) を参照してください。

| Phase | 状態 | 説明 |
|-------|------|------|
| Phase 1 — Core MVP | **完了** | Transport, Auth, Guard, Log, Middleware Chain |
| Phase 2 — デプロイ & インストール | **完了** | Docker, Homebrew, GoReleaser, CI/CD |
| Phase 3 — トークン & Web UI | **完了** | トークン管理、Web UIダッシュボード |
| Phase 3.5 — Gateway & DID | **完了** | マルチupstreamルーティング、DIDブロックリスト、verifiedモード |
| Phase 4 — 高度化 | 予定 | エージェント評判、プロトコル自動検出、WebSocket |

---

## ライセンス

[MIT](LICENSE)
