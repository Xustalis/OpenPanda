// SPDX-License-Identifier: AGPL-3.0-or-later

package i18n

func init() {
	for loc, text := range map[Locale]string{
		English:     "running local workers refresh this policy automatically; active calls finish with their current settings",
		ChineseSimp: "运行中的本地工作进程会自动同步；已有调用继续使用原设置完成",
		Japanese:    "実行中のローカルワーカーは自動同期します。進行中の呼び出しは現在の設定で完了します",
		Spanish:     "los procesos locales se sincronizan automáticamente; las llamadas activas terminan con su configuración actual",
		German:      "lokale Worker aktualisieren sich automatisch; laufende Aufrufe enden mit ihren bisherigen Einstellungen",
	} {
		messages[loc]["cli.config.live"] = text
	}
}
