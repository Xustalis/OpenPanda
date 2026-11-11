// SPDX-License-Identifier: AGPL-3.0-or-later

package i18n

// The panda's closing line for a round that handed out more than one job. It
// is a header plus one bullet per task, so the reader gets the batch at a
// glance: how many jobs, and which of them ended badly. A single job never
// gets one — its own report turn already says how it ended, and a second
// voice repeating the title reads like a log line in the middle of a
// conversation.
//
// The header carries the count but no per-state tally: the bullets below it
// are the tally, and "0 completed, 0 failed" was most of what made the old
// line unreadable. The header's {total} is always >= 2, so no locale needs a
// singular form.
func init() {
	for loc, rows := range map[Locale]map[string]string{
		English: {
			"session.round.head":            "Round result: {total} tasks",
			"session.round.state.done":      "done",
			"session.round.state.failed":    "failed",
			"session.round.state.cancelled": "cancelled",
			"session.round.state.expired":   "expired",
			"session.round.state.review":    "needs review",
			"session.round.detached":        "The request stopped observing or was interrupted; queued tasks were not automatically cancelled. These are their current outcomes.",
		},
		ChineseSimp: {
			"session.round.head":            "本轮 {total} 个任务",
			"session.round.state.done":      "完成",
			"session.round.state.failed":    "失败",
			"session.round.state.cancelled": "已取消",
			"session.round.state.expired":   "已过期",
			"session.round.state.review":    "待审批",
			"session.round.detached":        "本轮请求已停止观察或中断，不代表已取消排队任务。以下是这些任务当前的结果。",
		},
		Japanese: {
			"session.round.head":            "今回 {total} タスク",
			"session.round.state.done":      "完了",
			"session.round.state.failed":    "失敗",
			"session.round.state.cancelled": "取消",
			"session.round.state.expired":   "期限切れ",
			"session.round.state.review":    "承認待ち",
			"session.round.detached":        "リクエストの監視は停止または中断されました。キュー内のタスクは自動取消されていません。以下は現在の実行結果です。",
		},
		Spanish: {
			"session.round.head":            "Resultado: {total} tareas",
			"session.round.state.done":      "completada",
			"session.round.state.failed":    "fallida",
			"session.round.state.cancelled": "cancelada",
			"session.round.state.expired":   "caducada",
			"session.round.state.review":    "por revisar",
			"session.round.detached":        "La observación se detuvo o se interrumpió; las tareas en cola no se cancelaron automáticamente. Estos son sus resultados actuales.",
		},
		German: {
			"session.round.head":            "Rundenergebnis: {total} Aufgaben",
			"session.round.state.done":      "abgeschlossen",
			"session.round.state.failed":    "fehlgeschlagen",
			"session.round.state.cancelled": "abgebrochen",
			"session.round.state.expired":   "abgelaufen",
			"session.round.state.review":    "zu prüfen",
			"session.round.detached":        "Die Beobachtung wurde beendet oder unterbrochen; wartende Aufgaben wurden nicht automatisch abgebrochen. Dies sind ihre aktuellen Ergebnisse.",
		},
	} {
		for key, value := range rows {
			messages[loc][key] = value
		}
	}
}
