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
- 並列書き込みには分離した `paths`、別 `workdir`、または `isolate` が必要です。重複・不明な範囲は直列です。

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
