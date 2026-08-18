import type { Messages } from './index'

const zhCN: Messages = {
  // Shell / navigation
  'app.name': 'OpenPanda',
  'app.tagline': '跨设备的个人任务编排助手',
  'nav.queue': '任务队列',
  'nav.ask': '提问',
  'nav.projects': '项目',
  'nav.nodes': '节点',

  // Token gate
  'token.title': '连接到你的节点',
  'token.description': '输入该节点配置中的面板令牌（config.yaml 的 network.panel_token）。',
  'token.label': '面板令牌',
  'token.submit': '连接',
  'token.invalid': '令牌被拒绝 — 请检查后重试。',
  'token.logout': '断开连接',

  // Common
  'common.loading': '加载中…',
  'common.error': '出了点问题。',
  'common.retry': '重试',
  'common.cancel': '取消',
  'common.save': '保存',
  'common.close': '关闭',
  'common.empty': '这里还没有内容。',

  // Task states
  'state.submitted': '已提交',
  'state.queued': '已入队',
  'state.dispatched': '已派发',
  'state.waiting_context': '等待上下文',
  'state.running': '运行中',
  'state.review': '待审批',
  'state.done': '已完成',
  'state.failed': '已失败',
  'state.cancelled': '已取消',
  'state.expired': '已过期',

  // Queue view
  'queue.subtitle': '本节点知晓的全部任务，实时更新。',
  'queue.allStates': '全部状态',
  'queue.allProjects': '全部项目',
  'queue.empty': '还没有任务。去「提问」说点什么来创建一个。',
  'queue.title': '标题',
  'queue.project': '项目',
  'queue.state': '状态',
  'queue.owner': '归属',
  'queue.updated': '更新时间',

  // Detail view
  'detail.back': '返回队列',
  'detail.approve': '批准',
  'detail.reject': '拒绝',
  'detail.cancelTask': '取消任务',
  'detail.rejectedViaWeb': '经 Web 面板拒绝',
  'detail.id': '任务 ID',
  'detail.project': '项目',
  'detail.owner': '归属节点',
  'detail.attempt': '执行批次',
  'detail.created': '创建时间',
  'detail.updated': '更新时间',
  'detail.risk': '风险',
  'detail.result': '执行结果',
  'detail.timeline': '事件时间线',

  // Ask view
  'ask.subtitle': '一句输入 — 得到回答，或派发为任务。',
  'ask.hint': '随便问。OpenPanda 会直接回答、调用小工具，或把任务派发给你的设备。',
  'ask.placeholder': '向 OpenPanda 提问…（Enter 发送，Shift+Enter 换行）',
  'ask.authorize': '授权高风险操作',
  'ask.voice': '语音输入',
  'ask.send': '发送',
  'ask.thinking': '思考中…',
  'ask.taskCreated': '任务已创建。',

  // Projects view
  'projects.subtitle': '项目用于划分任务队列与项目级记忆。',
  'projects.namePlaceholder': '新项目名称',
  'projects.create': '创建',
  'projects.empty': '还没有项目 — 在上方创建一个。',

  // Nodes view
  'nodes.subtitle': '能力目录：本节点可以委派任务的所有设备。',
  'nodes.empty': '还没有已注册的节点。',
  'nodes.lastSeen': '最后在线',
  'nodes.never': '从未',
}

export default zhCN
