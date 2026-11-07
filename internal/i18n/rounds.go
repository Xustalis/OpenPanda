// SPDX-License-Identifier: AGPL-3.0-or-later

package i18n

func init() {
	for loc, rows := range map[Locale]map[string]string{
		English: {
			"session.round.summary":  "Round result: {total} tasks; {done} completed, {failed} failed, {cancelled} cancelled/expired, {review} need review.",
			"session.round.detached": "The request stopped observing or was interrupted; queued tasks were not automatically cancelled. These are their current outcomes.",
		},
		ChineseSimp: {
			"session.round.summary":  "本轮结果：共 {total} 个任务，{done} 个完成，{failed} 个失败，{cancelled} 个取消或过期，{review} 个待人工处理。",
			"session.round.detached": "本轮请求已停止观察或中断，不代表已取消排队任务。以上是这些任务当前的执行结果。",
		},
		Japanese: {
			"session.round.summary":  "今回の結果：全 {total} タスク、完了 {done}、失敗 {failed}、取消・期限切れ {cancelled}、要確認 {review}。",
			"session.round.detached": "リクエストの監視は停止または中断されました。キュー内のタスクは自動取消されていません。現在の実行結果を示しています。",
		},
		Spanish: {
			"session.round.summary":  "Resultado de la ronda: {total} tareas; {done} completadas, {failed} fallidas, {cancelled} canceladas/caducadas, {review} requieren revisión.",
			"session.round.detached": "La observación se detuvo o se interrumpió; las tareas en cola no se cancelaron automáticamente. Estos son sus resultados actuales.",
		},
		German: {
			"session.round.summary":  "Rundenergebnis: {total} Aufgaben; {done} abgeschlossen, {failed} fehlgeschlagen, {cancelled} abgebrochen/abgelaufen, {review} zur Prüfung.",
			"session.round.detached": "Die Beobachtung wurde beendet oder unterbrochen; wartende Aufgaben wurden nicht automatisch abgebrochen. Dies sind ihre aktuellen Ergebnisse.",
		},
	} {
		for key, value := range rows {
			messages[loc][key] = value
		}
	}
}
