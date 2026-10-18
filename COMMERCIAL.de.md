# OpenPanda-Lizenzierung: Community & Kommerziell

Sprachen: [English](COMMERCIAL.md) (maßgeblich) · [简体中文](COMMERCIAL.zh-CN.md) · [日本語](COMMERCIAL.ja.md) · [Español](COMMERCIAL.es.md) · Deutsch

*Diese Übersetzung dient nur der Orientierung; bei Abweichungen ist die englische Fassung maßgeblich.*

OpenPanda ist dual lizenziert. Dieselbe Codebasis wird unter der
GNU Affero General Public License v3 oder später (AGPL-3.0-or-later)
für Open-Source-Nutzung angeboten und unter einer separaten kommerziellen
Lizenz für Fälle, die die AGPL nicht abdeckt.

## Community-Lizenz — AGPL-3.0-or-later

Für immer kostenlos. Sie dürfen OpenPanda unter den AGPL-Bedingungen
ausführen, untersuchen, verändern und weiterverbreiten. Die in der Praxis
relevanten Pflichten:

- **Eine Kopie verbreiten** (Binär- oder Quellform)? → den
  Corresponding Source unter AGPL bereitstellen.
- **Es verändern und Nutzern den Zugriff über ein Netzwerk erlauben**
  (OpenPanda ist ein Daemon mit WebSocket-/Web-Konsole — das zählt)? →
  diesen Nutzern den Quelltext Ihrer veränderten Version anbieten
  (AGPL §13).
- Unverändert auf den eigenen Rechnern laufen lassen, für sich selbst
  oder die eigene Organisation, auch in einem Unternehmen? → **keine
  Pflichten**. Interne Nutzung ist immer kostenlos.

Die AGPL enthält außerdem eine ausdrückliche Patenterteilung und
Retaliation-Klausel (§10–11) und schützt Ihre Freiheit, veränderte
Versionen auf Ihrer eigenen Hardware zu installieren.

## Kommerzielle Lizenz

Für Situationen, in denen AGPL-Konformität nicht passt, z. B.:

- OpenPanda in ein Closed-Source-Produkt oder eine Appliance einbetten;
- ein verändertes/erweitertes OpenPanda als gehosteten Dienst anbieten,
  ohne den eigenen Quelltext zu veröffentlichen;
- unter eigener EULA vertreiben oder Indemnity-, Support- bzw.
  SLA-Zusagen benötigen;
- den Namen/die Marken „OpenPanda" für ein kommerzielles Angebot
  verwenden.

**Kontakt: fing2024@outlook.com**

Kommerzielle Bedingungen werden je Deployment verhandelt; sie
berühren die Community-Edition nicht, die vollständig AGPL bleibt.

## Gewährleistung & Haftung

Die Community-Edition wird strikt „as is" bereitgestellt, ohne
Gewährleistung und ohne Übernahme von Haftung — siehe `TERMS.md`
(中文译本 `TERMS.zh-CN.md`), das auf jede Kopie der Software auf beiden
Lizenzpfaden anwendbar ist. Eine unterzeichnete kommerzielle Vereinbarung
kann eigene Gewährleistungs-, Indemnity- und SLA-Zusagen enthalten; wo
eine solche Vereinbarung schweigt, gilt `TERMS.md` als Basis.

## Warum dieses Modell

Die AGPL hält die Community-Edition echt frei — Verbesserungen können
nie privatisiert werden, ohne dass der Nehmende etwas zurückgibt —,
während die kommerzielle Lizenz die Entwicklung finanziert. Dual
Licensing funktioniert nur, weil das Urheberrecht aggregiert ist: jeder
externe Beitragende unterzeichnet `CLA.md` und räumt dem Projekt das
Recht ein, seinen Beitrag unter beiden Pfaden anzubieten.

## FAQ

**Kann ein Unternehmen OpenPanda intern kostenlos nutzen?**
Ja. Das bloße Ausführen der Software — auch in Produktion, auch in
einem großen Unternehmen — löst keine Quelloffenlegungspflicht aus.
AGPL-Pflichten beginnen, wenn Sie Kopien *übermitteln* oder *Netzwerk-
zugriff auf eine veränderte Version anbieten*.

**Wir betreiben einen veränderten Knoten im LAN. Müssen wir Quelltext
veröffentlichen?**
Nur wenn Nutzer außerhalb Ihrer Organisation über ein Netzwerk damit
interagieren. Interne Veränderungen, die nur innerhalb Ihrer
Organisation geteilt werden, müssen nicht veröffentlicht werden (die
Übermittlung der Software an jemanden verlangt allerdings weiterhin,
*ihm* den Quelltext zu geben).

**Macht die Nutzung eines OpenPanda-Adapters/-Treibers meinen Code AGPL?**
Nein. Agenten-Adapter laufen als separate Prozesse, die ein Stdio-
Wire-Protokoll sprechen; reine Interprozesskommunikation ist kein
abgeleitetes Werk.

**Kann ich das letzte MIT-lizenzierte Release forken?**
Ja — Versionen, die vor dem Lizenzwechsel getaggt wurden, bleiben unter
MIT und werden es immer bleiben. Nur neue Entwicklung ab dem
Umstellungs-Commit ist AGPL/kommerziell.

**Ich bin Beitragender — was passiert mit meinem Code?**
Ihr Urheberrecht bleibt bei Ihnen. Die CLA (`CLA.md`) erlaubt dem
Projekt, Ihre Arbeit sowohl unter AGPL- als auch unter kommerziellen
Bedingungen auszuliefern; die Community-Edition bleibt immer freie
Software.
