# 🐼 OpenPanda

**オープンソース・ローカルファーストのパーソナル AI エージェント OS ＆ 分散デバイス・オーケストレーター**

> すべてのデバイスをプライベートな P2P メッシュで接続し、孤立した端末 AI エージェント（Claude Code、Codex、Grok 等）を「雇用」して、マシンを跨いだ協調エンジニアリングチームを編成します。

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![Release: v0.0.8-preview](https://img.shields.io/badge/release-v0.0.8--preview-blue.svg)](https://github.com/Xustalis/OpenPanda/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.26-00ADD8)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-3776AB)
![Platforms](https://img.shields.io/badge/プラットフォーム-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)
![Memory](https://img.shields.io/badge/常駐メモリ-~20MB%20RSS-brightgreen)
![Local First](https://img.shields.io/badge/クラウド依存-ゼロ%20(完全ローカル)-success)

---

## ⚡ なぜ OpenPanda なのか？

最新の AI コーディングアシスタント（**Claude Code、OpenAI Codex、Grok Build、OpenCode** 等）は極めて強力ですが、**「単一マシンの単一ターミナル内に閉じ込められている」**という共通の制約を抱えています。

しかし、実際の開発環境は多様なマシンで構成されています：
- アイデアの入力やコードレビューを行う軽量な **MacBook**
- 重いビルド、Docker、学習を行う強力な CPU/GPU を備えた **Linux ワークステーション / ホームサーバー**
- 常時稼働でバックグラウンドジョブや IoT センサーを扱う **Raspberry Pi などの SBC**

**OpenPanda はこの断絶を解消します。** 既存のエージェントツールを置き換えるのではなく、それらを**「雇用」して統括**します：

```
┌─────────────────────────────────────────────────────────────┐
│                      あなた：1 つの指示                      │
│             (ターミナル TUI / Web コンソール / 音声)         │
└──────────────────────────────┬──────────────────────────────┘
                               │
                  ┌────────────▼────────────┐
                  │     🐼 OpenPanda OS     │
                  │   意図解析・最適ルーティング │
                  │   実行監督・セキュリティ制御 │
                  └────────────┬────────────┘
                               │ 直接 P2P WebSocket (クラウド不要)
     ┌─────────────────────────┼─────────────────────────┐
     │                         │                         │
┌────▼──────────────┐   ┌──────▼────────────┐   ┌────────▼────────────┐
│  MacBook (Worker) │   │  Linux Build Box  │   │  Raspberry Pi / SBC │
│  - 高速テスト     │   │  - 重量級ビルド   │   │  - GPIO / センサー  │
│  - Claude Code    │   │  - Codex / Docker │   │  - 24時間常駐デーモン│
└───────────────────┘   └───────────────────┘   └─────────────────────┘
```

**どのデバイスからでも**一度指示を出すだけで、OpenPanda がタスクを解析し、最適な能力を持つマシンへ自動委譲。エージェントの実行を監督し、結果を検証して手元へストリーミング送信します。

---

## 🌟 主な機能と特徴

### 1. 🌐 異種マルチデバイスの P2P 協調
- **動的能力カード**: 各ノードのハードウェア仕様（CPU、GPU、RAM、OS）と利用可能なツールを自律検知。
- **インテリジェントなタスク配分**: 重いビルドは強力なサーバーへ、センサー操作はエッジボードへ自動ルーティング。
- **完全ローカル・クラウド中継なし**: 認証・暗号化された WebSocket で直接通信。コードやデータが私有デバイスの外へ漏れることはありません。

### 2. 🤖 あらゆるエージェントの統括 ＆ 自動フォールバック
- **主要エージェント対応**: Claude Code、OpenAI Codex、Grok Build、DeepSeek Harness、OpenCode、ローカルシェルに対応。
- **認証救済モデル注入**: Agent の API 残高不足や 401 エラー発生時、設定済みの代替モデルを自動注入してタスクを完遂。
- **完全透明な実行トレース**: Bash コマンド、ファイル編集、ツール呼び出しをリアルタイムにターミナルやブラウザへ表示。

### 3. 🛡️ 自律安全制御 ＆ 人間による承認ゲート（Human-in-the-Loop）
- **リスク段階評価**: 安全な可逆操作（コード読み込み、ローカルビルド、テスト実行）は全自動で完結。
- **不可逆操作の承認ゲート**: `git push`、データベース書き換え、ファイル削除などの高リスク操作は自動一時停止し、承認プロンプトを表示。
- **暴走防止サーキットブレーカー**: 無限ループ検知とリトライ制限により、無駄なトークン消費を防止。

### 4. 🧠 2層分離メモリ ＆ 自己進化型スキル
- **メモリの物理分離**: ユーザー個人設定（`USER.md`）とプロジェクト文脈（`MEMORY.md`）を厳格に分離。
- **自己改善型スキル**: 成功した作業手順を構造化された `SKILL.md` に蓄積し、使えば使うほど賢くなります。
- **文脈付きタスク移送**: マシン間でタスクを委譲する際、プロジェクト記憶と作業ツリーの要約が自動で同伴します。

### 5. 🖥️ 3 つの統一インターフェース
- **対話型ターミナル TUI**: Bubble Tea 製。矢印キー操作、リアルタイム進捗、実行中の方向転換（Mid-turn Steering）に対応。
- **ゼロ構成の Web コンソール**: タスクカンバン、リアルタイム SSE、モバイル対応ドロワー、自動ログイン。
- **スクリプト可能な CLI**: `panda ask "..."` など、シェルスクリプトや CI から直接呼び出し可能。

### 6. 🪶 超軽量（常駐メモリ約 20MB）
- 単一の純 Go 静的バイナリ（Pure Go SQLite WAL モード）。
- 20 ドルの SBC（Raspberry Pi）からハイエンドサーバーまで軽快に常駐動作。

---

## 🚀 クイックスタート（3分でセットアップ）

### ステップ 1: インストール

**macOS / Linux:**
```bash
curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh
```

**macOS (Homebrew):**
```bash
brew tap Xustalis/openpanda
brew install openpanda
```

**Windows (PowerShell):**
```powershell
irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
```

### ステップ 2: ノードの初期化

```bash
panda init
```
*対話式ウィザードでデバイス名、LLM プロバイダー（DeepSeek、Claude、OpenAI、Ollama 等）を設定します。*

### ステップ 3: 起動

- **対話型 TUI を起動:**
  ```bash
  panda
  ```
- **Web コンソールを起動（ブラウザが自動起動）:**
  ```bash
  panda web
  ```
- **コマンドラインから直接実行:**
  ```bash
  panda ask "システムの稼働状態と未完了タスクを表示して"
  ```

### 30 秒で 2 台目のデバイスを追加

MacBook と Linux ワークステーションを連携させる場合：
1. デバイス A で実行: `panda pair`（ペアリングコードを発行）
2. デバイス B で実行: `panda nodes add <デバイスAのアドレス>`
*これだけでプライベート P2P メッシュが構築されます！*

---

## 🛠️ コマンド一覧

| コマンド | 説明 |
|---|---|
| `panda` | フル機能の対話型 Bubble Tea TUI を起動 |
| `panda ask "<query>"` | 即時実行（直接回答・ツール呼び出し・タスク委譲） |
| `panda web` | 組み込み Web コンソールを起動して自動ログイン |
| `panda nodes` | ネットワーク内のオンラインデバイスと能力一覧を表示 |
| `panda pair` | 新規デバイス接続用のペアリングコードを表示 |
| `panda queue` | 待機中・実行中・承認待ちのタスクを表示 |
| `panda approve <id>` | 保留中の第 2 層高リスク操作を承認 |
| `panda project list` | ワークスペースプロジェクトと記憶の管理 |
| `panda doctor` | PATH、設定、アダプター、データベースのヘルスチェック |
| `panda version` | 現在のバイナリバージョンを表示 |

---

## 🤝 コミュニティと貢献

コミュニティからの貢献を歓迎します！新しい AI CLI アダプターの追加、スケジューラーの改善、TUI コンポーネントの機能向上など：

1. [CONTRIBUTING.ja.md](CONTRIBUTING.ja.md) で開発規約をご確認ください。
2. [SECURITY.md](SECURITY.md) でセキュリティ基準をご確認ください。
3. [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) 行動規範を遵守してください。
4. PR 提出前に `make gate` を実行してください。

---

## 📄 ライセンス

OpenPanda は [MIT License](LICENSE) のもとで公開されているオープンソースソフトウェアです。
