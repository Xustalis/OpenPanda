# 更新履歴

[English](CHANGELOG.md) · [简体中文](CHANGELOG.zh-CN.md) · [日本語](CHANGELOG.ja.md) · [Español](CHANGELOG.es.md) · [Deutsch](CHANGELOG.de.md)

## [Unreleased]

## [0.0.2] - 2026-08-22

CLI ファーストのリリース：カーネル再設計が完了——すべての Web 機能に対応する CLI コマンドが揃い——REPL が製品の正玄関となり、CLI は会話メモリ・リアルタイムタスク報告・出力先ごとの Markdown レンダリングを獲得した。

### CLI 正玄関と REPL オーバーホール

- **CLI 正玄関**——素の `panda` は対話 REPL を開く（従来はヘッドレス daemon）；カーネルは明示的な `panda daemon` サブコマンドへ移動。systemd/LaunchAgent/Windows ランチャーと Makefile の実行ターゲットも明示的に daemon を起動するよう更新。
- **REPL マルチターンコンテキスト**——素の ask は 24k 文字を予算とする会話を蓄積し（古い交換から順に丸ごと退避するため、ユーザー発言がその回答と切り離されて再生されることはない）、`~/.local/state/openpanda/conversation.json` に永続化：新しく開いたターミナルは前回の続きから再開。`/new` でクリア、`/history` で閲覧、`!!` で直前のプロンプトを再実行、`panda ask --continue` で同じスレッドをワンショットで継続。
- **帯域外タスク報告**——対話 REPL はタスクウォッチャーを動かし、ストアの状態フィンガープリントをポーリングして、タスクが終状態に達したら（ボードの待機タスク、Web コンソール送信、ピア委譲）✓/✗ の 1 行を表示——入力中のバッファを壊さず行エディタに割り込む。インライン ask は自身の結果を吸収し、二重通知しない。
- **ライブタスクボード**——`panda queue --watch`（と REPL の `/tasks watch`）が 2 秒ごとにキューをその場で再描画、状態ごとに着色；Ctrl-C はプロセスではなくビューを終了。
- **スラッシュコマンドメニュー**——REPL で `/` を打つと、プロンプト下にマッチするコマンドをリアルタイム列挙（上限 10 件、(+N, Tab) ヒント付き）；補完自体は引き続き Tab。`/e` が `/exit ` に吸着し、バックスペースで再発火する補完ループを修正。
- **起動バナー刷新**——クラシックな figlet 書体で OpenPanda を純粋 ASCII で綴る（どの端末でも描画される）、ノード/モデル/作業ディレクトリの情報行付き；TTY のみ着色。
- **TTY/コンソール劣化**——素の Linux コンソール（TERM=linux）では UI が英語と ASCII セパレータにフォールバックし、菱形の文字化けを出さない；非 UTF-8 端末では `·` セパレータを `|` に置換。
- **能力カード自動発見**——`--card` は `./capabilities.yaml`、次に `/etc/openpanda/capabilities.yaml` を既定で探索し、インストール済みノードはゼロフラグでタスクを実行できる。
- **フラグ並べ替え**——フラグを位置引数の後に置ける（`panda task <id> --config x`）：Go の flag パッケージは末尾フラグを位置引数テキストに黙って飲み込んでいた；全サブコマンドが先頭へ持ち上げるよう修正。

### カーネル再設計——CLI がカーネル、Web は薄いシェル

- **インジェクションポリシー（stage A）**——`injection.model: auto|always|never`：エージェントネイティブのモデル資格情報がデフォルトで優勝；資格情報の注入はすべてタスク出力で宣言され監査ログに記録。
- **コスト考慮ルーティング（stage A）**——エージェント選択は能力 × cost_tier を `preferred_agents` ボーナス付きでスコアリングし、次善の一致エージェントへフォールバック。
- **メモリ刷新（stage A）**——設定可能な上限（`memory.limits`）、マニフェスト風の選択的注入による複数ファイル topics、低重みの dream 沈殿。
- **ハードウェア探査（stage A）**——共有 `internal/hwinfo` パッケージが `panda detect` と `/api/self` を支える。
- **CLI コマンドファミリー（stage B）**——すべての Web 機能に CLI の対応物：`panda session`（worktree 分離チャットセッション）、`task`（ボード/タイムライン/追加）、`memory`（複数ファイル編集）、`config`、`agents`（探査+接続テスト）、`project`——すべてパネルとサービス層を共有。プラットフォーム端末ヘルパー（term_darwin/linux/unix/other）が raw モード UX を提供。
- **パネル：self・アプリ設定・メモリ topics（stage C）**——`GET /api/self`（デバイスプロファイル）、`GET/PUT /api/settings/app`（検証付きアプリポリシー保存）、ファイルごとの読み書きに対応したメモリ API；コンソールのメモリページは topics 向けに製品化、設定はグループ化、i18n は全 5 言語で同期。
- **パネルエンドポイントのテストカバー**——17 のテストが監査が指摘した高リスクの穴を埋める：セッション CRUD に本物の git エンドツーエンド（HTTP での worktree 切り出し、diff、メインチェックアウトへのマージ着地）、モデル設定のキーマスキング（生のシークレットは決して外に出ない）、MCP 起動失敗の 400、skills ライフサイクル、リマインダー CRUD、システムエンドポイント。

### 出力衛生とアダプター

- **Markdown 出力衛生**——新 `internal/mdtext` が出力先ごとに回答をレンダリング：カラー TTY は ANSI 強調（シアンの見出し、太字、淡色コード、整列テーブル）、パイプと素のコンソールはプレーンテキスト、音声パイプライン（`Speak`）は TTS 前に必ず Markdown を除去。ストリーミング差分も同じ規則で行単位にレンダリングされ、どの面にも生の `**`/`|`/`#` マーカーは漏れない。
- **回答規律**——エントリプロンプトは結論ファーストの回答（推論過程を見せない、最小限の構造）を要求し、エージェントプロンプトは出力ライダーを携える：最終メッセージは何をしたかを報告し、探索の軌跡ではない。実行の詳細は `panda task <id>` のイベントに残る——折りたたまれた「過程を表示」の CLI 版。
- **codex アダプター**——`-s danger-full-access` で実行：codex 自身のサンドボックスは非対話な親プロセス下では初期化すらできない（状態 DB と PATH エイリアス作成が最初のターン前に EPERM で失敗する）うえ、PANDA はすでにアダプターをタスク cwd に閉じ込めている。エージェント失敗も空の `result {"failed":""}` ではなく診断を表示するようになった。

### 追加

- **リソース考慮のローカルタスクキュー**——`core.Submit` の同期モデルが非同期キューへ：`internal/scheduler/queue` はドラッグ順 → 優先度 → FIFO でタスクを並べ、リソースロックレジストリと `MaxConcurrent` で起動を門番するため、リソースが衝突しないタスクは詰まったキューを追い越して先行する。タスクは `priority`/`seq`/`session_id`/`resource_keys` を獲得（SQLite v9）、ボードタスクは紐づくセッションへジャンプ（0e8d850）。
- **`panda init`——対話式初回ブートストラップ**——1 回の対話プロンプトから `config.yaml` + `capabilities.yaml` をユーザー書き込み可能なディレクトリへ：ハードウェアスキャンのデフォルト、検証付き列挙入力（打ち間違いは再質問）、5 言語プロンプト。`config.ResolvePath` が単一の解決順序（フラグ > 環境変数 > ユーザー設定 > システム既定）を提供し、daemon、`panda web`、webui サイドカー、doctor が共有するため、init が書いた設定は追加フラグなしでどこでも拾われる（f5610fc）。
- **コンソール P1 仕上げ——統一ページ、編集可能メモリ、トースト、確認ダイアログ**——共有 `PageHeader`/`ErrorState` コンポーネントが全ビューの構造とエラー処理を標準化；メモリページは製品ページへ（`§` でエントリ分割、新エントリ強調、文字カウンタ、新 `PUT /api/memory/{file}` によるその場編集）；グローバルトーストフィードバック（エラーは手動閉じ、成功/情報は自動）が散在するビューごとのエラーテキストに取って代わる；破壊的操作——チャット削除、skill 拒否、タスクキャンセル、リマインダー削除——には確認ダイアログが必要（45ee941）。

### 変更

- **グループ化サイドバーナビゲーション**——コンソールナビが折りたたみ可能なセクション（タスク / デバイスとエージェント / 個人 / システム）に集約、アクティブなグループを自動展開——8 つのフラットな項目の代わりに段階的開示；エントリプロンプトは「指揮者」ペルソナに再設定：単純な質問は直接回答、複雑な作業はデバイスとエージェントへ派遣（f5610fc）。
- **ask パイプラインのストリーミング耐性と収束**——`streamWithRetry` はユーザーがまだ何も見ていない間だけ一時的な断（429/5xx/ネットワーク）をバックオフ付きで再試行；`deltaGuard` は生の構造化出力（タスク JSON、```json フェンス）がチャットバブルや端末に流れるのを防ぎ、抑制された差分は配信済みと数えないため JSON 途中の切断も再試行可能；maxRounds を使い切ったツールループはエラーではなく最後のツールなし呼び出しで収束；ツール実行は分類時と同じレジストリスナップショットを使い、MCP ホットスイッチ中の「unknown tool」を防止；CLI/REPL の回答は TTY でライブストリーミング（ツール進捗は 1 行ノート）、パイプ出力はクリーンなまま（df47725）。

### 修正

- **アダプターの全実行タイムアウト**——codex/claude アダプターは CLI の stdout を EOF まで読んでからプロセスを待つため、途中で固まった CLI（パイプは開いたまま、出力なし）は読みループを永久にブロックしていた——リクエストタイムアウトは stdout EOF 後の尻尾しかカバーしていなかった。両アダプターは CLI を独立プロセスグループで起動し、ウォッチドッグスレッドが期限にプロセスツリー全体をキルする：子プロセスがパイプを継承して握ったままになるため、直接の子だけキルすると読み手はブロックし続けた。
- **Anthropic ツール API 互換**——tool_use ブロックは常に `input` を携える（引数なしツールは空オブジェクト）：以前は map の omitempty がそれを落とし、厳格な Anthropic 互換プロバイダ（DeepSeek /anthropic）が後続ターンを 400 で拒否していた。ドット付きツール名（reminder.set、time.now、weather.get）は `^[a-zA-Z0-9_-]+$` パターンを満たすようアンダースコアに改名。
- **work_path 自動作成**——daemon は起動時に全ストレージルート（context/memory/projects/skills/work）を mkdir；作業ディレクトリの欠落はコマンドバイナリを責める誤解を招く fork/exec ENOENT として現れていた。
- **相互ダイヤル再接続ストーム**——重複解消の敗者の最後の hello 返信が到着コネクションではなくレジストリコネクションで送られ、敗者はピアの身元を永遠に束縛できず、MaintainPeer のエッジ待機を飛ばし、毎秒ダイヤルし直していた；実機で 15 分間に 869 回の再接続を観測、今は 1 回の後は静寂。
- **ゲートと小さな硬化**——Makefile `measure` ターゲットが存在しない設定を参照（README スニペットと一致するよう修正）；gofmt ゲートを満たすため 6 ファイル再フォーマット；README バッジ/前提条件を 5 版で Go ≥1.26 に統一；`.gitignore` に `.openpanda/` を追加；サンプル設定の幻のピアをコメントアウトし `make run` が警告をホットループしないように；パネルに `securityHeaders` ミドルウェアを追加し縦深防御（cacde7b）。
- **レガシー DB 上の SQLite v9 マイグレーション**——キュー構造マイグレーションは `tasks` テーブルが存在しないデータベースでクラッシュ；欠けていれば作成するよう修正（0e8d850）。
- **実行可能な API エラーマッピング**——非 OK ステータスコードがストリーミング経路で保持され、エラーは生のトランスポートノイズではなく指針として届く：401/403 は `model.api_key` へ、404 は `base_url`/モデル名へ、400 はリクエストへ、持続する 429/5xx はレート制限かサービス不安を明示、接続失敗はネットワーク確認を提案（df47725）。

## [0.0.1] - 2026-08-19

初のオープンソースプレリリース。

**プロジェクトを OpenPanda に改称**（Open + Personal Adaptive Node-based Distributed Assistant）。Go モジュールパスは `github.com/Xustalis/OpenPanda` に；すべての環境変数は `OPENPANDA_` プレフィックス；systemd/LaunchAgent ユニットは `openpanda.service` / `com.openpanda.node.plist`；デフォルト DB ファイル名は `openpanda.db`。CLI バイナリは短い名前 `panda` を維持。

全期間ゲート全緑：build / vet / 全テスト / `-race` / クロスコンパイル。完全なカーネル機能一式（daemon、CLI、P2P 委譲、監査チェーン、マイグレーション、スケジューラスコアリング+重複排除、SSE パネル、組み込み Web コンソール、対話 REPL、クロスプラットフォーム install/uninstall/doctor）に加え、アシスタント層：エージェント感覚、スケジュールリマインダー、MCP 統合、worktree チャットセッション、カンバンキューボード。

### v0.0.1 プレリリース監査修正

- **Web コンソール埋め込み再構築**——vite ビルドは `dist/app/` に着地し、リポジトリにコミットされた `dist/index.html` プレースホルダはビルドで触られない。以前は `make web` + `git add -A` サイクルが、無視された `/assets/*` を指すハッシュ付き index.html をコミットし、新規クローンごとにコンソールがホワイトスクリーンになっていた。プレースホルダは安定し、`make web` は `dist/app/index.html` の存在を守り、静的ハンドラは本物のビルドを優先しプレースホルダでフォールバック。
- **`panda help`**——サブコマンドが存在するように（`-h`/`--help` も）、エラーの代わりに方向性のある概観を出力；未知のサブコマンドも同じ使い方を出力。
- **ブランド残滓**——エントリモデルのシステムプロンプトはエージェントを「PANDA」と自己紹介していた；「OpenPanda」に（すべての返信でユーザーに見える）。`config.example.yaml` ヘッダも同様。
- **`config.example.yaml`**——文書化されていなかった `mcp:` セクションと `model.api_type`（anthropic | openai）を記載；push セクションのコメントを組み込みコンソール時代に更新。
- **デッドリンク**——ロードマップがローカルのみの委譲レポートを参照；検証を再現する `scripts/smoke-delegate` を指すよう修正。

### 追加

- **`panda install` / `panda uninstall` / `panda doctor`——クロスプラットフォームなグローバルコマンドライフサイクル**——`panda install` はバイナリを `~/.local/bin`（unix）/ `%LOCALAPPDATA%\OpenPanda\bin`（Windows）へコピーし PATH に永続登録：unix ではシェル rc ファイルのマーク付きブロック（`# >>> openpanda path >>>`、冪等、ユーザー行は不改変）、Windows ではレジストリ API で HKCU\Environment に値型を保持して（`setx` は回避——PATH を 1024 文字で切り詰める）WM_SETTINGCHANGE ブロードキャスト付き；その後インストール済みコピーを実行して自己検証。`panda doctor` はスタンドアロンの自己診断（インストール済みコピーが実行可 / PATH が解ける / 再起動後も永続 / 設定と DB が利用可；失敗時は exit 1）。`panda uninstall` はホワイトリスト安全：完全な計画を表示、`confirm` の入力を要求（スクリプトは `--yes`、プレビューは `--dry-run`）、明示的に導出されたターゲットのみ削除（バイナリ、PATH 登録、DB+ジャーナル、context ディレクトリ、VAPID キー、所有するルート内の設定のみ）、ユーザー資産は常に保持（projects/memory/skills/work ディレクトリ——ホームや資産と重なるものは自動的に保持へ反転）、削除前の状態の zip バックアップをホームに書き、削除/保持レポートを生成。ガードレールの核は `internal/install`（単体テスト済み、シンボリックリンク安全：リンクは除去され、決して辿らない）。全編 5 言語 CLI メッセージ。
- **`panda web`——1 コマンドで開くコンソールと自動ログイン**——デフォルトでループバックバインドとランダムな一時トークン（ゼロ設定）、ブラウザは `/?token=…` を開き、アプリは 1 回消費してアドレスバーから除去：設定編集もトークン貼り付けも不要。同じゼロ設定+自動ログイン挙動は REPL の `/web` と `panda-webui` サイドカーにも（開ける URL を出力）；トークンなしの非ループバックバインドは今なお fail closed。フロントエンドはロード時に `?token=` を消費（Jupyter 流）；`make web` はビルドが着地しなければ大声で失敗（プレースホルダガード）。
- **対話 REPL**——`panda repl` はオペレータの席：素の入力は ask エンジンへ、スラッシュコマンドはすべてのパネル機能を駆動（`/ask`、`/tasks`、`/task`、`/cancel`、`/approve`、`/reject`、`/logs`、`/projects`、`/project`、`/nodes`、`/authorize`、`/lang`）、`/web` で組み込みコンソールをワンクリック起動。未知のコマンドは修正を指摘して終了しない；ask エンジンは任意で、モデルエンドポイントなしでも REPL はパネルコマンドを提供（7a5c2bf）。
- **組み込み Web コンソール**——コンソールは Vite + Preact + TypeScript で再構築（Preact 以外ゼロランタイム依存）され `go:embed` でバイナリに折り込まれる：キュー/詳細/ask/プロジェクト/ノード/承認ビュー、ライブ SSE 更新、5 UI 言語（English、简体中文、日本語、Español、Deutsch）、パンダブランド SVG。`make web` がビルド；コミット済みプレースホルダが node なしの `go build` を機能させる（844ccf6、688cc20）。
- **パネル書き込み経路 + SSE**——`POST /api/ask`（共有 `askengine` パッケージによる統一エントリモデル）、`POST /api/projects` + `GET /api/projects`、`GET /api/nodes`（ライブ能力ディレクトリ）、`POST /api/tasks/{id}/cancel`、`GET /api/tasks/{id}/logs`、`GET /api/events`（キュー/ノード変更の SSE ストリーム）がシステム監査で見つかった読み取り専用の隙間を埋める（b599dc7、6748baa）。
- **CLI i18n**——`internal/i18n`：ロケール検出、英語フォールバック、`{placeholder}` 補間；CLI と REPL が同じ 5 言語メッセージマップを共有し、ロケールエントリの追加で拡張可能（7a5c2bf）。
- **設定起動検証**——リソースクラス、ピアとリスンアドレス、リスン/パネルポートの衝突をダイヤル時ではなく起動時に検査（b599dc7）。
- **`scripts/smoke-delegate`**——プロセス間委譲検証器：一時的なスケジューラ参加者となり、ピアのみが持つ能力を要求するタスクを提出してどこで実行されたか報告；exit 0 は往復がピアで done に到達（fbb4f9e）。
- **CONTRIBUTING.md**——エンジニアリングゲート（`make gate`）、コード規約（エラーラップ、コメント方針、並行規則、fail-closed セキュリティ）、コミットスタイル、i18n 規則、PR チェックリスト；公開の[デスクトップとパッケージングのロードマップ](docs/plans/roadmap-desktop-and-packaging.md)（Stage 1 完了 → Stage 2 配布硬化 → Stage 3 Wails によるデスクトップ → Stage 4 マーケット/モバイル/マルチユーザー）。
- **Sprint 2 の論文メカニズム（ATC-MARL マッピング）**——`internal/scheduler/score.go`：DCPS 加重スコアリング（`0.4·resource_efficiency + 0.3·user_priority + 0.2·scheduler_tier + 0.1·wait_time`）を TMB ハートビートの新しさで割り引く（`exp(-λ·Δt)`、30 分の半減期）；`MaxConcurrent` による容量駆動の accept/decline；ハートビートがライブ `CurrentTasks` を運び、両メカニズムのデータループを閉じる（543801f）。
- **拒否の自動再経路**——タスクは `requires` 能力集合を永続化（`requires_json` カラム、提出と委譲の両経路）；拒否されたタスクは履歴の拒否者を除いて DCPS スコアリングを再実行し（監査イベント `EvDecline` の `DeclinedBy`）、次善のノードへ再派遣（dad4f04、P1-5）。
- **`panda metrics [--csv]`**——委譲メトリクスのエクスポート；**`panda audit verify [--task <id>]`**——グローバル監査ログまたは 1 タスクのイベントタイムラインの `prev_hash` チェーンを検証（6f2c8d5）。
- **PRAGMA `user_version` 駆動の SQLite マイグレーション**（6f2c8d5、A1）と `task_events`・`audit_log` の **`prev_hash` 監査チェーン**（6f2c8d5、A3）。
- **`OPENPANDA_WAKE_KEYWORD`** 環境変数で音声ウェイクワードを上書き；openwakeword は `OPENPANDA_WAKE_MODEL` でカスタム `.tflite` を指定可能（2e72c8c）。
- **エージェントの感覚**——`time.now` と `weather.get` システムツール：モデル自身に時計も窓もないため、ask エンジンが提供（天気は Open-Meteo ジオコーディング + 今日/明日）（c36cad1）。
- **スケジュールリマインダー**——`reminder.set` ツールでエージェント自身が予定；SQLite ストアとアトミックな `ClaimDue` + スキャナで daemon とパネルが 1 つの DB を二重発火なしで共有；配信は Web Push（ループバックはセキュアコンテキストとして扱われ、`panda web` は TLS なしで push を得る）と開いているコンソールでのライブ SSE カウントダウン；`panda reminder list/add/rm` が CLI から管理（c36cad1）。
- **MCP 統合とホットリロード**——1 つの stdio MCP サーバー、`config.yaml`（`mcp.command`、コメント保持更新）またはコンソール設定カードで構成、スワップ前に実際にサーバーを起動してツールを列挙して検証；ツールは即座にエージェントツールセットへ参加、再起動不要（c36cad1）。
- **git worktree のチャットセッション**——コンソールの会話は隔離された git worktree でストリーミング返信とともに実行；セッションのタスクカードはライブ思考チェーンを公開（task_events リプレイ + SSE 再取得）し、完了したタスクはちょうど 1 回だけ概要ターンをセッションへ折り返す（c36cad1、0e8d850）。
- **カンバンキューボード**——Web コンソールの 4 列タスクボード：作成フォーム、優先度サイクル、列ごとのドラッグ並べ替え、インライン承認（da9c9e1）。
- **codex アダプター + エージェント可視性**——`adapters/codex.py` が同じアダプタープロトコルで claude/opencode に参加；設定ページはインストール済みエージェント CLI を接続テスト付きで列挙；`panda detect` はハードウェア（CPU/RAM/GPU/エージェント CLI）を capabilities.yaml ドラフトにスキャン；doctor は python3、`adapters/`、各エージェント CLI も検査（c36cad1、0e8d850）。

### 変更

- **ピア再接続が古い接続を置換**——同じ認証済み ID からの新しい接続がレジストリに入れ替わり（古い接続はロック外でクローズ）、`handleHello` は置換時に再挨拶；レジストリ除去はコネクションポインタで一致（befa3bd、P1-7）。
- **エージェント経路が Tier 認可モデルに参加**——`ledger.Agent` に `tier` フィールド；未宣言のエージェントはデフォルト Tier 2（fail closed）で、アダプターサブプロセス起動前に `defense.Authorize` が拒否；明示的な `tier: 1` カードは承認なしで実行（c26b11e、P1-15）。
- **シークレットファイル硬化**——`api_key` / `shared_secret` / `panel_token` を含む設定は自動で 0600 に chmod され、起動時に環境変数推奨の警告（`OPENPANDA_SHARED_SECRET` / `OPENPANDA_PANEL_TOKEN` / `OPENPANDA_MODEL_API_KEY`）；chmod 失敗は警告のみで阻断しない（e5de650、P1-19）。
- **インタプリタ `-c` 分類はホワイトリスト制**——純粋出力と証明できるコード（echo/print/console.log…）のみ Tier 1、他はすべて Tier 2（38186af、P1-14）。
- **パネルはループバックがデフォルト**——`127.0.0.1:7840`；非ループバックバインドは平文 HTTP を警告（3c7e8f4、P1-24）。
- **配備ベースライン成文**——平文 `ws://` はループバック/Tailscale/信頼済み LAN のみ、公のインターネットでは TLS リバースプロキシ + `wss://`（6f2c8d5、C1）。
- **個人メモリとワークスペースセッションの間の硬いメモリ壁**——AskTurns は従来、プロジェクト worktree に束縛されたセッション会話も含め、すべての分類に Hermes 個人メモリを注入していたため、「ユーザーはダークテーマが好き」がそのチャットから派生したコードタスクに漏れ得た。固定された workDir はワークスペース会話を標識し個人メモリは一切読み込まれない；パネルも非リポジトリセッションを作業パスにピン留めし、すべてのセッションがワークスペーススコープと数えられる。プロジェクトメモリは実行側の ContextPack 経由でのみ届き、エントリプロンプトには決して入らない；回帰テストが両方向をピン留め（da9c9e1）。
- **Anthropic に加えて OpenAI ワイヤ形式**——エントリモデルは `/v1/messages` 互換と OpenAI 互換の両エンドポイントを話し、両経路でストリーミング補完（c36cad1）。

### 修正

- **相互ダイヤル接続フラップ**——2 ノードが同時にお互いにダイヤルしたとき（両側 `peers:` の一般的な配備）、各側は同じピアへの 1 つのアウトバウンドと 1 つのインバウンド接続を保持；2 番目の登録が最初をクローズし、その再接続ループが 1 秒後にダイヤルし直し、今度は相手側の接続を追い出す——能力ディレクトリをオフライン/オンラインでかき回す無限の ~1 秒接続/切断サイクル。修正：`ensurePeer` の決定的タイブレーク——辞書順に小さいノード ID が開始した接続が勝ち、両側が同じ勝者を計算し、正確に 1 本の TCP が生存；`MaintainPeer` はホットダイヤルではなくエッジが死ぬまでブロック（fbb4f9e、回帰テスト `TestMutualDialDedup`）。
- **ワイヤプロトコル認可の隙間**——`handleResult`/`handleDecline`/`handleAccept` は送信者が現在の実行者であることを検証（監査イベント `EvDelegate` の `DispatchTarget`；空の `AttemptID` は拒否）；`handleContextAck` は送信者が `context_fetch` のターゲットか検証；Accept/Cancel/Approve/Reject の CAS 状態ガードが TOCTOU 競合を閉じる；`waiting_context` は常にリースを携える；ローカル実行失敗はゾンビを残さず終状態化（a6fc1c2、P1-1/2/3/4/6/8/9/11）。
- **コマンド分類のバイパス**——`env -S` 値の再帰分類；`php -r` スキャン；`find -exec`、`tar --checkpoint-action`、`git push/commit` 等は Tier 2 へ fail-closed；`make`/`ssh` が破壊的テーブルに参加（38186af、P1-12/13）。
- **プロセスグループ管理 + アダプターのハードタイムアウト**——Unix `Setpgid` でキャンセル時にグループ全体をキル、Windows は `taskkill /T`；アダプターは 630 秒の `context.WithTimeout` でラップ（exit 124）、孤児の孫プロセスなし（38186af、P1-17/18）。
- **メモリシステムの注入チャネル**——Hermes/Projects/skills/dream-last-deep のアトミック書き込み（`util.WriteFileAtomic`）；`Projects.Save` のミューテックス；外部入力は `[ext]` で汚染マークされ Deep dreaming で昇格されない；メモリ注入は `<memory_data>` で囲まれデータ非指示の前書き付き；日次エントリの改行サニタイズ（3c7e8f4、P1-20/21/22/23、P2-16）。
- **CLI の落とし穴**——未知のサブコマンドは daemon を起動せず exit 2（`panda statsu` は常駐 daemon を起こさない）；手書きの `dirOf` を `filepath.Dir` に置換（3c7e8f4、P1-25/26）。
- **キャンセル伝播**——`task_cancel` は実行中のノードへ下流転送（派遣ターゲットは `EvDelegate` から復元）、委譲チェーンに沿ってホップごとにカスケード；CLI とワイヤ経路が `Core.CancelTree`/`finishCancel` を共有（66b265d、P2-3/P2-7）。
- **トランザクションな状態書き込み**——TaskStore の状態 UPDATE と監査イベント INSERT が 1 トランザクションでコミット（`applyCAS`、`applyState`、Accept/Decline/Approve/Reject/Cancel/CreateWithID）；ctxstore の upsert + 容量退避もトランザクション化、並行 Put の回帰テスト付き（bcbf156、P2-1/14）。
- **音声ウェイクのデフォルト**——デフォルトキーワードは各バックエンドの本物の内蔵項目（openwakeword は `hey_jarvis`、pvporcupine は `porcupine`）；以前はデフォルトの `hey_panda` が起動時に例外（2e72c8c、P2-21）。
- **スロー DoS 防護**——hello タイムアウトとグローバル/ IP あたり接続上限（`max_connections`、`max_connections_per_ip`）（6f2c8d5、A2）。
- **MCP クライアントのハードタイムアウト**とプロセスキルのフォールバック（6f2c8d5、A4）。
- **包括チェックの一掃（D1–D32 + P1–P3）**——委譲孤児の終状態化、転送コピーのリース化、ForceFail/CompleteFromRemote/FailFromRemote の CAS ガード、`PreferredNode` を明示的な `spec.node` に束縛、hello HMAC の 5 分タイムスタンプ窓への束縛、NetworkGuard の許可リストを構成済みエンドポイントにピン留め、Redact の JSON 引用キー対応、TierFromCommand のパス/`.exe` 正規化、サブプロセス出力キャプチャの上限（8MiB）ほか（75b98c8）。

### 計画中のフォローアップ（見送り）

意図的に v0.0.1 後へ見送り——可視性のためここに記録：

- **キーボードショートカット**——コンソールのグローバルホットキー（新規チャット、クイックタスク、ビュー切替）。
- **ブラウザ統合**——アシスタントのコンパニオンブラウザ面。
- **Git 画面**——コンソール内の第一級 git ビュー（ブランチ状態、履歴、リモート）。
- **Worktree 管理**——チャット削除時だけでなく、コンソールから worktree の一覧/整理/検視。
- **パーソナライズ**——ユーザー調整可能なアシスタントの性格と提示設定。
- **Web 検索キャッシュ**——エージェントの Web 検索のキャッシュ層で繰り返し取得とレイテンシを削減。
- **推論努力の段階**——低/中/高の推論強度をタスクごとの設定として公開。
