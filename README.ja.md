# 🐼 OpenPanda

**Open** **P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant

> あらゆるデバイス、あらゆる演算力、ひとつのコマンド。
> あなたが持つ異種のデバイスを、ピアツーピアのノードネットワークとして
> 横断する、個人向けタスクオーケストレーションアシスタント。

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.26-blue)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-blue)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)
![Status](https://img.shields.io/badge/status-pre--release-yellow)

---

## 目次

- [OpenPandaとは](#pandaとは)
- [主な機能](#主な機能)
- [アーキテクチャ](#アーキテクチャ)
- [インストール](#インストール)
- [はじめに](#はじめに)
- [使い方](#使い方)
- [CLI リファレンス](#cli-リファレンス)
- [設定](#設定)
- [ドキュメント](#ドキュメント)
- [テスト](#テスト)
- [デプロイ](#デプロイ)
- [技術スタック](#技術スタック)
- [ロードマップ](#ロードマップ)
- [コントリビューション](#コントリビューション)
- [ライセンス](#ライセンス)
- [謝辞](#謝辞)

## OpenPandaとは

OpenPanda は**また一つの Agent CLI ではありません**——それらの**上流**に位置するレイヤー、あなたのすべてのデバイスとすべてのツールの執事です。

Claude Code、Codex、OpenCode、OpenClaw……どれも単一マシン上の強力な Agent です。OpenPanda はそれらと競合せず、**雇用**します。どのデバイスで話しかけても、そのデバイスが総司令となり、自分でできることはその場でこなし、できない場合はネットワーク越しに本当にできるノードへルーティングします。そのノード自身の Agent（Claude Code、Codex など）に任せるか、単なるデバイス操作（サーボの制御など）なら Agent を介さず直接実行します。

```
サブエージェント（単一マシン）    Agent オーケストレーション（単一マシン）   OpenPanda（複数デバイス）
┌──────────────┐          ┌──────────────┐          ┌──────────────────────┐
│ Claude Code  │          │ マルチエージェント│          │ 異種混合デバイス群       │
│ Codex …      │          │ オーケストレーション│          │ + 各デバイスの Agent   │
│              │          │              │          │ + 生のハードウェア      │
└──────────────┘          └──────────────┘          └──────────────────────┘
                これらすべての上流：OpenPanda が委譲し、彼らが実行する
```

実際の使い方：どのデバイスからでも一度指示を出すだけで、OpenPanda がタスクを最適なノードに委譲し、結果を返し、学んだことを次回のために記憶します。プロジェクトの作業は個人の記憶から厳密に隔離されているため、「アシスタントがダークテーマ好きを知っている」せいでコードベースが暴走することはありません。

設計の根幹は**個人用**システムです。クラウド依存なし、記憶はあなたのデバイスにだけ残り、各ノードはあなたが管理する直接のWebSocketリンクでピアと通信します。

## 主な機能

- **異種ノードネットワーク** — 各ノードが能力カード（capability card）で実力（CPUクラス、シェル、Agentアダプタ）を宣言。ネットワークはタスクを本当に実行できるノードにルーティングします。ノートPC、SBC、デスクトップ、その他あらゆる階層のデバイス向けに設計されています。
- **統一エントリモデル** — 1つのプロンプト入力に対して3つのインテントを出力：`answer`（純粋なLLM応答）、`tool_call`（あなたのツール）、`task`（ノードへ委譲）。自動インテント分類と、フォールバックによる穏やかな劣化。
- **3層の能力実行** — `native`（シェル直接実行）、`agent`（アダプタ経由のAgent、例：Anthropic互換エンドポイント経由のClaude Code）、`manual`（キューに入れてあなたの承認/手動実行を待つ）。
- **P2P委譲プロトコル** — WebSocket + JSON上で、冪等な`task_id`キーと実行ごとに一意な`attempt_id`を使用。クラッシュ後の再試行が二重実行されることはありません。
- **自己進化するスキル** — `SKILL.md`ファイルによるプロシージャル記憶：各スキルは適用条件と実行方法を宣言し、使用ごとに洗練できます。
- **日常アシスタントツール** — エージェントはシステム時計の読み取り、リアルタイム天気の取得、そして**リマインダーの設定**（`reminder.set`）が可能。SQLite に保存され、スキャナーが発火し、Web Push 通知と SSE ライブ更新で開いているコンソールに届きます。CLI からは `panda reminder list/add/rm`。
- **MCP 統合** — config.yaml（`mcp.command`）またはコンソールの設定ページで stdio MCP サーバーを 1 台設定でき、そのツールはデーモン再起動なしでエージェントのツールセットに**ホットロード**されます。
- **2層メモリ** — ユーザー単位・プロジェクト単位で分離された記憶（`USER.md` / `MEMORY.md`形式）を隔離壁の背後に保持。さらにバックグラウンドの**Dreaming**エンジンが、ノードがアイドルの間に日次ログを長期記憶へ統合します。
- **音声入力** — オプションのサイドカーパイプライン（ウェイクワード → STT → LLM → TTS）。ハードウェアゲート付きで、組み込みマイク向けに準備されています。
- **対話型 REPL + 内蔵 Web コンソール** — `panda repl` が操作席：素の入力は ask エンジンへ、スラッシュコマンド（`/tasks`、`/approve`、`/projects`、`/nodes`、`/lang`…）でパネルを駆動し、`/web` で内蔵コンソールをワンクリック起動。タスクキューは**カンバンボード**（未着手/進行中/承認待ち/完了）でインライン承認対応。チャット、リマインダー、編集可能なメモリページ（USER/MEMORY/DREAMS）、設定ページ（モデルエンドポイント：Anthropic/OpenAI 互換、MCP サーバー）も同梱。`panda web` はワンコマンドで起動：デフォルトでループバック + 一時トークン、ブラウザがログイン済みで開きます。UI 言語は 5 種類。
- **セルフアップデート** — `panda web`（および `/web`）はバックグラウンドでリリースチャネルを確認し、コンソールが利用可能な更新をダウンロード・検証して、タスクキューがアイドルになると 1 クリックでインストールします。ダウンロード済みの更新を破棄しても何も残りません。
- **防御と安全レイヤ** — 権限Tier、サーキットブレーカー、スコープ逸脱検出と無限ループ検出。実行側の強化：サンドボックス、ネットワーク許可リスト、シークレットの秘匿化、監査ログ。
- **スリム設計** — 定常RSSは約 **21–23 MB**（`make measure` 実測）。リソース制約のあるシングルボードコンピュータで動くことを前提に設計されています。
- **クリーンなクロスコンパイル** — プラットフォームごとに単一の静的バイナリ、CGO不要（`modernc.org/sqlite`による純Go SQLite）。

## アーキテクチャ

```
                        ┌───────────────────────────┐
                        │  あなた：CLI / Web / 音声  │
                        └─────────────┬─────────────┘
                                      │
                 ┌────────────────────▼────────────────────┐
                 │            entry · panda ask             │
                 │   分類：answer | tool_call | task        │
                 └────────────────────┬────────────────────┘
                                      │  WebSocket + JSON で委譲
                       ┌──────────────┴───────────────┐
                       │                              │
          ┌────────────▼────────────┐     ┌────────────▼────────────┐
          │        Workerノード     │     │        Workerノード     │
          │   例：ノートPC (Standard)│     │ 例：シングルボード (Micro)│
          └─────────────────────────┘     └─────────────────────────┘
```

各ノードの内部:

```
┌─────────────────────────────────────────────────────────────┐
│ cmd/panda      デーモン + CLI（ask / status / queue / task…） │
├─────────────────────────────────────────────────────────────┤
│ entry          統一エントリモデル（answer · tool_call · task）│
│ scheduler      委譲とルーティングの決定                       │
│ commander      3層実行：native · agent · manual              │
│ defense        権限Tier · サーキット · 逸脱 · ループ検出      │
│ security       サンドボックス · 許可リスト · 秘匿化 · 監査    │
│ memory         USER/MEMORYストア + Dreamingエンジン          │
│ skills         SKILL.md プロシージャル記憶                   │
├─────────────────────────────────────────────────────────────┤
│ bus            WebSocketトランスポート + メッセージエンベロープ│
│ ledger         能力ディレクトリ（カード、ハートビート）       │
│ storage        SQLite（WAL）+ マイグレーション               │
│ log / util     構造化JSONログ、UUIDv7                       │
└─────────────────────────────────────────────────────────────┘
```

## インストール

1 行でリリースバイナリを入手できます。macOS / Linux / Windows 対応、一貫した体験で root 不要。インストーラーは対応するリリースアーカイブをダウンロードして SHA-256 を検証し、バイナリと agent アダプター（`adapters/*.py`）をユーザー単位のプレフィックスに展開し、`panda` を `PATH` にリンクします。

| プラットフォーム | コマンド |
|---|---|
| macOS / Linux | `curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh \| sh` |
| macOS（Homebrew） | `brew tap Xustalis/openpanda && brew install openpanda` |
| Windows（PowerShell） | `Set-ExecutionPolicy -Scope Process Bypass` を実行後、`irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 \| iex` |

フラグでデフォルトを上書きできます：

```bash
sh scripts/install.sh --version 0.0.7           # バージョン指定（デフォルト latest）
sh scripts/install.sh --prefix /opt/openpanda   # カスタムインストール先
sh scripts/install.sh --yes                     # 自動起動も登録（確認なし）
sh scripts/install.sh --no-service              # 自動起動は一切しない
```

macOS / Linux では XDG 規約に沿ったユーザー単位のプレフィックスに配置されます：

```
${XDG_DATA_HOME:-~/.local/share}/openpanda/
├── bin/panda            # 実体バイナリ
├── adapters/*.py        # agent アダプター（daemon がタスク委譲に必要）
├── config.example.yaml
└── capabilities.example-*.yaml
```

`~/.local/bin/panda` はそのバイナリへのシンボリックリンク（`PATH` に含まれます）。シェルが `~/.local/bin` を含まない場合は、スクリプトが追加すべき `export PATH` の行を表示します。Windows では `%LOCALAPPDATA%\OpenPanda\` に展開され、その `bin` がユーザー `PATH` に追加されます。インストーラーは自動起動サービス（ログイン時の `panda daemon`）も登録できますが、先に `panda init` を実行してから有効にしてください。設定がないと daemon は起動しません。

インストール後は、初期化して実行します：

```bash
panda init      # 対話形式で config.yaml とケイパビリティカードを生成
panda doctor    # 自己診断：バイナリ / PATH / 設定 / アダプター / agent
panda repl      # 対話型 REPL に入る
panda web       # 内蔵 Web コンソールを開く（ループバック、自動ログイン）
```

完全に削除するには：

- macOS / Linux：`rm -rf ~/.local/share/openpanda ~/.local/bin/panda`（先に自動起動を停止）。
- Windows：`%LOCALAPPDATA%\OpenPanda` を削除し、ユーザー `PATH` から対応する `bin` を削除。

詳しいガイドとトラブルシューティングは [docs/install.md](docs/install.md) を参照してください。

## はじめに

### 前提条件

| ツール | バージョン |
|---|---|
| Go | 1.26.5+ |
| Python | 3.10+（Agentアダプタ / 音声サイドカー） |
| make | 新しいバージョンであれば可 |

### ビルド

```bash
make build          # ネイティブバイナリ → bin/panda（リリース、strip済み）
make web            # 内蔵Webコンソールをバイナリに組み込み（node/npm 必須。省略するとヒントページ表示）
make test           # 全テストスイートを実行
make vet            # 静的解析
```

実際に使うデバイス向けにクロスコンパイル:

```bash
make build-linux-arm64   # → bin/panda-linux-arm64  （SBC・組み込みボード向け）
make build-linux-amd64   # → bin/panda-linux-amd64
make build-darwin-arm64  # → bin/panda-darwin-arm64
make build-windows-amd64 # → bin/panda-windows-amd64.exe
```

### 設定

対話形式で1発初期化 — モデルエンドポイント、ノード名、ケイパビリティカードをまとめて生成:

```bash
./bin/panda init
```

またはサンプル設定をコピーして、ノードごとに編集:

```bash
cp config.example.yaml /etc/openpanda/config.yaml   # またはローカルに置いて --config で指定
```

設定は小さく、自己説明的です。もっとも重要な2箇所:

```yaml
network:
  listen_addr: ":7836"        # WebSocketリスナー
  shared_secret: "..."        # ノード間のHMAC認証 — 全ノードが同じ値を共有
  peers:                      # ネットワーク内の他のノード
    - "worker-1.your-tailnet.ts.net:7836"
model:
  base_url: "https://api.deepseek.com/anthropic"  # 任意の /v1/messages 互換エンドポイント
  model: "deepseek-v4-flash"
  # api_key: ""               # 環境変数 OPENPANDA_MODEL_API_KEY を推奨
```

シークレット（モデルのAPIキー）は、可能な限り設定ファイルではなく`OPENPANDA_MODEL_API_KEY`環境変数から読み取ります。

### 起動

システム全体を最速で見る方法は、ワンコマンドのWebコンソール：ループバック + 一時トークンで、ブラウザがログイン済みで開きます——設定編集もトークンの貼り付けも不要：

```bash
./bin/panda web
```

モデルのエンドポイント（Anthropic / OpenAI 互換）は、未設定ならコンソールの設定ページから直接管理できます。

常駐マルチノード構成ではデーモンそのものを起動します：

```bash
./bin/panda daemon --config config.yaml --card config/capabilities.example-desktop.yaml
```

タスクを*実行*できる各ノードは、能力カード付きで起動してください。カードなしのノードはハートビートには参加しますが、タスクは割り当てられません。

## 使い方

何でも質問——エントリモデルが応答・ツール・委譲を自動判断:

```bash
./bin/panda ask "この1週間のgit logを要約して"
```

ネットワークとキューを確認:

```bash
./bin/panda status
./bin/panda queue
```

タスクの詳細表示 / キャンセル:

```bash
./bin/panda task <task-id>
./bin/panda cancel <task-id>
./bin/panda logs <task-id>
```

スキルの管理:

```bash
./bin/panda skill
```

## CLI リファレンス

| コマンド | 説明 |
|---|---|
| `panda`（引数なし） | 対話型REPLを起動（`panda repl`と同じ）；デーモンは `panda daemon` サブコマンドに移行 |
| `panda daemon [--config PATH] [--card PATH]` | デーモン起動：ノード登録、ハートビート、WSサーバー、ピア再接続 |
| `panda ask [--config PATH] [--card PATH] [--authorize] [--output-format json \| stream-json] "<質問>"` | 統一エントリ：answer / tool_call / task / plan に分類して実行；`--output-format` はヘッドレス用途に 1 つの JSON オブジェクトまたは NDJSON イベントを出力 |
| `panda plan run <ファイル.yaml> \| show <id> \| example` | クロスデバイス多段パイプライン：段階はただのタスク（キュー投入、ハードウェア別ルーティング、再試行、レビュー停泊）で、plan が順序を与え、前段階の作業ディレクトリを次のマシンの段階へ渡す。`run --dry-run` は作成せずルーティングのみ検証・印刷 |
| `panda voice [--once] [--mute]` | デスクペット入口：ウェイクワード → ASR → 同じ入口パイプライン → TTS。キーボードのないデバイス向け。`--once` は 1 発だけ処理、`--mute` は朗読の代わりに印刷 |
| `panda repl [--config PATH] [--card PATH]` | 対話シェル：スラッシュコマンド（tasks/approve/projects/nodes/lang）、素の入力は ask エンジンへ、`/web` で組み込みコンソールを起動 |
| `panda web [--config PATH] [--card PATH] [--no-browser]` | コマンド1つで Web コンソール：デフォルトはループバック + 一時トークン、ブラウザが開いた時点でログイン済み |
| `panda session list \| new \| show \| rm \| ask \| diff \| merge` | git worktree ベースのチャットセッション：`new [--title T]` がリポジトリに worktree を切り出し、`ask <id> <prompt>` で継続、`diff <id>` で変更を確認、`merge <id> [--message M]` でブランチを HEAD にマージ |
| `panda init` | 対話形式の初回セットアップ：`config.yaml` + `capabilities.yaml` を生成（モデルエンドポイント、ノード名、ハードウェアスキャンの既定値） |
| `panda install [--dir PATH] [--no-path]` | `panda` をグローバルコマンドとして PATH に登録（再起動後も有効）、インストール済みコピーの自動検証付き |
| `panda uninstall [--config PATH] [--yes] [--no-backup] [--dry-run] [--purge]` | 安全なアンインストール：計画を表示 → `confirm` 入力で二次確認、ホワイトリスト削除のみ、ユーザーアセット（projects/memory/skills）は常に保持、zip バックアップとレポートを生成；`--purge` はユーザーデータも削除し、さらにもう一度の確認が必要 |
| `panda doctor [--config PATH]` | セルフチェック：インストール済みコピーの実行、PATH 解決、永続化、設定/DB の可用性 |
| `panda status` | ノードとタスクの状態 |
| `panda nodes` | 現在および既知のノード（status と同じデータ） |
| `panda nodes add <host:port>` | ダイヤルするピアを追加（`shared_secret` 未設定時は生成し、相手マシンの参加ガイドを表示）——再起動なしで即時ダイヤル |
| `panda nodes invite` | ピアリストを変更せずに参加ガイドを表示 |
| `panda nodes disconnect <addr>` | ダイヤルリストからピアを削除 |
| `panda nodes remove <id>` | ライブピアが存在しない古いディレクトリ行を削除；自ノードとオンラインノードは拒否される（再登録されるため） |
| `panda pair --secret S --peer <host:port>` | 新しいマシンから既存ネットワークに参加：共有シークレットとピアをこのノードの設定に書き込む |
| `panda queue` | タスクキューの一覧 |
| `panda task [--config PATH] <task-id>` | タスクの詳細 + タイムライン |
| `panda task add --title T [--prompt P] [--priority レベル] [--project p] [--authorize]` | タスクを手動でキューに投入（`--card` が必要）；優先度は `low \| medium \| normal \| high \| critical` |
| `panda task priority <id> <level>` | タスクの優先度を変更 |
| `panda task move <id> <seq>` | ドラッグソートキューを並べ替え |
| `panda cancel [--config PATH] <task-id>` | タスクをキャンセル（実行ノードへカスケード） |
| `panda approve [--config PATH] <task-id>` | レビュー中のタスクを承認（review → done） |
| `panda reject [--config PATH] [--reason s] <task-id>` | レビュー中のタスクを却下 |
| `panda logs [--config PATH] <task-id>` | タスク実行ログ |
| `panda skill list \| approve <name> \| reject <name>` | スキルストアの管理 |
| `panda reminder list \| add \| rm` | リマインダー：一覧 / 追加（`--after 10m` または `--at "2006-01-02 15:04"`）/ 削除 |
| `panda memory list \| get \| set \| rm` | メモリファイル：`user \| memory \| dreams \| topic:<n> \| project:<n> \| daily:<date>`（`set` はデフォルトで stdin を読み、`--file F` も可；dreams と daily は読み取り専用） |
| `panda project list \| create` | プロジェクトメモリ |
| `panda config <セクション> <get \| set \| test>` | `config.yaml` の表示/編集（コメント保持）：セクションは `model \| mcp \| limits \| routing \| injection \| approval`；変更は daemon/パネル再起動後に反映 |
| `panda detect [-o PATH]` | このマシンのハードウェア（CPU/RAM/GPU/Agent CLI）をスキャンして capabilities.yaml のドラフトを生成 |
| `panda card show \| rescan \| edit \| set` | このノードの能力カード：内容と読み込み元のパスを表示、ハードウェアとインストール済み Agent CLI を再スキャン（`rescan` は差分のみ表示、`--write` で適用し `.bak` を残す）、`$EDITOR` で編集、`set <フィールド>=<値>` でエディタ無しに 1 項目だけ変更。探測されたハードウェア項目は上書きされ、人が決めた項目（ノード名・resource_class・max_concurrent_tasks・agent の tier・native/manual 能力）は保持されます |
| `panda card native \| agent \| manual add \| remove \| set` | CLI からの構造化カード編集：エディタと同じ検証器 + `.bak` + 原子書き込みパイプラインで、稼働中の daemon にホットリロード |
| `panda metrics [--csv]` | 委譲メトリクスをエクスポート |
| `panda audit verify \| entries [--task <id>]` | `verify` は監査ログ（または単一タスクイベント）の `prev_hash` チェーンを検証；`entries` は監査証跡行を出力 |
| `panda version` | バージョンを表示 |

## 設定

| セクション | キー | 意味 |
|---|---|---|
| `node` | `name` | 一意なノードID（ネットワーク全体で使用） |
| `node` | `resource_class` | `Micro` \| `Standard` \| `Full` → スケジューラTier |
| `network` | `listen_addr` | WebSocketリスナーアドレス |
| `network` | `shared_secret` | ノード間ハローを認証するHMACシークレット；WSリスナーはこれが無いと起動しない（全ノード共通の値） |
| `network` | `max_connections` | グローバル同時WS接続数の上限（0 = 無制限） |
| `network` | `max_connections_per_ip` | リモートIPごとの同時WS接続数の上限（0 = 無制限） |
| `network` | `panel_addr` | WebコンソールのHTTPアドレス（`panda web` / `/web`）。デフォルト `127.0.0.1:7840` |
| `network` | `panel_token` | コンソールの`/api/*`を守るBearerトークン（ループバックでは一時トークンを自動生成。`OPENPANDA_PANEL_TOKEN`を推奨） |
| `network` | `peers` | 接続する手動ピアアドレス |
| `storage` | `db_path` | SQLiteデータベースのパス |
| `storage` | `context_path` | コンテキストスナップショットストア |
| `storage` | `memory_path` | 個人メモリのルート |
| `storage` | `projects_path` | プロジェクト単位メモリのルート |
| `storage` | `skills_path` | プロシージャル記憶のルート |
| `storage` | `work_path` | Agentの実行ディレクトリ；スコープ逸脱はここで計測 |
| `storage` | `artifact_path` | パック済みタスク成果物プール（ハッシュ命名；ステージ出力はここ経由でノード間を渡る） |
| `log` | `level` | `debug` \| `info` \| `warn` \| `error` |
| `model` | `base_url` | Anthropic互換Messages APIのベースURL |
| `model` | `model` | モデルID（例：`deepseek-v4-flash`） |
| `model` | `api_key` | シークレット — `OPENPANDA_MODEL_API_KEY`を推奨 |
| `model` | `api_type` | `anthropic` \| `openai`（デフォルト `anthropic`） |
| `model` | `max_tokens` | 補完トークン上限（デフォルト4096） |
| `injection` | `model` | Agent サブプロセスへのモデル注入：`auto`（デフォルト——Agent 自身がモデル資格情報を持たない場合のみ注入） \| `always` \| `never` |
| `routing` | `preferred_agents` | ルーティングスコア +0.5 のボーナスが付与される Agent 名 |
| `memory` | `limits.user` | USER.md の文字数上限（デフォルト 5000） |
| `memory` | `limits.memory` | MEMORY.md の文字数上限（デフォルト 10000） |
| `memory` | `limits.project` | プロジェクトごとの MEMORY.md 文字数上限（デフォルト 30000） |
| `approval` | `mode` | タスク承認ゲート：`always` \| `on-request`（デフォルト——モデルがリスクありとマークしたタスクのみ） \| `never` |
| `timeouts` | `task_lease_s` | タスク 1 回の試行がリースを保持できる時間（デフォルト 1200）；`agent_s` より十分大きくする必要がある |
| `timeouts` | `agent_s` | Agent アダプター 1 回実行の実時間バジェット（デフォルト 600） |
| `timeouts` | `supervise_rounds` | タスクごとの 実行 → 判定 → 再委譲 ループの上限（デフォルト 5） |
| `mcp` | `command` | stdio MCP サーバーのコマンドライン（空 = 無効）。ツールはエージェントのツールセットにホットロード |
| `push` | `enabled` | `/api/push/*`の提供とWeb Push送信を有効化（内蔵コンソール + webuiサイドカー） |
| `push` | `vapid_subject` | VAPIDサブジェクト（例：`mailto:`アドレス） |
| `push` | `vapid_key_path` | VAPIDキーのパス（初回起動時に自動生成） |

設定の読み込み順序: `--config`フラグ > 環境変数 > 既定 `/etc/openpanda/config.yaml`。

## ドキュメント

完全なドキュメントは[`docs/`](docs/)ディレクトリにあります：

- [ドキュメントインデックス](docs/README.md) — 公開ドキュメントへの入り口。
- [コントリビューションガイド](CONTRIBUTING.md) — ツールチェーン、品質ゲート、コード規約、PRチェックリスト
  （日本語版：`CONTRIBUTING.ja.md`、他言語版は `CONTRIBUTING.zh-CN.md` / `CONTRIBUTING.es.md` / `CONTRIBUTING.de.md`）。
- [デスクトップ＆パッケージングロードマップ](docs/plans/roadmap-desktop-and-packaging.md) — ネイティブデスクトップクライアント、署名付きインストーラ、公証、自動更新に向けた段階計画。

## テスト

```bash
make test        # 全スイート
make vet         # go vet
```

主要なプロトコル不変条件は、実際の2ノードWebSocketテストでカバーされています（Tailscale不要）:

```bash
go test ./internal/core/ -run 'TestTwoNodeProtocol|TestDelegateIdempotent|TestCancelPropagates' -v
```

## デプロイ

### ネットワークセキュリティ基盤

OpenPandaのノードはデフォルトで平文 WebSocket（`ws://`）で通信します。**平文 WebSocket は信頼できるプライベート経路でのみ使用してください：**

- ループバック / 同一ホスト接続（例：`127.0.0.1`、`localhost`）。
- **Tailscale** や VPN など、自分が管理するプライベートオーバーレイネットワーク。
- 全デバイスが信頼できる物理的に隔離された LAN。

**いずれかの OpenPanda ピアが公衆インターネットを経由する場合は、WebSocket リスナーの手前で TLS を終端してください**（nginx、Caddy、Traefik など）。ピアには `wss://` URL を設定します。`shared_secret` はノード間の hello を認証するもので、トランスポート暗号化の代わりには*なりません*。平文 `ws://` リスナーを公衆インターネットに公開しないでください。

`panel_addr` の Web コンソールは平文 HTTP で Bearer トークンを運びます（ループバックでは一時トークンが自動生成されます）。ループバックに留めるか、同じ TLS リバースプロキシの背後に配置してください。

### メモリフットプリント

OpenPandaは低消費電力デバイスをターゲットにしています。ハードウェアへ載せる前に、定常メモリを検証してください——`ps`の1回のサンプリングはGCノイズで不正確なため、複数回測定します:

```bash
make build
for i in 1 2 3 4 5; do
  ./bin/panda daemon --config testdata/node-a.yaml >/dev/null 2>&1 &
  PID=$!; sleep 3
  ps -o rss= -p $PID | awk '{printf "%d MB\n", $1/1024}'
  kill -TERM $PID; wait $PID 2>/dev/null
done
```

## 技術スタック

| レイヤ | 選択 |
|---|---|
| コアデーモン | Go（modernc.org/sqlite — 純Go、CGOなし） |
| グルー / アダプタ | Python 3.10+ |
| トランスポート | WebSocket + JSONエンベロープ |
| 状態 | WALモードのSQLite |
| フロントエンド | Webコンソール（Vite + Preact、`go:embed`で単一バイナリ） |
| LLMアクセス | Anthropic互換`/v1/messages`またはOpenAI互換エンドポイント（例：DeepSeek） |

## ロードマップ

Phase 0–3（エントリモデル・P2P委譲・メモリ/音声/実行の強化・カーネル/コンソール/REPLの再構築＋実機2ノード検証）は完了。Phase 4（デスクトップクライアント＋署名付きインストーラパイプライン＋自動更新機構＋リリースチャネル）については[デスクトップ＆パッケージングロードマップ](docs/plans/roadmap-desktop-and-packaging.md)に詳しく計画されています。

## コントリビューション

コントリビューション歓迎です。コードベースの一貫性を保つため、Pull Requestの前に以下のエンジニアリングゲートを守ってください:

- `make vet && make test` が通ること。
- `gofmt -l internal/ cmd/ adapters/` の出力が空であること。
- コアモジュールのテストカバレッジを可能な限り約60%以上に保つこと。

完全な規約は[コントリビューションガイド](CONTRIBUTING.md)を参照してください：エラーラッピング（`%w` / `errors.Is`）、複雑度制限、デッドコード禁止、並行処理ルール、i18n規約、コミットスタイル。各言語版ガイド：[`CONTRIBUTING.ja.md`](CONTRIBUTING.ja.md)、[`CONTRIBUTING.zh-CN.md`](CONTRIBUTING.zh-CN.md)、[`CONTRIBUTING.es.md`](CONTRIBUTING.es.md)、[`CONTRIBUTING.de.md`](CONTRIBUTING.de.md)。

## ライセンス

[MITライセンス](LICENSE)の下で公開されています。

## 謝辞

分散マルチエージェントスケジューリング理論と、Hermes・OpenClawのメモリパターンに触発されています。Xenithが制作。
