# Agent・Subagent のモデルと並列設定

**設定 → Agents** で親・子モデル、Agent ごとの並列上限、ワーカー並列上限、子タスクの制限を設定できます。例のモデル ID は各 CLI で利用できる値に置き換えてください。

```yaml
injection:
  model: never
routing:
  max_concurrent: 6
  agent_concurrency:
    codex: 2
    claude_code: 2
  agent_models:
    codex: [my-root-model]
    claude_code: [my-claude-model]
  subagent_models:
    codex: [my-child-model]
    claude_code: [my-child-claude-model]
delegation:
  enabled: true
  max_concurrent: 3
  fanout: 3
  max_children: 12
  max_depth: 2
```

- `routing.max_concurrent` はこのワーカーの実行中の Agent 呼び出しとキュー受付の上限です。0 は能力カードの値を継承します。
- `agent_concurrency` は Agent 別の追加上限です。キーにはカード名かアダプターファイル名を使えます。同じアダプターの別名はファイル名の上限を共有します。
- `subagent_models` は OpenPanda が作る子タスク用です。キー省略は親の選択を継承、明示的な `[]` は CLI の既定値です。画面で空にすると継承へ戻ります。
- `delegation.max_concurrent` は全親タスクの子呼び出しの共有上限、0 は追加制限なしです。`fanout` は親ごとの同時生成・待機数、`max_children` は累積子数、`max_depth` は最大世代です。
- 上限を下げても進行中の処理は中断しません。待機中の親は Agent 呼び出し枠を使いません。
- 制限は単一ワーカー内の実行中のアダプター呼び出しです。クラスター全体や CLI 内部の子 Agent・待機セッションは対象外です。独立ワーカー間の厳密な共有セマフォではありません。
- プロジェクト付きの複数子タスクで有効な `fanout` が 1 を超える場合、`paths`・`isolate`・`workdir` を省略した兄弟タスクは自動的に隔離コピーとなり、worker・Agent・委譲の上限内で並列実行できます。変更は競合を検査してマージされます。単独の範囲未指定タスクは引き続きプロジェクト全体をロックします。明示的に重複しない `paths` を指定すればコピーを避けられます。共有ツリーで重複するパスは直列です。

これはネイティブ CLI モデル選択です。注入モデルと明示的な CLI 環境変数が優先されます。例の `injection.model: never` は CLI 自身の認証を使います。Panda の認証情報が必要なノードではコピーしないでください。接続先と認証の上書きには `injection.agents` を使います。単一モデル CLI は最初の候補、OpenCode は利用可能な候補を選びます。ZCode は `provider/model`、generic コマンドは `{model}` に対応します。

保存は現在のワーカーへ即時反映され、同じ設定の他ワーカーは約 1 秒ごと・実行前に同期します。進行中のモデルは変わりません。

```sh
panda config routing set agent_models codex my-root-model
panda config routing set subagent_models codex my-child-model
panda config routing set max_concurrent 6
panda config routing set agent_concurrency codex 2
panda config delegation set max_concurrent 3
panda config delegation set fanout 3
panda config routing get --json
```

API: `GET /api/settings/execution`, `PUT /api/settings/execution` (Bearer token).

## 実行期限とコンテキスト

- 候補切り替え、一時的な障害の再試行、認証情報の救済は、ラウンドごとに同じ期限を共有します。期限はタスク指定または全体のタイムアウトに終了猶予を加えた値です。キャンセル後は新しい候補を開始しません。panic は自身の試行を終了し、実行リソースを解放します。
- OpenCode の provider/session エラーは即時終了します。回答後の終端 idle でも、パイプを開いたままの CLI を終了します。初期 idle、ツールエラー、別セッションのイベントは完了扱いにしません。管理対象の Codex タスクは再開用セッションをディスクに保存し、独立診断は既定で一時セッションを使用します。`thread.started` と旧形式のセッションイベントに対応します。
- agent プロンプトのソフト予算は複雑度に応じて 12,000 / 32,000 / 64,000 バイト、不明時は 32,000 です。ユーザー要件、受け入れ基準、作業範囲、プロトコル制約は維持します。スキル本文と縮約索引は残り予算を共有します。個人メモリ索引は関連トピック最大六件、4,000 バイト以内とし、プロジェクトとの分離を維持します。全コマンドは `panda help` で参照できます。
- コンテキスト超過時の再試行は元の予算を半減し、最大 32,000 バイトとします。実行メモは省略箇所を明示し、UTF-8 を保ったまま 12,000 バイトまで圧縮できます。再開時もコールドスタートに備え全タスク要件を保持します。`exec_agent_start` の `prompt_bytes` と `context_budget_bytes` はトークン数ではなくバイト数です。
- 検収・要約は同じモデルの独立した短文設定を使用します。出力上限 2,048 トークン、通信再試行は最大一回、拡張思考の無効化を要求しますが対応は provider に依存します。検収期限は 45 秒で、失敗時は人による確認に回します。通常の対話や agent のモデル設定は変更しません。設定済み・使用中のモデル確認は、agent タスクを作成せず読み取り専用 `model_list` ツールで処理します。

反映には新しいバイナリとアダプターの配置が必要です。設定のホットリロードでは実行中のコードは置き換わりません。
