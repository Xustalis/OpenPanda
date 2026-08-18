# 🐼 OpenPanda

**Open** **P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant

> あらゆるデバイス、あらゆる演算力、ひとつのコマンド。
> あなたが持つ異種のデバイスを、ピアツーピアのノードネットワークとして
> 横断する、個人向けタスクオーケストレーションアシスタント。

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.22-blue)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-blue)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)
![Status](https://img.shields.io/badge/status-pre--release-yellow)

---

## 目次

- [OpenPandaとは](#pandaとは)
- [主な機能](#主な機能)
- [アーキテクチャ](#アーキテクチャ)
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
- **対話型 REPL + 内蔵 Web コンソール** — `panda repl` が操作席：素の入力は ask エンジンへ、スラッシュコマンド（`/tasks`、`/approve`、`/projects`、`/nodes`、`/lang`…）でパネルを駆動し、`/web` で内蔵コンソールをワンクリック起動。タスクキューは**カンバンボード**（未着手/進行中/承認待ち/完了）でインライン承認対応。チャット、リマインダー、メモリビューア（USER/MEMORY/DREAMS）、設定ページ（モデルエンドポイント：Anthropic/OpenAI 互換、MCP サーバー）も同梱。`panda web` はワンコマンドで起動：デフォルトでループバック + 一時トークン、ブラウザがログイン済みで開きます。UI 言語は 5 種類。
- **防御と安全レイヤ** — 権限Tier、サーキットブレーカー、スコープ逸脱検出と無限ループ検出。実行側の強化：サンドボックス、ネットワーク許可リスト、シークレットの秘匿化、監査ログ。
- **スリム設計** — 定常RSSは約 **13–20 MB**。リソース制約のあるシングルボードコンピュータで動くことを前提に設計されています。
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

## はじめに

### 前提条件

| ツール | バージョン |
|---|---|
| Go | 1.22+（モジュールは1.26.5をターゲット） |
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

サンプル設定をコピーして、ノードごとに編集:

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
  model: "deepseek-chat"
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
./bin/panda --config config.yaml --card config/capabilities.example-desktop.yaml
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
| `panda`（引数なし） | デーモン起動：ノード登録、ハートビート、WSサーバー、ピア再接続 |
| `panda ask [--config PATH] [--card PATH] [--authorize] "<質問>"` | 統一エントリ：answer / tool_call / task に分類して実行 |
| `panda repl [--config PATH] [--card PATH]` | 対話シェル：スラッシュコマンド（tasks/approve/projects/nodes/lang）、素の入力は ask エンジンへ、`/web` で組み込みコンソールを起動 |
| `panda web [--config PATH] [--card PATH] [--no-browser]` | コマンド1つで Web コンソール：デフォルトはループバック + 一時トークン、ブラウザが開いた時点でログイン済み |
| `panda install [--dir PATH] [--no-path]` | `panda` をグローバルコマンドとして PATH に登録（再起動後も有効）、インストール済みコピーの自動検証付き |
| `panda uninstall [--config PATH] [--yes] [--no-backup] [--dry-run]` | 安全なアンインストール：計画を表示 → `confirm` 入力で二次確認、ホワイトリスト削除のみ、ユーザーアセット（projects/memory/skills）は常に保持、zip バックアップとレポートを生成 |
| `panda doctor [--config PATH]` | セルフチェック：インストール済みコピーの実行、PATH 解決、永続化、設定/DB の可用性 |
| `panda status` | ノードとタスクの状態 |
| `panda queue` | タスクキューの一覧 |
| `panda task [--config PATH] <task-id>` | タスクの詳細 |
| `panda cancel [--config PATH] <task-id>` | タスクをキャンセル（実行ノードへカスケード） |
| `panda approve [--config PATH] <task-id>` | レビュー中のタスクを承認（review → done） |
| `panda reject [--config PATH] [--reason s] <task-id>` | レビュー中のタスクを却下 |
| `panda logs [--config PATH] <task-id>` | タスク実行ログ |
| `panda skill` | スキルストアの管理 |
| `panda reminder list \| add \| rm` | リマインダー：一覧 / 追加（`--after 10m` または `--at "2006-01-02 15:04"`）/ 削除 |
| `panda detect [-o PATH]` | このマシンのハードウェア（CPU/RAM/GPU/Agent CLI）をスキャンして capabilities.yaml のドラフトを生成 |
| `panda metrics [--csv]` | 委譲メトリクスをエクスポート |
| `panda audit [--task <id>]` | 監査ログまたは単一タスクイベントの `prev_hash` チェーンを検証 |
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
| `log` | `level` | `debug` \| `info` \| `warn` \| `error` |
| `model` | `base_url` | Anthropic互換Messages APIのベースURL |
| `model` | `model` | モデルID（例：`deepseek-chat`、`deepseek-reasoner`） |
| `model` | `api_key` | シークレット — `OPENPANDA_MODEL_API_KEY`を推奨 |
| `model` | `api_type` | `anthropic` \| `openai`（デフォルト `anthropic`） |
| `model` | `max_tokens` | 補完トークン上限（デフォルト4096） |
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

OpenPandaは低消費電力デバイスをターゲットにしています。ハードウェアへ載せる前に、定常メモリを検証してください——`ps`の1回のサンプリングはGCノイズで不正確なため、複数回測定します:

```bash
make build
for i in 1 2 3 4 5; do
  ./bin/panda --config testdata/node-a.yaml >/dev/null 2>&1 &
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

分散マルチエージェントスケジューリング理論（ATC-MARL）と、Hermes・OpenClawのメモリパターンに触発されています。Xenithが制作。
