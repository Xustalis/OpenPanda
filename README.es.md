# 🐼 OpenPanda

**Open** **P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant

> Cualquier dispositivo, cualquier potencia de cálculo, un solo comando.
> Un asistente personal de orquestación de tareas que se ejecuta en tus
> dispositivos heterogéneos como una red entre pares de nodos.

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.26-blue)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-blue)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)
![Status](https://img.shields.io/badge/status-pre--release-yellow)

---

## Índice

- [¿Qué es OpenPanda?](#qué-es-panda)
- [Características principales](#características-principales)
- [Arquitectura](#arquitectura)
- [Instalación](#instalación)
- [Primeros pasos](#primeros-pasos)
- [Uso](#uso)
- [Referencia de CLI](#referencia-de-cli)
- [Configuración](#configuración)
- [Documentación](#documentación)
- [Pruebas](#pruebas)
- [Despliegue](#despliegue)
- [Stack tecnológico](#stack-tecnológico)
- [Hoja de ruta](#hoja-de-ruta)
- [Contribuciones](#contribuciones)
- [Licencia](#licencia)
- [Agradecimientos](#agradecimientos)

## ¿Qué es OpenPanda?

OpenPanda **no es otro CLI de agentes** — es la capa *por encima* de ellos: el
mayordomo de todos tus dispositivos y todas tus herramientas.

Claude Code, Codex, OpenCode, OpenClaw … cada uno es un agente potente en *una*
máquina. OpenPanda no compite con ellos — los **contrata**. Sea cual sea el
dispositivo desde el que hablas, ese dispositivo se convierte en el comandante:
responde directamente cuando puede, y cuando no puede, enruta la tarea por tu red
al nodo que sí puede hacerla — entregándosela a los agentes de ese nodo (Claude
Code, Codex, …), o directamente al hardware cuando basta una acción de
dispositivo pura (un servomotor no necesita un LLM).

```
sub-agentes (una máquina)    orquestación de agentes (una máquina)   OpenPanda (muchas máquinas)
┌──────────────┐             ┌──────────────┐             ┌──────────────────────┐
│ Claude Code  │             │ multi-agente │             │ flota heterogénea    │
│ Codex …      │             │ orquestación │             │ + sus agentes        │
│              │             │              │             │ + hardware directo   │
└──────────────┘             └──────────────┘             └──────────────────────┘
                por encima de todos ellos: OpenPanda delega, ellos ejecutan
```

En la práctica: preguntas una sola vez, desde cualquier dispositivo, y OpenPanda
delega la tarea al nodo mejor preparado para ejecutarla, devuelve el resultado y
recuerda lo aprendido para la próxima vez — manteniendo el trabajo de proyectos
estrictamente aislado de la memoria personal, para que tu base de código nunca
se desvíe porque «al asistente le consta que prefieres los temas oscuros».

Está construido desde cero como un sistema **personal**: sin dependencia de la
nube, tu memoria permanece en tus dispositivos y cada nodo habla con sus pares a
través de enlaces WebSocket directos que tú controlas.

## Características principales

- **Red de nodos heterogéneos** — cada nodo publica sus capacidades reales
  (clase de CPU, shell, adaptadores de agente) mediante una tarjeta de
  capacidades; la red enruta cada tarea al nodo que realmente puede hacerla.
  Diseñado para portátiles, SBC, escritorios y todos los niveles de plataforma intermedios.
- **Modelo de entrada unificado** — una petición de entrada, tres intenciones de
  salida: `answer` (respuesta pura del LLM), `tool_call` (tus herramientas) y
  `task` (delegada a un nodo). Clasificación automática de intenciones con
  degradación elegante.
- **Ejecución de capacidades en tres niveles** — `native` (ejecución directa en
  shell), `agent` (agente basado en adaptador, p. ej. Claude Code a través de un
  endpoint compatible con Anthropic) y `manual` (en cola para que lo apruebes o
  lo ejecutes a mano).
- **Protocolo de delegación P2P** — claves `task_id` idempotentes y `attempt_id`s
  únicos por ejecución sobre WebSocket + JSON, de modo que los reintentos tras un
  fallo nunca se ejecuten dos veces.
- **Skills auto-evolutivas** — memoria procedimental en archivos `SKILL.md`: una
  skill declara cuándo aplica y cómo se ejecuta, y puede refinarse tras cada uso.
- **Herramientas de asistente diario** — el agente puede leer el reloj del
  sistema, obtener el clima en vivo y programar **recordatorios**
  (`reminder.set`): almacenados en SQLite, disparados por un escáner y
  entregados como notificaciones Web Push y actualizaciones SSE en vivo a
  cualquier consola abierta. `panda reminder list/add/rm` los gestiona desde la
  CLI.
- **Integración MCP** — un servidor MCP de stdio configurable en config.yaml
  (`mcp.command`) o en la página de ajustes de la consola; sus herramientas se
  cargan **en caliente** en el conjunto de herramientas del agente, sin
  reiniciar el daemon.
- **Memoria de dos capas** — memoria por usuario y por proyecto (estilo `USER.md`
  / `MEMORY.md`) tras un muro de aislamiento, más un motor **Dreaming** en segundo
  plano que consolida los registros diarios en memoria a largo plazo mientras el
  nodo está inactivo.
- **Entrada por voz** — pipeline sidecar opcional (palabra de activación → STT →
  LLM → TTS), controlado por hardware y listo para micrófonos embebidos.
- **REPL interactivo + consola web embebida** — `panda repl` es el puesto de
  mando: la entrada directa va al motor de preguntas, los comandos con barra
  (`/tasks`, `/approve`, `/projects`, `/nodes`, `/lang` …) gobiernan el panel y
  `/web` arranca la consola embebida en un clic. La cola de tareas es un
  **tablero kanban** (pendiente / en curso / en revisión / terminado) con
  aprobaciones en línea, más chat, recordatorios, una página de memoria
  editable (USER / MEMORY / DREAMS) y una página de ajustes (endpoint del modelo —
  compatible Anthropic u OpenAI — y servidor MCP). `panda web` es la vía de un
  solo comando: bind de loopback + token efímero por defecto, el navegador se
  abre ya autenticado. Cinco idiomas de interfaz.
- **Auto-actualización** — `panda web` (y `/web`) comprueba el canal de releases
  en segundo plano; la consola descarga y verifica una actualización disponible
  y la instala en un clic cuando la cola de tareas queda inactiva. Si descartas
  una actualización descargada, no queda ningún residuo.
- **Capas de defensa y seguridad** — niveles de permisos, un cortacircuitos,
  detección de desviación de alcance y de bucles infinitos, además de
  endurecimiento del lado de la ejecución: sandbox, listas blancas de red,
  redacción de secretos y registro de auditoría.
- **Ligero por diseño** — RSS en estado estable ≈ **13–20 MB**, pensado para
  ordenadores de placa única con recursos limitados.
- **Cross-compile limpio** — un binario estático por plataforma, sin CGO
  (SQLite en Go puro mediante `modernc.org/sqlite`).

## Arquitectura

```
                        ┌───────────────────────────┐
                        │  Tú: CLI / web / voz      │
                        └─────────────┬─────────────┘
                                      │
                 ┌────────────────────▼────────────────────┐
                 │            entry · panda ask             │
                 │  clasifica:  answer | tool_call | task   │
                 └────────────────────┬────────────────────┘
                                      │  delega por WebSocket + JSON
                       ┌──────────────┴───────────────┐
                       │                              │
          ┌────────────▼────────────┐     ┌────────────▼────────────┐
          │      Nodo trabajador    │     │      Nodo trabajador    │
          │  p. ej. Portátil (Std)  │     │   p. ej. SBC (Micro)     │
          └─────────────────────────┘     └─────────────────────────┘
```

Dentro de cada nodo:

```
┌─────────────────────────────────────────────────────────────┐
│ cmd/panda      daemon + CLI (ask / status / queue / task …)  │
├─────────────────────────────────────────────────────────────┤
│ entry          modelo de entrada unificado (answer·tool_call·task)│
│ scheduler      decisiones de delegación y enrutamiento       │
│ commander      ejecución en 3 niveles: native · agent · manual│
│ defense        niveles de permisos · circuit · drift · loops │
│ security       sandbox · listas blancas · redacción · auditoría│
│ memory         stores USER/MEMORY + motor Dreaming           │
│ skills         memoria procedimental SKILL.md                │
├─────────────────────────────────────────────────────────────┤
│ bus            transporte WebSocket + envoltorio de mensaje  │
│ ledger         directorio de capacidades (tarjetas, heartbeat)│
│ storage        SQLite (WAL) + migraciones                    │
│ log / util     logs JSON estructurados, UUIDv7              │
└─────────────────────────────────────────────────────────────┘
```

## Instalación

Consigue un binario de release en una línea — macOS, Linux o Windows; experiencia
consistente, sin necesidad de root. El instalador descarga el archivo de release
correspondiente, verifica su SHA-256, desempaqueta el binario y sus adaptadores de
agente (`adapters/*.py`) en un prefijo por usuario y enlaza `panda` en tu `PATH`.

| Plataforma | Comando |
|---|---|
| macOS / Linux | `curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh \| sh` |
| macOS (Homebrew) | `brew tap Xustalis/openpanda && brew install openpanda` |
| Windows (PowerShell) | `Set-ExecutionPolicy -Scope Process Bypass`, luego `irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 \| iex` |

Sobrescribe los valores por defecto con estos flags:

```bash
sh scripts/install.sh --version 0.0.4           # fijar versión (por defecto: latest)
sh scripts/install.sh --prefix /opt/openpanda   # directorio de instalación propio
sh scripts/install.sh --yes                     # registrar también el autoarranque, sin preguntar
sh scripts/install.sh --no-service              # omitir el autoarranque por completo
```

En macOS / Linux los archivos quedan bajo un prefijo por usuario (convención XDG):

```
${XDG_DATA_HOME:-~/.local/share}/openpanda/
├── bin/panda            # el binario real
├── adapters/*.py        # adaptadores de agente (el daemon los necesita para delegar)
├── config.example.yaml
└── capabilities.example-*.yaml
```

`~/.local/bin/panda` es un enlace simbólico a ese binario (ya en el `PATH`); si tu
shell no incluye `~/.local/bin`, el script imprime la línea `export PATH` que debes
añadir. En Windows los archivos van a `%LOCALAPPDATA%\OpenPanda\` y su `bin` se
añade al `PATH` de usuario. El instalador también puede registrar un servicio de
autoarranque (`panda daemon` al iniciar sesión) — actívalo solo tras `panda init`,
porque el daemon no arrancará sin configuración.

Tras instalar, inicializa y ejecuta:

```bash
panda init      # config.yaml + tarjeta de capacidades interactivos
panda doctor    # autodiagnóstico: binario / PATH / config / adaptadores / agentes
panda repl      # entrar en el REPL interactivo
panda web       # abrir la consola web embebida (loopback, login automático)
```

Para eliminar todo:

- macOS / Linux: `rm -rf ~/.local/share/openpanda ~/.local/bin/panda` (y detén antes
  cualquier servicio de autoarranque).
- Windows: borra `%LOCALAPPDATA%\OpenPanda` y quita su `bin` del `PATH` de usuario.

Guía completa y resolución de problemas: [docs/install.md](docs/install.md).

## Primeros pasos

### Requisitos previos

| Herramienta | Versión |
|---|---|
| Go | 1.26.5+ |
| Python | 3.10+ (adaptadores de agente / sidecar de voz) |
| make | cualquier versión reciente |

### Compilar

```bash
make build          # binario nativo → bin/panda (release, sin símbolos)
make web            # consola web embebida en el binario (requiere node/npm; omitir = página de aviso)
make test           # ejecutar toda la suite de pruebas
make vet            # análisis estático
```

Cross-compile para los dispositivos que realmente usas:

```bash
make build-linux-arm64   # → bin/panda-linux-arm64  (SBCs, placas empotradas)
make build-linux-amd64   # → bin/panda-linux-amd64
make build-darwin-arm64  # → bin/panda-darwin-arm64
make build-windows-amd64 # → bin/panda-windows-amd64.exe
```

### Configuración

Arranca un nodo de forma interactiva — el endpoint del modelo, el nombre del
nodo y una tarjeta de capacidades se generan en un solo diálogo:

```bash
./bin/panda init
```

O copia la configuración de ejemplo y edítala para cada nodo:

```bash
cp config.example.yaml /etc/openpanda/config.yaml   # o déjala local y usa --config
```

La configuración es pequeña y autoexplicativa. Lo más importante:

```yaml
network:
  listen_addr: ":7836"        # listener WebSocket
  shared_secret: "..."        # autenticación HMAC entre nodos — todos comparten el mismo valor
  peers:                      # otros nodos de tu red
    - "worker-1.your-tailnet.ts.net:7836"
model:
  base_url: "https://api.deepseek.com/anthropic"  # cualquier endpoint compatible con /v1/messages
  model: "deepseek-chat"
  # api_key: ""               # prefiere la variable de entorno OPENPANDA_MODEL_API_KEY
```

Los secretos (claves de API del modelo) se leen de `OPENPANDA_MODEL_API_KEY` en lugar
del archivo de configuración siempre que sea posible.

### Ejecutar

La forma más rápida de ver el sistema completo es la consola web de un solo
comando: bind de loopback, token efímero y el navegador se abre ya
autenticado — sin editar configuración ni pegar tokens:

```bash
./bin/panda web
```

Si aún no hay un endpoint del modelo configurado, la página de ajustes de la
consola lo gestiona directamente (compatible Anthropic u OpenAI).

Para un despliegue residente multi-nodo, arranca el propio daemon:

```bash
./bin/panda daemon --config config.yaml --card config/capabilities.example-desktop.yaml
```

Cada nodo que pueda *ejecutar* trabajo debe iniciarse con su tarjeta de
capacidades. Un nodo sin tarjeta sigue participando en los heartbeats, pero no
recibirá tareas.

## Uso

Pregunta lo que sea — el modelo de entrada decide si responde, llama a una
herramienta o delega:

```bash
./bin/panda ask "resume el git log de la última semana"
```

Inspecciona la red y la cola:

```bash
./bin/panda status
./bin/panda queue
```

Explora o cancela una tarea concreta:

```bash
./bin/panda task <task-id>
./bin/panda cancel <task-id>
./bin/panda logs <task-id>
```

Gestiona las skills:

```bash
./bin/panda skill
```

## Referencia de CLI

| Comando | Descripción |
|---|---|
| `panda` (sin argumentos) | Abre la REPL interactiva (igual que `panda repl`); el daemon ahora se ejecuta con el subcomando `panda daemon` |
| `panda daemon [--config PATH] [--card PATH]` | Ejecutar el daemon: registrar nodo, heartbeat, servidor WS, reconexión de pares |
| `panda ask [--config PATH] [--card PATH] [--authorize] "<pregunta>"` | Entrada unificada: clasifica en answer / tool_call / task / plan y ejecuta |
| `panda plan run <archivo.yaml> \| show <id> \| example` | Tubería multietapa entre dispositivos: una etapa ES una tarea ordinaria (se encola, enruta por hardware, reintenta, se estaciona en revisión), el plan aporta el orden y entrega el directorio de trabajo de cada etapa a la etapa de la siguiente máquina; `run --dry-run` valida e imprime el enrutado sin crear nada |
| `panda voice [--once] [--mute]` | Entrada de mascota de escritorio: palabra de activación → ASR → la misma tubería de entrada → TTS, para un dispositivo sin teclado; `--once` atiende una sola frase, `--mute` imprime en lugar de hablar |
| `panda repl [--config PATH] [--card PATH]` | Shell interactivo: comandos slash (tasks/approve/projects/nodes/lang), la entrada simple va al motor ask, `/web` arranca la consola incrustada |
| `panda web [--config PATH] [--card PATH] [--no-browser]` | Consola web con un solo comando: loopback + token efímero por defecto, el navegador se abre ya con la sesión iniciada |
| `panda init` | Configuración inicial interactiva: genera `config.yaml` + `capabilities.yaml` (endpoint del modelo, nombre del nodo, valores por defecto del escaneo de hardware) |
| `panda install [--dir PATH] [--no-path]` | Registra `panda` como comando global en PATH (persiste tras reiniciar) y autoverifica la copia instalada |
| `panda uninstall [--config PATH] [--yes] [--no-backup] [--dry-run]` | Eliminación segura: plan completo primero, `confirm` obligatorio, borrado solo por lista blanca, los activos del usuario (projects/memory/skills) siempre se conservan, zip de respaldo e informe |
| `panda doctor [--config PATH]` | Autochequeo: la copia instalada ejecuta, PATH resuelve, la persistencia sobrevive al reinicio, config/base de datos utilizables |
| `panda status` | Estado del nodo y de las tareas |
| `panda queue` | Listar la cola de tareas |
| `panda task [--config PATH] <task-id>` | Detalles de la tarea |
| `panda cancel [--config PATH] <task-id>` | Cancelar una tarea (en cascada al nodo ejecutor) |
| `panda approve [--config PATH] <task-id>` | Aprobar una tarea en revisión (review → done) |
| `panda reject [--config PATH] [--reason s] <task-id>` | Rechazar una tarea en revisión |
| `panda logs [--config PATH] <task-id>` | Registros de ejecución de la tarea |
| `panda skill` | Gestión del almacén de skills |
| `panda reminder list \| add \| rm` | Recordatorios: listar / añadir (`--after 10m` o `--at "2006-01-02 15:04"`) / eliminar |
| `panda detect [-o PATH]` | Escanea el hardware de esta máquina (CPU/RAM/GPU/CLIs de agente) y genera un borrador de capabilities.yaml |
| `panda card show \| rescan \| edit \| set` | Tarjeta de capacidades de este nodo: mostrarla (con el archivo de origen), volver a escanear el hardware y los CLIs de agente instalados (`rescan` imprime el diff, `--write` lo aplica y conserva un `.bak`), abrirla en `$EDITOR`, o `set <campo>=<valor>` sin editor. Los campos de hardware detectados se sobrescriben; las decisiones escritas a mano (nombre del nodo, resource_class, max_concurrent_tasks, tier de cada agente, habilidades native/manual) se conservan |
| `panda metrics [--csv]` | Exportar métricas de delegación |
| `panda audit [--task <id>]` | Verificar la cadena `prev_hash` del registro de auditoría o de los eventos de una tarea |
| `panda version` | Imprimir la versión |

## Configuración

| Sección | Clave | Significado |
|---|---|---|
| `node` | `name` | ID de nodo único (usado en toda la red) |
| `node` | `resource_class` | `Micro` \| `Standard` \| `Full` → nivel del scheduler |
| `network` | `listen_addr` | Dirección del listener WebSocket |
| `network` | `shared_secret` | Secreto HMAC que autentica los hellos entre nodos; el listener WS no arranca sin él (todos los nodos comparten un valor) |
| `network` | `max_connections` | Límite global de conexiones WS concurrentes (0 = ilimitado) |
| `network` | `max_connections_per_ip` | Límite de conexiones WS concurrentes por IP remota (0 = ilimitado) |
| `network` | `panel_addr` | Dirección HTTP de la consola web (`panda web` / `/web`); por defecto `127.0.0.1:7840` |
| `network` | `panel_token` | Token Bearer que protege `/api/*` de la consola (loopback genera uno efímero; prefiere `OPENPANDA_PANEL_TOKEN`) |
| `network` | `peers` | Direcciones de pares manuales a las que conectarse |
| `storage` | `db_path` | Ruta de la base de datos SQLite |
| `storage` | `context_path` | Almacén de snapshots de contexto |
| `storage` | `memory_path` | Raíz de la memoria personal |
| `storage` | `projects_path` | Raíz de la memoria por proyecto |
| `storage` | `skills_path` | Raíz de la memoria procedimental |
| `storage` | `work_path` | Dónde ejecutan los agentes; aquí se mide la desviación de alcance |
| `log` | `level` | `debug` \| `info` \| `warn` \| `error` |
| `model` | `base_url` | URL base de la API Messages compatible con Anthropic |
| `model` | `model` | ID del modelo (p. ej. `deepseek-chat`, `deepseek-reasoner`) |
| `model` | `api_key` | Secreto — prefiere `OPENPANDA_MODEL_API_KEY` |
| `model` | `api_type` | `anthropic` \| `openai` (por defecto `anthropic`) |
| `model` | `max_tokens` | Límite de tokens de completado (por defecto 4096) |
| `mcp` | `command` | Línea de comandos del servidor MCP stdio (vacío = desactivado); las herramientas se cargan en caliente en el conjunto del agente |
| `push` | `enabled` | Servir `/api/push/*` y enviar Web Push (consola embebida + sidecar webui) |
| `push` | `vapid_subject` | Sujeto VAPID (p. ej. una dirección `mailto:`) |
| `push` | `vapid_key_path` | Ruta de la clave VAPID (generada automáticamente en el primer arranque) |

Orden de carga de la configuración: bandera `--config` > variable de entorno >
valor por defecto `/etc/openpanda/config.yaml`.

## Documentación

La documentación completa vive en el directorio [`docs/`](docs/):

- [Índice de documentación](docs/README.md) — punto de entrada de los documentos públicos.
- [Guía de contribución](CONTRIBUTING.md) — herramienta, gates de ingeniería,
  convenciones de código y lista de verificación para PR
  (traducciones: `CONTRIBUTING.es.md` / `CONTRIBUTING.zh-CN.md` / `CONTRIBUTING.ja.md` / `CONTRIBUTING.de.md`).
- [Hoja de ruta de escritorio y empaquetado](docs/plans/roadmap-desktop-and-packaging.md) —
  el plan por etapas para cliente nativo de escritorio, instaladores firmados,
  notarización y actualizaciones automáticas.

## Pruebas

```bash
make test        # suite completa
make vet         # go vet
```

Las invariantes clave del protocolo están cubiertas por pruebas reales de
WebSocket entre dos nodos (sin necesidad de Tailscale):

```bash
go test ./internal/core/ -run 'TestTwoNodeProtocol|TestDelegateIdempotent|TestCancelPropagates' -v
```

## Despliegue

### Base de seguridad de red

Los nodos de OpenPanda hablan WebSocket sin cifrar (`ws://`) por defecto. **WebSocket
sin cifrar solo debe usarse sobre una ruta privada de confianza:**

- Enlaces de loopback / mismo host (p. ej. `127.0.0.1`, `localhost`).
- Una red overlay privada que controles, como **Tailscale** o una VPN.
- Una LAN físicamente aislada donde todos los dispositivos son de confianza.

**Si algún peer de OpenPanda cruza Internet público, termina TLS delante del
listener WebSocket** (p. ej. nginx, Caddy, Traefik) y configura los peers con la
URL `wss://`. El `shared_secret` autentica los saludos entre nodos, pero *no*
sustituye al cifrado de transporte — no expongas un listener `ws://` sin cifrar
en Internet público.

La consola web `panel_addr` sirve HTTP plano y lleva un token Bearer (uno efímero
se genera automáticamente en loopback). Mantenla en loopback o ponla detrás del
mismo reverse proxy TLS.

### Huella de memoria

OpenPanda apunta a dispositivos de bajo consumo. Verifica la memoria en estado
estable antes de desplegar en hardware — una sola muestra de `ps` no es fiable
debido al ruido del GC; toma varias:

```bash
make build
for i in 1 2 3 4 5; do
  ./bin/panda daemon --config testdata/node-a.yaml >/dev/null 2>&1 &
  PID=$!; sleep 3
  ps -o rss= -p $PID | awk '{printf "%d MB\n", $1/1024}'
  kill -TERM $PID; wait $PID 2>/dev/null
done
```

## Stack tecnológico

| Capa | Elección |
|---|---|
| Daemon principal | Go (modernc.org/sqlite — Go puro, sin CGO) |
| Pegamento / adaptadores | Python 3.10+ |
| Transporte | WebSocket + envoltorios JSON |
| Estado | SQLite en modo WAL |
| Frontend | Consola web (Vite + Preact, `go:embed` en un solo binario) |
| Acceso a LLM | Endpoints compatibles con Anthropic `/v1/messages` u OpenAI (p. ej. DeepSeek) |

## Hoja de ruta

Las fases 0–3 (modelo de entrada · delegación P2P · memoria+voz+endurecimiento
de la ejecución · reconstrucción de kernel/consola/REPL + verificación real
de dos nodos) están completas. La fase 4 (cliente de escritorio + pipeline
de instaladores firmados + mecanismo de actualizaciones automáticas + canales
de lanzamiento) está detallada en la [hoja de ruta de escritorio y empaquetado](docs/plans/roadmap-desktop-and-packaging.md).

## Contribuciones

Las contribuciones son bienvenidas. Para mantener la coherencia del código,
cumple estos gates de ingeniería antes de abrir un pull request:

- `make vet && make test` deben pasar.
- `gofmt -l internal/ cmd/ adapters/` debe estar vacío.
- Mantén la cobertura de pruebas de los módulos centrales por encima de ~60 %
  cuando sea posible.

Consulta la [guía de contribución](CONTRIBUTING.md) para las convenciones
completas: envoltura de errores (`%w` / `errors.Is`), límites de complejidad,
nada de código muerto, reglas de concurrencia, reglas de i18n y estilo de commits.
Traducciones: [`CONTRIBUTING.es.md`](CONTRIBUTING.es.md)、[`CONTRIBUTING.zh-CN.md`](CONTRIBUTING.zh-CN.md)、[`CONTRIBUTING.ja.md`](CONTRIBUTING.ja.md)、[`CONTRIBUTING.de.md`](CONTRIBUTING.de.md)。

## Licencia

Publicado bajo la [Licencia MIT](LICENSE).

## Agradecimientos

Inspirado por la teoría de planificación multiagente distribuida y por
los patrones de memoria de Hermes y OpenClaw. Construido por Xenith.
