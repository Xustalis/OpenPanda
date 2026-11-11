// SPDX-License-Identifier: AGPL-3.0-or-later

package i18n

func init() {
	for loc, rows := range map[Locale]map[string]string{
		English: {
			"ask.repository.unverified":    "I have not verified the implementation in this workspace, so I cannot give a source-backed answer yet. Please provide the relevant source or an available coding agent.",
			"ask.investigation.incomplete": "I have not completed the requested deep repository investigation: no coding-harness task was dispatched. The orientation reads in this turn cannot support a code-level conclusion. Please retry with an available coding agent; I won't present an unverified overview as a completed review.",
		},
		ChineseSimp: {
			"ask.repository.unverified":    "尚未读取并核实当前工作区的实现，不能把猜测当成源码结论。请提供相关源码或可用的 coding agent 后继续。",
			"ask.investigation.incomplete": "这次尚未完成你要求的深入代码库调研：没有成功派发 coding harness 任务。本轮的轻量探查不足以支撑源码级结论；请在可用 coding agent 就绪后重试。我不会把未经核实的概览冒充为完成的审查。",
		},
		Japanese: {
			"ask.repository.unverified":    "このワークスペースの実装はまだ確認できていません。推測をソースコードに基づく結論として提示できません。関連ソースか利用可能な coding agent が必要です。",
			"ask.investigation.incomplete": "要求されたコードベースの詳細調査は未完了です。coding harness のタスクは委譲されていません。今回の概要確認だけではコードレベルの結論を裏付けられません。利用可能な coding agent で再試行してください。未検証の概要を完了したレビューとして提示しません。",
		},
		Spanish: {
			"ask.repository.unverified":    "La implementación de este espacio de trabajo aún no se ha verificado. No puedo presentar suposiciones como conclusiones del código; se necesita el código pertinente o un agente disponible.",
			"ask.investigation.incomplete": "La investigación profunda del repositorio no está terminada: no se delegó ninguna tarea al coding harness. La exploración inicial no respalda conclusiones sobre el código. Vuelve a intentarlo con un agente disponible; no presentaré un resumen sin verificar como una revisión completa.",
		},
		German: {
			"ask.repository.unverified":    "Die Implementierung in diesem Arbeitsbereich wurde noch nicht geprüft. Vermutungen sind keine belegten Code-Aussagen; dafür sind die relevanten Quellen oder ein verfügbarer Coding-Agent nötig.",
			"ask.investigation.incomplete": "Die angeforderte eingehende Repository-Untersuchung ist nicht abgeschlossen: Es wurde keine Coding-Harness-Aufgabe delegiert. Die bisherige Orientierung belegt keine Aussagen über die Implementierung. Bitte mit einem verfügbaren Coding-Agent erneut versuchen; eine ungeprüfte Übersicht wird nicht als abgeschlossene Prüfung ausgegeben.",
		},
	} {
		for key, value := range rows {
			messages[loc][key] = value
		}
	}
}
