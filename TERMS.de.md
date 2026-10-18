# OpenPanda Nutzungsbedingungen und Haftungsausschluss

Sprachen: [English](TERMS.md) (maßgeblich) · [简体中文](TERMS.zh-CN.md) · [日本語](TERMS.ja.md) · [Español](TERMS.es.md) · Deutsch

**Diese deutsche Übersetzung dient nur der Lesbarkeit; bei Abweichungen gilt die [englische Fassung](TERMS.md).**

*Gilt für alle OpenPanda-Releases, die am oder nach dem 2026-10-11 verbreitet werden.*

Diese Nutzungsbedingungen und dieser Haftungsausschluss (die „Bedingungen")
gelten für die von Xustalis (dem „Lizenzgeber", „wir") veröffentlichte
OpenPanda-Software (die „Software") auf beiden Vertriebswegen — der GNU
Affero General Public License v3.0 oder später (`LICENSE`) oder einer
ausgehandelten kommerziellen Lizenz (`COMMERCIAL.md`).

**Inhalt:** 1. Annahme · 2. Was die Software tut · 3. Keine Gewährleistung
· 4. Risikoübernahme · 5. Dienste Dritter · 6. Haftungsbegrenzung ·
7. Freistellung · 8. Hochrisikotätigkeiten · 9. Experimentelle Funktionen
· 10. Datenschutz und Daten · 11. Rechtmäßige Nutzung; Export ·
12. Marken · 13. Beitragende · 14. Keine Supportpflicht ·
15. Änderungen · 16. Anwendbares Recht · 17. Verschiedenes

## 1. Annahme; Verhältnis zu den Lizenzen

Durch Herunterladen, Installieren, Kopieren, Verändern, Verbreiten oder
Nutzen der Software nehmen Sie diese Bedingungen an. Wenn Sie ihnen nicht
zustimmen, nutzen Sie die Software nicht. Nutzen Sie die Software im Namen
einer Organisation, versichern Sie, zur Bindung dieser Organisation
befugt zu sein, und „Sie" bezeichnet diese Organisation. Sie müssen in
Ihrer Rechtsordnung mindestens volljährig sein, um diese Bedingungen
selbst anzunehmen; andernfalls müssen ein Elternteil, ein gesetzlicher
Vertreter oder eine verantwortliche Organisation sie in Ihrem Namen
annehmen und tragen die Verantwortung für Ihre Nutzung.

Die Annahme über den Erststart- oder Erneute-Zustimmungshinweis wird auf
Ihrem eigenen Gerät mit UTC-Zeitstempel aufgezeichnet (`terms_accepted_at`
in der UI-Konfiguration) und — soweit der Store des Deployments geöffnet
ist — als Eintrag in seiner signierten Audit-Kette; beide Aufzeichnungen
bleiben auf Ihren Rechnern.

Diese Bedingungen sind Nutzungsbedingungen; sie verändern oder ersetzen
nicht die Lizenz, unter der Ihre Kopie übermittelt wird. Für unter der
AGPL übermittelte Kopien wirken die folgenden Gewährleistungsausschluss-,
Haftungsbegrenzungs-, Freistellungs- und Markenbestimmungen als zusätzliche
Bedingungen der Art, die Abschnitt 7 dieser Lizenz ausdrücklich erlaubt;
soweit eine Bestimmung dieser Bedingungen dennoch als weitere Beschränkung
der von der AGPL gewährten Erlaubnisse gelesen werden könnte, wird diese
Bestimmung abgetrennt und die AGPL geht vor. Haben Sie einen unterzeichneten
kommerziellen Lizenzvertrag, gelten dessen eigene Gewährleistungs-,
Freistellungs- und Haftungsklauseln; wo dieser Vertrag schweigt, gelten
diese Bedingungen als Grundlage.

## 2. Was die Software tut — bitte lesen

OpenPanda ist ein Orchestrator, keine Sandbox. Es koordiniert
KI-Agenten-Kommandozeilenwerkzeuge und direkte Shell-Ausführung über einen
oder mehrere Rechner, die zu einem Peer-to-Peer-Mesh verbunden sind. Je
nach Konfiguration kann es:

- beliebige Shell-Befehle ausführen; Dateien erstellen, verändern und
  löschen; Pakete installieren und sonst den Systemzustand ändern;
- Agenten-CLIs Dritter ausführen (z. B. Claude Code, OpenAI Codex und
  andere), die autonom Aktionen erzeugen und ausführen und Ihre
  API-Konten belasten können;
- Netzwerk-Listener öffnen, sich mit Peer-Knoten verbinden und Aufgaben
  zwischen Rechnern delegieren;
- nach menschlicher Freigabe als tier-2/irreversibel eingestufte
  Operationen ausführen;
- Updates für sich selbst aus einem Release-Kanal prüfen und anwenden.

Die Software tut dies **unter Ihrer Konfiguration, auf Hardware, die Sie
kontrollieren, mit Ihren Zugangsdaten und auf Ihre Anweisung**. Sie —
nicht der Lizenzgeber — betreiben das System und tragen seine Ergebnisse.

## 3. KEINE GEWÄHRLEISTUNG — die Software wird „WIE BESEHEN" bereitgestellt

SOWEIT NACH ANWENDBAREM RECHT ZULÄSSIG, WIRD DIE SOFTWARE „WIE BESEHEN"
UND „WIE VERFÜGBAR" MIT ALLEN FEHLERN UND OHNE GEWÄHRLEISTUNG ODER
BEDINGUNG JEGLICHER ART BEREITGESTELLT. DER LIZENZGEBER, SEINE VERBUNDENEN
UNTERNEHMEN UND ALLE BEITRAGENDEN SCHLIESSEN ALLE AUSDRÜCKLICHEN,
STILLSCHWEIGENDEN, GESETZLICHEN ODER SONSTIGEN GEWÄHRLEISTUNGEN UND
BEDINGUNGEN AUS, EINSCHLIESSLICH — ABER NICHT BESCHRÄNKT AUF —
STILLSCHWEIGENDE GEWÄHRLEISTUNGEN ODER BEDINGUNGEN DER MARKTGÄNGIGKEIT,
EIGNUNG FÜR EINEN BESTIMMTEN ZWECK, RECHTSMÄNGELFREIHEIT,
NICHTVERLETZUNG, RICHTIGKEIT, UNGESTÖRTEN BESITZES UND
SYSTEMINTEGRATION, SOWIE JEDE AUS GESCHÄFTSVERKEHR, GEBRAUCH ODER
HANDELSPRAXIS ENTSTEHENDE GEWÄHRLEISTUNG.

OHNE DAS VORSTEHENDE EINZUSCHRÄNKEN WIRD NICHT GEWÄHRLEISTET, DASS DIE
SOFTWARE IHRE ANFORDERUNGEN ERFÜLLT; DASS SIE UNUNTERBROCHEN, RECHTZEITIG,
SICHER ODER FEHLERFREI LÄUFT; DASS MÄNGEL BEHOBEN WERDEN; ODER DASS SIE
MIT BESTIMMTER HARDWARE, SOFTWARE ODER DIENSTEN DRITTER KOMPATIBEL IST.
SOWEIT ANWENDBARES RECHT DEN AUSSCHLUSS STILLSCHWEIGENDER
GEWÄHRLEISTUNGEN NICHT GESTATTET, GILT DIESE BESTIMMUNG IM GRÖSSTMÖGLICHEN
ERLAUBTEN UMFANG, UND JEDE UNVERMEIDBARE GEWÄHRLEISTUNG WIRD IN IHRER
DAUER AUF DEN KÜRZESTEN GESETZLICH ZULÄSSIGEN ZEITRAUM BESCHRÄNKT.

DIESELBE „WIE-BESEHEN"-GRUNDLAGE GILT FÜR DOKUMENTATION, BEISPIELDATEIEN,
BEISPIELKONFIGURATIONEN, ADAPTER-SKRIPTE UND ALLE SONSTIGEN MIT DER
SOFTWARE VERTRIEBENEN MATERIALIEN.

## 4. Risikoübernahme — autonomer Betrieb

Sie verstehen, akzeptieren und übernehmen das Risiko, dass:

- **Befehle und Agentenaktionen zerstörerisch, irreversibel oder unsicher
  sein können.** Ausgaben von KI-Modellen sind probabilistisch und können
  falsch, schädlich, unsicher oder zerstörerisch sein. Das Einrichten von
  Berechtigungsstufen, Genehmigungswarteschlangen, Sandboxing und
  Netzwerk-Allowlists sowie das Beaufsichtigen dessen, was das System
  ausführt, ist Ihre Verantwortung. Genehmigungsdialoge und Leitplanken
  sind Workflow-Hilfen, keine Sicherheitsgarantien.
- **Sie sind für das von Ihnen aufgebaute Mesh verantwortlich.** Jeder
  Knoten vertraut jedem Peer, der das gemeinsame Geheimnis hält; ein
  kompromittierter Knoten kann das gesamte Mesh kompromittieren (siehe
  `SECURITY.md`/`SECURITY.en.md`). Schlüsselmanagement,
  Transportverschlüsselung, Listener-Bindung und Aufnahmeentscheidungen
  liegen allein bei Ihnen.
- **Kosten sind Ihre.** Agenten-CLIs, Modell-APIs, Bandbreite, Rechenleistung
  und andere über die Software aufgerufene kostenpflichtige Dienste werden
  Ihren Konten unter Ihren eigenen Vereinbarungen mit diesen Anbietern
  belastet. Wiederholungen, Delegationsketten und außer Kontrolle geratene
  Agenten können API-Kontingent unbegrenzt verbrauchen. Der Lizenzgeber
  haftet nicht für entstandene Kosten.
- **Daten sind Ihre.** Backups, Snapshots und die Vertraulichkeit von
  Dateien, Zugangsdaten und personenbezogenen Daten auf Ihren Systemen
  liegen in Ihrer Verantwortung. An Agenten-Anbieter Dritter
  weitergeleitetes Material wird unter Ihrer Konfiguration und deren
  Bedingungen übertragen — prüfen Sie deren Datenschutzrichtlinien, bevor
  Sie solche Pfade aktivieren.
- **Keine Fachberatung.** Durch die Software erzeugte Pläne, Analysen,
  Antworten und Code sind keine Rechts-, Medizin-, Finanz-, Sicherheits-
  oder sonstige Fachberatung und werden nicht als richtig oder für
  irgendeine Verwendung geeignet dargestellt.

## 5. Dienste und Komponenten Dritter

Die Software orchestriert, ruft auf oder interagiert mit Agenten-CLIs
Dritter, Modellanbietern, Paketregistern und Release-Kanälen („Dienste
Dritter"). Dienste Dritter unterliegen ausschließlich deren eigenen
Bedingungen und Richtlinien. Der Lizenzgeber kontrolliert sie nicht,
empfiehlt sie nicht und übernimmt keinerlei Verantwortung für sie, ihre
Ausgaben, ihre Verfügbarkeit, Änderungen an ihnen oder für Daten, die Sie
an sie übermitteln, und haftet nicht für Handlungen oder Unterlassungen
eines Dienstes Dritter. Mitgelieferte Komponenten Dritter sind in
`THIRD_PARTY_NOTICES.md` aufgeführt und werden Ihnen unter deren eigenen
Lizenzen lizenziert, die diese Komponenten regeln.

## 6. HAFTUNGSBEGRENZUNG

SOWEIT NACH ANWENDBAREM RECHT ZULÄSSIG, HAFTEN DER LIZENZGEBER, SEINE
VERBUNDENEN UNTERNEHMEN ODER JEGLICHE BEITRAGENDEN ODER VERTRIEBENDEN DER
SOFTWARE IN KEINEM FALL IHNEN ODER DRITTEN GEGENÜBER FÜR INDIREKTE,
BEILÄUFIGE, BESONDERE, FOLGE-, EXEMPLARISCHE ODER STRAFENDE SCHÄDEN ODER
FÜR VERLUST VON GEWINNEN, EINNAHMEN, DATEN, FIRMENWERT,
GESCHÄFTSGELEGENHEITEN ODER ERWARTETEN EINSPARUNGEN ODER FÜR DIE KOSTEN
VON ERSATZGÜTERN ODER -DIENSTEN, DIE AUS DER SOFTWARE ODER DIESEN
BEDINGUNGEN ENTSTEHEN ODER DAMIT IN ZUSAMMENHANG STEHEN — EINSCHLIESSLICH,
ABER NICHT BESCHRÄNKT AUF, SCHÄDEN DURCH VON DER SOFTWARE ODER VON IHR
KOORDINIERTEN AGENTEN AUSGEFÜHRTE BEFEHLE ODER AKTIONEN, DATENVERLUST ODER
-BESCHÄDIGUNG, SICHERHEITSVORFÄLLE ODER UNBEFUGTEN ZUGRIFF,
NETZWERKEXPOSITION, AUSFÄLLE ODER KOSTEN VON DIENSTEN DRITTER ODER
GESCHÄFTSUNTERBRECHUNG — WIE AUCH IMMER VERURSACHT UND UNTER JEDER
HAFTUNGSTHEORIE (VERTRAG, UNERLAUBTE HANDLUNG, FAHRLÄSSIGKEIT, VERSCHULDENS-
UNABHÄNGIGE HAFTUNG, GESETZ ODER SONSTIGES), SELBST WENN EINE PARTEI AUF
DIE MÖGLICHKEIT SOLCHER SCHÄDEN HINGEWIESEN WURDE UND SELBST WENN EIN
RECHTSMITTEL SEINEN WESENTLICHEN ZWECK VERFEHLT.

SOWEIT HAFTUNG NICHT VOLLSTÄNDIG AUSGESCHLOSSEN WERDEN KANN, IST DIE
GESAMTHAFTUNG DES LIZENZGEBERS, DIE AUS DER SOFTWARE ODER DIESEN
BEDINGUNGEN ENTSTEHT ODER DAMIT IN ZUSAMMENHANG STEHT, AUF DEN BETRAG
BESCHRÄNKT, DEN SIE DEM LIZENZGEBER FÜR DIE SOFTWARE IN DEN ZWÖLF MONATEN
VOR DEM DEN ANSPRUCH BEGRÜNDENDEN EREIGNIS GEZAHLT HABEN — WAS FÜR DIE
COMMUNITY-EDITION NULL IST.

DIESE BESTIMMUNG SCHLIESST HAFTUNG NICHT AUS ODER BEGRENZT SIE, SOWEIT
ZWINGENDES RECHT DEN AUSSCHLUSS ODER DIE BEGRENZUNG NICHT GESTATTET, WIE
HAFTUNG FÜR VORSATZ ODER GROBE FAHRLÄSSIGKEIT ODER FÜR TOD ODER
KÖRPERVERLETZUNG, SOWEIT DAS GESETZ DEN AUSSCHLUSS VERBIETET.

JEDER ANSPRUCH ODER JEDE KLAGE, DIE AUS DER SOFTWARE ODER DIESEN
BEDINGUNGEN ENTSTEHT ODER DAMIT IN ZUSAMMENHANG STEHT, MUSS INNERHALB
VON EINEM (1) JAHR NACH ENTSTEHUNG DES ANSPRUCHS ERHOBEN WERDEN; DANACH
IST ER DAUERHAFT VERJÄHRT, SOWEIT NICHT UNABDINGBARES RECHT EINE LÄNGERE
FRIST VERLANGT.

## 7. Freistellung

Soweit nach anwendbarem Recht zulässig, werden Sie den Lizenzgeber und
alle Beitragenden und Vertreibenden der Software von sämtlichen Ansprüchen,
Schäden, Verlusten, Verbindlichkeiten, Kosten und Auslagen (einschließlich
angemessener Anwaltsgebühren) freistellen, sie dagegen verteidigen und
schadlos halten, soweit sie entstehen aus oder in Zusammenhang stehen mit:
(a) Ihrer Nutzung, Ihrem Betrieb oder Ihrem Deployment der Software;
(b) Aktionen, die die Software oder von ihr koordinierte Agenten auf
Systemen unter Ihrer Kontrolle ausführen; (c) Ihrer Veränderung oder
Weiterverbreitung der Software; (d) Ihrem Verstoß gegen diese Bedingungen
oder gegen eine anwendbare Lizenz oder Bedingungen Dritter; oder (e)
Ihrem Verstoß gegen anwendbares Recht oder Rechte Dritter.

## 8. Hochrisikotätigkeiten

Die Software ist nicht für Umgebungen konzipiert, zertifiziert oder
gewährleistet, in denen ihr Ausfall oder ihre Fehlfunktion zu Tod,
Körperverletzung oder schweren Umwelt- oder Sachschäden führen könnte —
einschließlich lebenserhaltender Systeme, Waffensysteme, Notfallkommunikation
oder sicherheitskritischer Steuerung von Flugzeugen, Fahrzeugen oder
Industrieanlagen. Entscheiden Sie sich für einen Einsatz in solchen
Kontexten, geschieht dies vollständig auf eigenes Risiko, und die
Freistellung in Abschnitt 7 gilt für daraus resultierende Ansprüche.

## 9. Experimentelle Funktionen

Als experimentell oder Vorschau dokumentierte Funktionen (zum Beispiel die
`panda rpc`-Einbettungsoberfläche, Subscription-OAuth und Sprache) werden
nur zur Evaluierung bereitgestellt; sie können unvollständig sein, sich
inkompatibel ändern oder in einem beliebigen Release entfernt werden. Alle
diese Bedingungen gelten für sie in vollem Umfang — rechnen Sie damit,
dass sie brechen.

## 10. Datenschutz und Datenverarbeitung

Die Software ist local-first: Sie sendet keine Telemetrie an den
Lizenzgeber und überträgt Ihren Code, Ihre Prompts und Dateien nur dorthin,
wohin Ihre eigene Konfiguration sie leitet (zum Beispiel an Agenten-Anbieter
Dritter oder Release-/Update-Kanäle wie GitHub, die jeweils ihrer eigenen
Datenschutzrichtlinie unterliegen). Personenbezogene Daten, die über Ihr
Deployment verarbeitet werden, verarbeiten Sie als Verantwortlicher; die
Einhaltung des Datenschutzrechts auf Ihren Systemen liegt in Ihrer
Verantwortung.

## 11. Rechtmäßige Nutzung; Exportkontrolle

Sie dürfen die Software nur im Einklang mit anwendbarem Recht nutzen. Sie
dürfen sie nicht nutzen, um Rechte anderer zu verletzen, rechtswidrige
Überwachung oder Eindringen durchzuführen, Schadsoftware zu erstellen oder
zu verbreiten oder für sonstige rechtswidrige Zwecke. Sie versichern, sich
nicht in einem umfassend embargierten Gebiet zu befinden, auf keiner
anwendbaren Sanktions- oder Restricted-Party-Liste zu stehen und die
Software nicht unter Verstoß gegen anwendbares Exportkontroll- oder
Sanktionsrecht zu exportieren, zu reexportieren oder zu übertragen.

## 12. Marken

„OpenPanda" und zugehörige Marken sind Marken des Lizenzgebers. Weder diese
Bedingungen noch die AGPL gewähren eine Markenlizenz: Sie dürfen den Namen
oder die Marken ohne schriftliche Genehmigung nicht verwenden, um abgeleitete
Produkte oder Dienste zu kennzeichnen oder eine Billigung anzudeuten. Siehe
`NOTICE`.

## 13. Beitragende

Jeder Haftungsausschluss, jede Haftungsbegrenzung und jeder Schutz in diesen
Bedingungen erstreckt sich auf jeden einzelnen Beitragenden und
Vertreibenden der Software. Soweit gesetzlich zulässig, haftet kein
Beitragender persönlich für etwas, das die Abschnitte 3 bis 7 abdecken.

## 14. Keine Pflicht zu Pflege oder Support

Der Lizenzgeber ist nicht verpflichtet, Updates, Korrekturen, Wartung oder
Support zu leisten oder die Software weiterzuentwickeln oder zu vertreiben.
Releases können jederzeit hinzugefügt, geändert oder zurückgezogen werden.
Support, Freistellung zu Ihren Gunsten oder SLA-Zusagen bestehen nur unter
einem unterzeichneten kommerziellen Vertrag.

## 15. Änderungen dieser Bedingungen

Der Lizenzgeber kann diese Bedingungen von Zeit zu Zeit revidieren. Eine
Revision gilt für danach veröffentlichte Releases; die mit einem
bestimmten Release ausgelieferten Bedingungen regeln dieses Release
weiterhin. Die Einwilligungskarte in der Anwendung ist nur eine
Zusammenfassung — bei Abweichungen gehen `LICENSE` und diese Bedingungen
vor.

## 16. Anwendbares Recht und Gerichtsstand

Diese Bedingungen und jede aus ihnen oder der Software entstehende oder
damit in Zusammenhang stehende Streitigkeit unterliegen dem Recht der
Rechtsordnung, in der der Lizenzgeber seinen Sitz oder seine
Hauptniederlassung hat, unter Ausschluss ihrer Kollisionsnormen. Die
Gerichte an diesem Ort haben ausschließliche Zuständigkeit für solche
Streitigkeiten, es sei denn, eine Partei kann wegen Missbrauchs geistigen
Eigentums bei jedem zuständigen Gericht einstweilige oder sonstige
vorläufige Rechtsbehelfe beantragen, und soweit zwingendes
Verbraucherschutzrecht Ihnen einen anderen Gerichtsstand gewährt. Das
Übereinkommen der Vereinten Nationen über Verträge über den internationalen
Warenkauf findet keine Anwendung.

## 17. Verschiedenes

- **Teilnichtigkeit.** Wird eine Bestimmung dieser Bedingungen für
  undurchsetzbar gehalten, wird sie im größtmöglichen zulässigen Umfang
  durchgesetzt und der Rest bleibt in Kraft.
- **Kein Verzicht.** Die Nichtdurchsetzung einer Bestimmung ist kein
  Verzicht auf sie.
- **Fortbestand.** Die Abschnitte 3 bis 7 und 12 bis 17 bestehen über jede
  Beendigung Ihrer Nutzung der Software oder Ihrer Rechte an ihr hinaus
  fort.
- **Keine Vertretung.** Nichts in diesen Bedingungen oder in Ihrer Nutzung
  der Software begründet eine Partnerschaft, ein Joint Venture, ein
  Arbeits- oder Vertretungsverhältnis zwischen Ihnen und dem Lizenzgeber.
- **Abtretung.** Der Lizenzgeber kann diese Bedingungen im Zusammenhang mit
  einer Umorganisation, einem Verkauf von Vermögenswerten oder einer
  Übertragung des Projekts abtreten oder übertragen. Sie dürfen Ihre Rechte
  oder Pflichten daraus nicht ohne vorherige schriftliche Zustimmung des
  Lizenzgebers abtreten; jede vorgebliche Abtretung ohne Zustimmung ist
  nichtig.
- **Höhere Gewalt.** Der Lizenzgeber haftet nicht für Nichtleistung oder
  Verzögerungen aus Gründen außerhalb seiner zumutbaren Kontrolle.
- **Mitteilungen.** Der Lizenzgeber kann Mitteilungen zur Software und zu
  diesen Bedingungen durch Veröffentlichung im Projekt-Repository, auf der
  Projekt-Website oder in Release-Notes geben; solche Mitteilungen sind mit
  Veröffentlichung wirksam.
- **Überschriften.** Abschnittsüberschriften dienen nur der Übersicht und
  beeinflussen die Auslegung nicht.
- **Gesamte Vereinbarung.** Zusammen mit der Lizenz, unter der Ihre Kopie
  übermittelt wird, stellen diese Bedingungen die gesamte Vereinbarung
  zwischen Ihnen und dem Lizenzgeber über die Nutzung der Software dar und
  gehen früheren Absprachen zu diesem Gegenstand vor.
- **Sprache.** Diese Bedingungen werden auf Englisch, Chinesisch,
  Japanisch, Spanisch und Deutsch veröffentlicht; die englische Fassung
  gilt.
- **Kontakt.** Xustalis — fing2024@outlook.com.
