// SPDX-License-Identifier: AGPL-3.0-or-later

package i18n

func init() {
	mergeMessages(English, map[string]string{
		"cmd.goal":                           "Persistent server goal: /goal [--tokens N] <objective>; edit, pause, resume, stop, clear",
		"cmd.planMode":                       "Toggle persistent read-only planning; /plan <description>; /plan confirm <version>",
		"conversation.status.active":         "active",
		"conversation.status.paused":         "paused",
		"conversation.status.blocked":        "blocked",
		"conversation.status.usage_limited":  "usage limited",
		"conversation.status.budget_limited": "budget limited",
		"conversation.status.complete":       "complete",

		"conversation.planOn":       "Planning active (read-only).",
		"conversation.planOff":      "Planning exited. Goal remains paused.",
		"conversation.goalHelp":     "/goal <objective> | edit <objective> | pause | resume | stop | clear",
		"conversation.confirm":      "Start implementation:",
		"conversation.confirmGoal":  "Implement and continue goal:",
		"conversation.implementing": "Confirmed proposal queued for implementation.",
		"conversation.plan":         "Plan",
		"conversation.goal":         "Goal",
	})
	mergeMessages(ChineseSimp, map[string]string{
		"cmd.goal":                           "服务端持续目标：/goal [--tokens N] <目标>；edit、pause、resume、stop、clear",
		"cmd.planMode":                       "切换持续只读规划；/plan <描述>；/plan confirm <版本>",
		"conversation.status.active":         "活动",
		"conversation.status.paused":         "暂停",
		"conversation.status.blocked":        "阻塞",
		"conversation.status.usage_limited":  "额度受限",
		"conversation.status.budget_limited": "预算受限",
		"conversation.status.complete":       "完成",

		"conversation.planOn":       "规划已开启（只读）。",
		"conversation.planOff":      "已退出规划，目标仍保持暂停。",
		"conversation.goalHelp":     "/goal <目标> | edit <目标> | pause | resume | stop | clear",
		"conversation.confirm":      "开始实施：",
		"conversation.confirmGoal":  "实施并继续目标：",
		"conversation.implementing": "已确认提案，等待后台实施。",
		"conversation.plan":         "计划",
		"conversation.goal":         "目标",
	})
	mergeMessages(Japanese, map[string]string{
		"cmd.goal":                           "継続目標：/goal [--tokens N] <目標>；edit、pause、resume、stop、clear",
		"cmd.planMode":                       "読み取り専用計画を切替；/plan <説明>；/plan confirm <版>",
		"conversation.status.active":         "実行中",
		"conversation.status.paused":         "一時停止",
		"conversation.status.blocked":        "ブロック中",
		"conversation.status.usage_limited":  "使用量上限",
		"conversation.status.budget_limited": "予算上限",
		"conversation.status.complete":       "完了",

		"conversation.planOn":       "計画中（読み取り専用）。",
		"conversation.planOff":      "計画を終了しました。目標は一時停止中です。",
		"conversation.goalHelp":     "/goal <目標> | edit <目標> | pause | resume | stop | clear",
		"conversation.confirm":      "実装を開始：",
		"conversation.confirmGoal":  "実装して目標を続行：",
		"conversation.implementing": "確認した提案を実装キューに登録しました。",
		"conversation.plan":         "計画",
		"conversation.goal":         "目標",
	})
	mergeMessages(Spanish, map[string]string{
		"cmd.goal":                           "Objetivo persistente: /goal [--tokens N] <objetivo>; edit, pause, resume, stop, clear",
		"cmd.planMode":                       "Alternar planificación de solo lectura; /plan <descripción>; /plan confirm <versión>",
		"conversation.status.active":         "activo",
		"conversation.status.paused":         "pausado",
		"conversation.status.blocked":        "bloqueado",
		"conversation.status.usage_limited":  "límite de uso",
		"conversation.status.budget_limited": "límite de presupuesto",
		"conversation.status.complete":       "completo",

		"conversation.planOn":       "Planificación activa (solo lectura).",
		"conversation.planOff":      "Planificación desactivada. El objetivo sigue pausado.",
		"conversation.goalHelp":     "/goal <objetivo> | edit <objetivo> | pause | resume | stop | clear",
		"conversation.confirm":      "Iniciar implementación:",
		"conversation.confirmGoal":  "Implementar y continuar objetivo:",
		"conversation.implementing": "Propuesta confirmada en cola.",
		"conversation.plan":         "Plan",
		"conversation.goal":         "Objetivo",
	})
	mergeMessages(German, map[string]string{
		"cmd.goal":                           "Dauerhaftes Ziel: /goal [--tokens N] <Ziel>; edit, pause, resume, stop, clear",
		"cmd.planMode":                       "Nur lesende Planung umschalten; /plan <Beschreibung>; /plan confirm <Version>",
		"conversation.status.active":         "aktiv",
		"conversation.status.paused":         "pausiert",
		"conversation.status.blocked":        "blockiert",
		"conversation.status.usage_limited":  "Nutzungslimit",
		"conversation.status.budget_limited": "Budgetlimit",
		"conversation.status.complete":       "abgeschlossen",

		"conversation.planOn":       "Planung aktiv (nur lesend).",
		"conversation.planOff":      "Planung beendet. Das Ziel bleibt pausiert.",
		"conversation.goalHelp":     "/goal <Ziel> | edit <Ziel> | pause | resume | stop | clear",
		"conversation.confirm":      "Umsetzung starten:",
		"conversation.confirmGoal":  "Umsetzen und Ziel fortsetzen:",
		"conversation.implementing": "Bestätigter Vorschlag zur Umsetzung eingereiht.",
		"conversation.plan":         "Plan",
		"conversation.goal":         "Ziel",
	})
}
