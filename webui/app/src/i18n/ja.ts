import type { Messages } from './index'

const ja: Messages = {
  // Shell / navigation
  'app.name': 'OpenPanda',
  'app.tagline': 'デバイスをまたぐパーソナルタスクオーケストレーション',
  'nav.queue': 'キュー',
  'nav.ask': '質問',
  'nav.projects': 'プロジェクト',
  'nav.nodes': 'ノード',

  // Token gate
  'token.title': 'ノードに接続',
  'token.description': 'このノードの設定（config.yaml の network.panel_token）にあるパネルトークンを入力してください。',
  'token.label': 'パネルトークン',
  'token.submit': '接続',
  'token.invalid': 'トークンが拒否されました — 確認してもう一度お試しください。',
  'token.logout': '切断',

  // Common
  'common.loading': '読み込み中…',
  'common.error': '問題が発生しました。',
  'common.retry': '再試行',
  'common.cancel': 'キャンセル',
  'common.save': '保存',
  'common.close': '閉じる',
  'common.empty': 'まだ何もありません。',

  // Task states
  'state.submitted': '送信済み',
  'state.queued': '待機中',
  'state.dispatched': '割当済み',
  'state.waiting_context': 'コンテキスト待ち',
  'state.running': '実行中',
  'state.review': '承認待ち',
  'state.done': '完了',
  'state.failed': '失敗',
  'state.cancelled': 'キャンセル済み',
  'state.expired': '期限切れ',

  // Queue view
  'queue.subtitle': 'このノードが把握しているすべてのタスク（リアルタイム）。',
  'queue.allStates': 'すべての状態',
  'queue.allProjects': 'すべてのプロジェクト',
  'queue.empty': 'タスクはまだありません。「質問」から何か依頼してみましょう。',
  'queue.title': 'タイトル',
  'queue.project': 'プロジェクト',
  'queue.state': '状態',
  'queue.owner': '担当',
  'queue.updated': '更新日時',

  // Detail view
  'detail.back': 'キューに戻る',
  'detail.approve': '承認',
  'detail.reject': '却下',
  'detail.cancelTask': 'タスクをキャンセル',
  'detail.rejectedViaWeb': 'Web パネルから却下',
  'detail.id': 'タスク ID',
  'detail.project': 'プロジェクト',
  'detail.owner': '担当ノード',
  'detail.attempt': '試行',
  'detail.created': '作成日時',
  'detail.updated': '更新日時',
  'detail.risk': 'リスク',
  'detail.result': '実行結果',
  'detail.timeline': 'タイムライン',

  // Ask view
  'ask.subtitle': 'ひとつの入力 — 回答、または委任タスクを返します。',
  'ask.hint': '何でも聞いてください。OpenPanda が直接回答し、小さなツールを実行し、またはデバイスにタスクを委任します。',
  'ask.placeholder': 'OpenPanda に質問…（Enter で送信、Shift+Enter で改行）',
  'ask.authorize': '高リスク操作を許可',
  'ask.voice': '音声入力',
  'ask.send': '送信',
  'ask.thinking': '考え中…',
  'ask.taskCreated': 'タスクを作成しました。',

  // Projects view
  'projects.subtitle': 'プロジェクトはタスクキューとプロジェクト単位のメモリを分割します。',
  'projects.namePlaceholder': '新しいプロジェクト名',
  'projects.create': '作成',
  'projects.empty': 'プロジェクトはまだありません — 上で作成してください。',

  // Nodes view
  'nodes.subtitle': 'ケイパビリティディレクトリ：このノードが委任できるすべてのデバイス。',
  'nodes.empty': '登録済みのノードはまだありません。',
  'nodes.lastSeen': '最終オンライン',
  'nodes.never': '未接続',
}

export default ja
