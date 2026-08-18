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
}

export default zhCN
