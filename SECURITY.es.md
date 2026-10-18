# Seguridad

Idiomas: 简体中文（为准）[SECURITY.md](SECURITY.md) · [English](SECURITY.en.md) · [日本語](SECURITY.ja.md) · Español · [Deutsch](SECURITY.de.md)

*Traducción al español de [SECURITY.md](SECURITY.md); en caso de discrepancia rige la versión en chino.*

OpenPanda es un núcleo de orquestación de tareas P2P que se ejecuta en
sus propios dispositivos. Este documento explica su modelo de confianza,
las líneas rojas del despliegue y el canal para notificar
vulnerabilidades. **Léalo antes de conectar cualquier nodo a una red.**

## Modelo de confianza

- **Un único secreto compartido protege toda la red (P2-8, parcialmente
  mitigado).** La única credencial de pertenencia en el bus entre nodos
  es `shared_secret` (`OPENPANDA_SHARED_SECRET`). La firma de identidad
  de `hello` es `HMAC-SHA256(secret, nodeID + ":" + ts + ":" + nonce)`,
  que vincula una marca temporal y un nonce por marcación para limitar
  la reutilización (véase [`docs/protocol.md`](docs/protocol.md)).
  Además, cada nodo mantiene una clave de identidad Ed25519 persistente:
  el consentimiento de tier-2 (irreversible) viaja con la tarea como una
  firma `Ed25519(taskID, authorized, ts, ConsentDigest)`, y el lado
  ejecutor la verifica contra la clave pública del nodo de origen
  registrada en su directorio (`internal/core/nodekey.go`); las firmas
  extraídas o manipuladas se rechazan directamente. `panda nodes verify`
  fija manualmente las huellas de los nodos, y un hello con clave nueva
  de un nodo fijado se rechaza. **Brecha restante:** quien posee el
  secreto compartido aún puede falsificar consentimiento «de un nodo cuya
  clave pública el receptor nunca vio» (límite inherente al relé
  multi-salto + TOFU — esa ruta imprime una advertencia), y las
  identidades sin fijar se aceptan en el primer uso. Por tanto, el
  invariante «las operaciones irreversibles requieren mi aprobación»
  solo se mantiene en **una sola máquina o una LAN plenamente
  confiable**; deja de mantenerse en cuanto la red contiene un nodo en
  el que usted no confía plenamente.
- **Límite del cifrado de transporte.** Desde v0.0.11, cuando ambos pares
  anuncian la capacidad `sessaead` en `hello` y existe un secreto
  compartido, la conexión activa un AES-256-GCM por conexión justo tras
  el saludo — la clave se deriva por HKDF del secreto compartido y de
  ambos nonces de hello, vinculada a ambas identidades y fresca por
  marcación; desde entonces todo marco de aplicación viaja sellado, de
  modo que un observador en el camino no puede leer el plano de tareas ni
  inyectar marcos. Solo los enlaces que no negocian cifrado de sesión
  se enfrentan a la puerta de texto plano (evaluada tras el saludo,
  `cleartextOK`): las marcaciones `ws://` solo se permiten a loopback /
  Tailscale (100.64.0.0/10, `fd7a:115e:a214::/48`, `*.ts.net`) o a
  rangos/hosts listados en `network.allow_cleartext_for` — todo otro
  destino debe ser `wss://` (TLS terminado en un proxy inverso), pares
  `punch:` (el plano UDP) o estar explícitamente cubierto por
  `network.allow_cleartext`; el texto plano entrante se juzga por la IP
  de origen de la misma forma. El plano de datagramas UDP se envuelve de
  extremo a extremo en AES-256-GCM (claves separadas por dominio del
  secreto compartido); las cargas útiles DTN bundle v2+ también se
  cifran y firman con AES-256-GCM, y los bundles manipulados nunca se
  entregan. **Los enlaces en texto plano solo se permiten en rutas ya
  cifradas por debajo (loopback / WireGuard / Tailscale / proxy TLS), o
  en rutas que activaron `sessaead` por sí mismas tras el saludo.**
- **La cadena de auditoría está firmada, pero anclada dentro de la base
  de datos.** `task_events` y `audit_log` llevan firmas Ed25519 por fila
  sobre la cadena de hash (P2-9); la verificación se vincula a la clave
  pública del nodo local (volver a firmar tras cambiar la clave se
  rechaza), y una cadena firmada no puede retroceder a filas sin firmar
  (anti-truncamiento). Pero el ancla de la firma vive en la propia base
  de datos: **un prefijo de historial sin firmar no puede distinguirse
  de uno reescrito por completo**. Endurecer esto requiere atestación
  externa (exportar/notarizar la cabeza de la cadena) y sigue en la
  hoja de ruta.
- **La autoactualización verifica una firma Ed25519 separada.** Desde
  v0.0.11-preview el canal oficial publica `checksums.txt.sig` — una
  firma separada sobre `checksums.txt`. La clave pública oficial de
  releases está compilada en cada binario (valor por defecto en
  `internal/version.ReleasePubKey`; el empaquetado puede rotarla por
  ldflags), así que las sumas falsificadas que viajan por el mismo canal
  que el archivo se rechazan directamente:

  `a84a556bd45ee7743437d7b9b0c4f6e0df34c496f639b5a045d5de484f128560`

  Las compilaciones antiguas sin clave integrada (artefactos compilados
  antes de v0.0.11-preview) rechazan las actualizaciones por defecto:
  establezca `OPENPANDA_UPDATE_PUBKEY=<la clave anterior>` para una
  actualización verificada al canal firmado, o explícitamente
  `OPENPANDA_UPDATE_UNSIGNED=1` para volver una vez a solo sumas de
  comprobación (los hashes y los archivos cruzan el mismo canal — una
  escotilla de escape, no una postura). `OPENPANDA_UPDATE_REPO` apunta
  las actualizaciones a un canal autoalojado; combínelo con
  `OPENPANDA_UPDATE_PUBKEY` con la clave propia de ese canal.
- **Las instalaciones de skills por URL tienen egress acotado.** `panda
  skill install <url>` y las rutas correspondientes del panel / MCP solo
  permiten https (excepto loopback), y rechazan loopback / direcciones
  privadas / link-local y otras reservadas al momento de marcar,
  evitando que un nodo se convierta en trampolín de sondeo de la
  intranet. Un hub privado configurado mediante `skills.hub_url` está
  exento — esa es la decisión de despliegue del propio administrador.

Consecuencia: **confianza plena e igual por defecto — cada nodo cuenta
como tier `mesh`, y un nodo comprometido compromete toda la malla.** No
extienda la malla a máquinas que no controle por completo. Desde v0.0.11
puede fijar un nivel de confianza por par sobre la identidad del par
(`network.peers[].trust` / `panda nodes trust`, experimental): `guarded`
acota lo que los delegados hacia ese par pueden llevar y admite el trabajo
que origina bajo `network.trust.accept.guarded`, mientras los pares `open`
solo figuran en el directorio y no intercambian delegaciones en modo push.
Un nivel de confianza es una declaración local y unilateral — estrecha su
exposición hacia un par, pero no cambia el hecho subyacente de que poseer
el secreto compartido significa pertenencia.
Las tareas de captura de pantalla y sus artefactos de salida llevan un
estándar aún más estricto: el contenido de pantalla solo circula por
enlaces mesh (`send.guarded.screen` es la excepción explícita del mismo
propietario) — el ejecutor sella `screen_output` al reclamar cuando
resuelve un actuador `screen_sensing`, la marca viaja en `task_result` de
modo que cada salto delegador vuelve a sellar su propia fila, y el lado
que sirve artefactos libera esa salida solo a pares mesh, pese a la
participación y a los grants firmados; una degradación de confianza corta
una transferencia en curso de inmediato en lugar de aguardar el TTL de la
caché de autorización.

## Verificación de integridad

`panda audit verify` (o el `GET /api/audit` del panel) verifica la cadena
de hash y las firmas Ed25519 de la cadena de auditoría global; `panda
audit verify --task <id>` verifica la cadena de eventos de una sola
tarea. Ambas se vinculan a la clave pública del nodo local, y las firmas
ausentes (dentro de una cadena firmada), las firmas extraídas o volver a
firmar con una clave nueva se reportan como errores.

## Líneas rojas del despliegue

Hasta que llegue la distribución de claves por nodo / la admisión
verificable (un cambio del modelo de confianza, no un parche), lo
siguiente es obligatorio:

1. **Interconectar solo por overlays cifrados.** Conecte los nodos vía
   Tailscale / WireGuard o similar; vincule `listen_addr` a la interfaz
   del overlay; **nunca exponga el puerto del bus en internet público ni
   en una LAN no confiable**. El cifrado de sesión `sessaead` da a los
   enlaces de una LAN confiable un suelo de confidencialidad, pero solo
   autentica el secreto compartido — no detiene ataques activos de quien
   posee el secreto, por lo que la superficie de exposición se mantiene
   mínima en cualquier caso. Si un enlace debe cruzar internet público,
   termine TLS en un proxy inverso y escriba el par como
   `wss://host:port`.
2. **Secretos solo por variables de entorno.** Proporcione
   `shared_secret` únicamente mediante `OPENPANDA_SHARED_SECRET`; nunca
   lo escriba en `config.yaml` ni lo confirme en ningún repositorio.
   Ejemplo de generación: `openssl rand -hex 32`.
3. **Sin capacidades tier-2 para remotos desconocidos.** Otorgue
   capacidades irreversibles (tier-2) en `capabilities.yaml` solo a
   nodos en los que confíe plenamente.
4. **Mantenga el panel en loopback.** Vincule consola / panel
   (`panel_addr`) solo a `127.0.0.1`; en un enlace no loopback sin
   `panel_token` se genera un token temporal único con una advertencia
   llamativa — `/api/*` nunca corre desprotegido, pero un token sobre
   HTTP en texto plano puede interceptarse en el enlace, así que para
   exposición externa prolongada configure un token estable o un proxy
   inverso TLS. No pegue URLs de auto-login que contengan el token en
   chats ni en sistemas de tickets.
5. **Escucha en loopback por defecto.** El bus escucha en loopback por
   defecto; sin `shared_secret` configurado, el escucha no arranca en
   absoluto y el nodo corre solo localmente (una restricción de
   seguridad, véase `docs/install.md`).

## Notificar vulnerabilidades

No abra issues públicos por fallos en la autenticación de transporte, el
modelo de niveles de permiso o la capa de ocultación de secretos.
Notifíquelos **en privado** al mantenedor (véase el contacto de seguridad
en la configuración del repositorio), con pasos de reproducción. Véase
«Reporting security issues» en [CONTRIBUTING.md](CONTRIBUTING.md).
