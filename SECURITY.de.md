# Sicherheit

Sprachen: 简体中文（为准）[SECURITY.md](SECURITY.md) · [English](SECURITY.en.md) · [日本語](SECURITY.ja.md) · [Español](SECURITY.es.md) · Deutsch

*Deutsche Übersetzung von [SECURITY.md](SECURITY.md); bei Abweichungen gilt die chinesische Fassung.*

OpenPanda ist ein P2P-Aufgaben-Orchestrierungskern, der auf Ihren eigenen
Geräten läuft. Dieses Dokument erklärt sein Vertrauensmodell, die
roten Linien beim Deployment und den Meldeweg für Sicherheitslücken.
**Lesen Sie dies, bevor Sie irgendeinen Knoten an ein Netzwerk anschließen.**

## Vertrauensmodell

- **Ein einzelnes gemeinsames Geheimnis schützt das gesamte Netzwerk
  (P2-8, teilweise entschärft).** Die einzige Mitgliedschaftskennung auf
  dem Inter-Knoten-Bus ist `shared_secret` (`OPENPANDA_SHARED_SECRET`).
  Die `hello`-Identitätssignatur ist `HMAC-SHA256(secret, nodeID + ":" +
  ts + ":" + nonce)` und bindet einen Zeitstempel sowie eine Nonce je
  Wählvorgang, um Replay einzuschränken (siehe
  [`docs/protocol.md`](docs/protocol.md)). Darüber hinaus hält jeder
  Knoten einen persistenten Ed25519-Identitätsschlüssel: Tier-2-
  (irreversible) Zustimmung reist mit der Aufgabe als
  `Ed25519(taskID, authorized, ts, ConsentDigest)`-Signatur, und die
  ausführende Seite prüft sie gegen den öffentlichen Schlüssel des
  Ursprungsknotens, wie er in ihrem Verzeichnis verzeichnet ist
  (`internal/core/nodekey.go`); abgestreifte oder manipulierte
  Signaturen werden schlicht abgelehnt. `panda nodes verify` pinnt
  Knoten-Fingerprints manuell, und ein umschlüsseltes hello eines
  gepinnten Knotens wird verweigert. **Verbleibende Lücke:** Ein Inhaber
  des gemeinsamen Geheimnisses kann weiterhin Zustimmung „von einem
  Knoten, dessen öffentlichen Schlüssel der Empfänger nie gesehen hat"
  fälschen (eine inhärente Grenze von Multi-Hop-Relay + TOFU — dieser
  Pfad gibt eine Warnung aus), und ungepinnte Identitäten werden bei
  Erstkontakt vertraut. Daher gilt die Invariante „irreversible
  Operationen erfordern meine Freigabe" nur auf **einem einzelnen Rechner
  oder einem vollständig vertrauten LAN**; sie hört auf zu gelten,
  sobald das Netzwerk einen Knoten enthält, dem Sie nicht vollständig
  vertrauen.
- **Grenze der Transportverschlüsselung.** Seit v0.0.11 rüstet die
  Verbindung direkt nach dem Handshake eine AES-256-GCM-Verbindung je
  Verbindung auf, wenn beide Peers die Fähigkeit `sessaead` in `hello`
  anbieten und ein gemeinsames Geheimnis existiert — der Schlüssel wird
  per HKDF aus dem gemeinsamen Geheimnis und beiden hello-Nonces
  abgeleitet, an beide Identitäten gebunden und pro Wählvorgang frisch;
  jeder Anwendungsframe reist ab dann versiegelt, sodass ein
  On-Path-Beobachter die Aufgabenebene weder lesen noch Frames
  einschleusen kann. Nur Verbindungen, die keine Sitzungsverschlüsselung
  aushandeln, unterliegen dem Klartext-Gate (nach dem Handshake
  bewertet, `cleartextOK`): `ws://`-Wählvorgänge sind nur zu Loopback /
  Tailscale (100.64.0.0/10, `fd7a:115e:a214::/48`, `*.ts.net`) oder zu
  in `network.allow_cleartext_for` aufgeführten Bereichen/Hosts erlaubt —
  alle anderen Ziele müssen `wss://` (TLS an einem Reverse-Proxy
  terminiert), `punch:`-Peers (die UDP-Ebene) oder ausdrücklich durch
  `network.allow_cleartext` abgedeckt sein; eingehender Klartext wird am
  selben Maßstab an der Quell-IP beurteilt. Die UDP-Datagramm-Ebene ist
  Ende-zu-Ende in AES-256-GCM gehüllt (Schlüssel vom gemeinsamen
  Geheimnis domain-getrennt); DTN-Bundle-v2+-Nutzlasten sind ebenfalls
  AES-256-GCM-verschlüsselt und -signiert, und manipulierte Bundles
  werden nie zugestellt. **Klartext-Verbindungen sind nur auf Pfaden
  erlaubt, die darunter bereits verschlüsselt sind (Loopback /
  WireGuard / Tailscale / TLS-Proxy), oder auf solchen, die nach dem
  Handshake selbst `sessaead` aufgerüstet haben.**
- **Die Audit-Kette ist signiert, aber in der Datenbank verankert.**
  `task_events` und `audit_log` tragen Ed25519-Signaturen je Zeile über
  die Hash-Kette (P2-9); die Verifikation bindet an den öffentlichen
  Schlüssel des lokalen Knotens (Neu-Signieren nach Umschlüsselung wird
  abgelehnt), und eine signierte Kette darf nicht auf unsignierte Zeilen
  zurückfallen (Anti-Kürzung). Doch der Signatur-Anker liegt in der
  Datenbank selbst: **ein unsignierter Historien-Präfix ist von einem
  vollständig neu geschriebenen nicht zu unterscheiden**. Eine weitere
  Härtung erfordert externe Attestierung (Export/Beglaubigung des
  Kettenkopfes) und bleibt auf der Roadmap.
- **Selbst-Update prüft eine detached Ed25519-Signatur.** Seit
  v0.0.11-preview liefert der offizielle Kanal `checksums.txt.sig` — eine
  detached Signatur über `checksums.txt`. Der offizielle Release-
  Public-Key ist in jede Binärdatei einkompiliert
  (`internal/version.ReleasePubKey`-Quellstandard; die Release-
  Verpackung kann ihn per ldflags rotieren), sodass gefälschte
  Prüfsummen, die denselben Kanal wie das Archiv reisen, schlicht
  abgelehnt werden:

  `a84a556bd45ee7743437d7b9b0c4f6e0df34c496f639b5a045d5de484f128560`

  Ältere Builds ohne eingebrannten Schlüssel (selbst kompilierte
  Artefakte vor v0.0.11-preview) verweigern Updates standardmäßig:
  setzen Sie `OPENPANDA_UPDATE_PUBKEY=<den obigen Schlüssel>` für ein
  verifiziertes Upgrade auf den signierten Kanal, oder ausdrücklich
  `OPENPANDA_UPDATE_UNSIGNED=1`, um einmalig auf reine Prüfsummen
  zurückzufallen (Hashes und Archive reisen denselben Kanal — ein
  Notausgang, kein Zustand). `OPENPANDA_UPDATE_REPO` richtet Updates auf
  einen selbst gehosteten Kanal; kombinieren Sie es mit
  `OPENPANDA_UPDATE_PUBKEY` auf den eigenen Schlüssel dieses Kanals.
- **Skill-URL-Installationen sind egress-beschränkt.** `panda skill
  install <url>` und die entsprechenden Panel-/MCP-Pfade erlauben nur
  https (Loopback ausgenommen) und lehnen Loopback-, private,
  Link-Local- und andere reservierte Adressen zur Wählzeit ab — so kann
  ein Knoten nicht zum Sprungbrett für Intranet-Scans gemacht werden.
  Ein über `skills.hub_url` konfigurierter privater Hub ist ausgenommen —
  das ist die eigene Deployment-Entscheidung der Administration.

Konsequenz: **standardmäßig gleiches, volles Vertrauen — jeder Knoten
zählt als `mesh`-Tier, und ein kompromittierter Knoten kompromittiert das
gesamte Mesh.** Erweitern Sie das Mesh nicht um Maschinen, die Sie nicht
vollständig kontrollieren. Seit v0.0.11 können Sie auf die Identität
eines Peers eine Vertrauensstufe je Peer pinnen (`network.peers[].trust`
/ `panda nodes trust`, experimentell): `guarded` begrenzt, was an diesen
Peer delegiert werden darf, und lässt von ihm ausgehende Arbeit unter
`network.trust.accept.guarded` zu, während `open`-Peers nur im Verzeichnis
stehen und keine Push-Delegationen austauschen. Eine Vertrauensstufe ist
eine lokale, einseitige Festlegung — sie verengt Ihre Exposition gegenüber
einem Peer, ändert aber nicht die zugrunde liegende Tatsache, dass der
Besitz des gemeinsamen Geheimnisses Mitgliedschaft bedeutet.
Bildschirmaufnahme-Aufgaben und ihre Ausgabearfakte tragen einen noch
strengeren Standard: Anzeigeinhalte bewegen sich nur über Mesh-
Verbindungen (`send.guarded.screen` ist die explizite Ausnahme desselben
Eigentümers) — der Ausführer prägt `screen_output` beim Claim, wenn er
einen `screen_sensing`-Aktuator auflöst, die Markierung reist auf
`task_result`, sodass jeder Delegator-Hop seine eigene Zeile neu prägt,
und die artefaktausliefernde Seite gibt solche Ausgaben nur an Mesh-Peers
frei, Teilnahme und signierte Grants ungeachtet; eine Vertrauensstufen-
Herabstufung unterbricht eine laufende Übertragung sofort, statt die TTL
des Autorisierungs-Caches abzuwarten.

## Integritätsverifikation

`panda audit verify` (oder `GET /api/audit` des Panels) verifiziert die
Hash-Kette und die Ed25519-Signaturen der globalen Audit-Kette; `panda
audit verify --task <id>` verifiziert die Ereigniskette einer einzelnen
Aufgabe. Beide binden an den öffentlichen Schlüssel des lokalen Knotens,
und fehlende Signaturen (innerhalb einer signierten Kette), abgestreifte
Signaturen oder Neu-Signieren unter einem neuen Schlüssel werden alle als
Fehler gemeldet.

## Rote Linien beim Deployment

Bis schlüsselbasierte Verteilung je Knoten / verifizierbare Aufnahme
landet (eine Änderung des Vertrauensmodells, kein Patch), ist Folgendes
verbindlich:

1. **Nur über verschlüsselte Overlays verbinden.** Knoten über Tailscale /
   WireGuard o. ä. verbinden; `listen_addr` an das Overlay-Interface
   binden; **den Bus-Port niemals auf das öffentliche Internet oder ein
   nicht vertrautes LAN exponieren**. Die `sessaead`-Sitzungschiffre gibt
   Verbindungen in einem vertrauten LAN eine Vertraulichkeitsbasis,
   authentifiziert aber nur das gemeinsame Geheimnis — sie stoppt keine
   aktiven Angriffe durch einen Geheimnisinhaber, sodass die
   Angriffsfläche ohnehin minimal bleibt. Muss eine Verbindung das
   öffentliche Internet queren, terminieren Sie TLS an einem
   Reverse-Proxy und schreiben Sie den Peer als `wss://host:port`.
2. **Geheimnisse nur über Umgebungsvariablen.** `shared_secret`
   ausschließlich über `OPENPANDA_SHARED_SECRET` bereitstellen; niemals
   in `config.yaml` schreiben oder in ein Repository committen.
   Erzeugungsbeispiel: `openssl rand -hex 32`.
3. **Keine tier-2-Fähigkeiten für unbekannte Gegenstellen.** Irreversible
   (tier-2-) Fähigkeiten in `capabilities.yaml` nur Knoten gewähren, denen
   Sie vollständig vertrauen.
4. **Das Panel auf Loopback halten.** Konsole/Panel (`panel_addr`) nur an
   `127.0.0.1` binden; bei einem Nicht-Loopback-Bind ohne `panel_token`
   wird ein einmaliges temporäres Token mit lauter Warnung erzeugt —
   `/api/*` läuft nie ungeschützt, aber ein Token über Klartext-HTTP kann
   auf der Strecke mitgeschnitten werden; konfigurieren Sie für langfristige
   externe Exposition ein stabiles Token oder einen TLS-Reverse-Proxy.
   Auto-Login-URLs mit dem Token nicht in Chat- oder Ticket-Systeme
   einfügen.
5. **Standardmäßig Loopback-Listen.** Der Bus lauscht standardmäßig auf
   Loopback; ohne konfiguriertes `shared_secret` startet der Listener
   gar nicht und der Knoten läuft nur lokal (eine
   Sicherheitsbedingung, siehe `docs/install.md`).

## Sicherheitslücken melden

Öffnen Sie keine öffentlichen Issues für Schwächen in
Transport-Authentifizierung, dem Berechtigungsstufen-Modell oder der
Secret-Redaction-Schicht. Melden Sie sie **privat** an den Maintainer
(siehe den Sicherheitskontakt in den Repository-Einstellungen), mit
Reproduktionsschritten. Siehe „Reporting security issues" in
[CONTRIBUTING.md](CONTRIBUTING.md).
