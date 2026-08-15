# 🐼 PANDA

**P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant

> Cualquier dispositivo, cualquier potencia de cálculo, un solo comando.
> Un asistente personal de orquestación de tareas que se ejecuta en tus
> dispositivos heterogéneos como una red entre pares de nodos.

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.22-blue)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-blue)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)

---

## Índice

- [¿Qué es PANDA?](#qué-es-panda)
- [Características principales](#características-principales)
- [Arquitectura](#arquitectura)
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

## ¿Qué es PANDA?

PANDA convierte cada dispositivo que tienes — un portátil, un ordenador de placa
única, un escritorio — en un *nodo* de tu red personal de tareas. Preguntas una
sola vez, desde cualquier dispositivo, y PANDA delega la tarea al nodo mejor
preparado para ejecutarla, devuelve el resultado y recuerda lo aprendido para la
próxima vez.

Está construido desde cero como un sistema **personal**: sin dependencia de la
nube, tu memoria permanece en tus dispositivos y cada nodo habla con sus pares a
través de enlaces WebSocket directos que tú controlas.

## Características principales

- **Red de nodos heterogéneos** — cada nodo publica sus capacidades reales
  (clase de CPU, shell, adaptadores de agente) mediante una tarjeta de
  capacidades; la red enruta cada tarea al nodo que realmente puede hacerla.
  Diseñado para MacBook ↔ Orange Pi 3B y todo lo que haya en medio.
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
- **Memoria de dos capas** — memoria por usuario y por proyecto (estilo `USER.md`
  / `MEMORY.md`) tras un muro de aislamiento, más un motor **Dreaming** en segundo
  plano que consolida los registros diarios en memoria a largo plazo mientras el
  nodo está inactivo.
- **Entrada por voz** — pipeline sidecar opcional (palabra de activación → STT →
  LLM → TTS), controlado por hardware y listo para micrófonos embebidos.
- **Panel de control PWA** — una consola web para la cola de tareas, los detalles
  de las tareas y las aprobaciones con intervención humana; instalable como
  Progressive Web App.
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
                        │  Tú: CLI / PWA / voz      │
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
          │   p. ej. MacBook (Full) │     │   p. ej. Orange Pi (Micro)│
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

## Primeros pasos

### Requisitos previos

| Herramienta | Versión |
|---|---|
| Go | 1.22+ (el módulo apunta a 1.26.5) |
| Python | 3.10+ (adaptadores de agente / sidecar de voz) |
| make | cualquier versión reciente |

### Compilar

```bash
make build          # binario nativo → bin/panda (release, sin símbolos)
make test           # ejecutar toda la suite de pruebas
make vet            # análisis estático
```

Cross-compile para los dispositivos que realmente usas:

```bash
make build-linux-arm64   # → bin/panda-linux-arm64  (p. ej. Orange Pi)
make build-linux-amd64   # → bin/panda-linux-amd64
make build-darwin-arm64  # → bin/panda-darwin-arm64
make build-windows-amd64 # → bin/panda-windows-amd64.exe
```

### Configuración

Copia la configuración de ejemplo y edítala para cada nodo:

```bash
cp config.example.yaml /etc/panda/config.yaml   # o déjala local y usa --config
```

La configuración es pequeña y autoexplicativa. Lo más importante:

```yaml
network:
  listen_addr: ":7836"        # listener WebSocket
  shared_secret: "..."        # autenticación HMAC entre nodos — todos comparten el mismo valor
  peers:                      # otros nodos de tu red
    - "orangepi3b.tailnet.ts.net:7836"
model:
  base_url: "https://api.deepseek.com/anthropic"  # cualquier endpoint compatible con /v1/messages
  model: "deepseek-chat"
  # api_key: ""               # prefiere la variable de entorno PANDA_MODEL_API_KEY
```

Los secretos (claves de API del modelo) se leen de `PANDA_MODEL_API_KEY` en lugar
del archivo de configuración siempre que sea posible.

### Ejecutar el daemon

```bash
./bin/panda --config config.yaml --card config/capabilities.macbook.yaml
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
| `panda` (sin argumentos) | Ejecutar el daemon: registrar nodo, heartbeat, servidor WS, reconexión de pares |
| `panda ask [--config PATH] [--card PATH] [--authorize] "<pregunta>"` | Entrada unificada: clasifica en answer / tool_call / task y ejecuta |
| `panda status` | Estado del nodo y de las tareas |
| `panda queue` | Listar la cola de tareas |
| `panda task [--config PATH] <task-id>` | Detalles de la tarea |
| `panda cancel [--config PATH] <task-id>` | Cancelar una tarea (en cascada al nodo ejecutor) |
| `panda approve [--config PATH] <task-id>` | Aprobar una tarea en revisión (review → done) |
| `panda reject [--config PATH] [--reason s] <task-id>` | Rechazar una tarea en revisión |
| `panda logs [--config PATH] <task-id>` | Registros de ejecución de la tarea |
| `panda skill` | Gestión del almacén de skills |
| `panda version` | Imprimir la versión |

## Configuración

| Sección | Clave | Significado |
|---|---|---|
| `node` | `name` | ID de nodo único (usado en toda la red) |
| `node` | `resource_class` | `Micro` \| `Standard` \| `Full` → nivel del scheduler |
| `network` | `listen_addr` | Dirección del listener WebSocket |
| `network` | `shared_secret` | Secreto HMAC que autentica los hellos entre nodos; el listener WS no arranca sin él (todos los nodos comparten un valor) |
| `network` | `panel_addr` | Dirección HTTP del panel PWA (vacío = desactivado) |
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
| `model` | `api_key` | Secreto — prefiere `PANDA_MODEL_API_KEY` |

Orden de carga de la configuración: bandera `--config` > variable de entorno >
valor por defecto `/etc/panda/config.yaml`.

## Documentación

La documentación completa vive en el directorio [`docs/`](docs/), dividida en
partes públicas e internas:

- [Índice de documentación](docs/README.md) — punto de entrada de todos los
  documentos.
- [Manual de desarrollo](docs/guides/DEVELOPMENT.md) — quickstart, mapa de
  directorios, convenciones de ingeniería, gates de calidad e inventario de
  pruebas.
- [Informes de fase](docs/reports/) — informes de progreso de cada fase y sprint.

Los documentos internos de planificación, diseño y auditoría quedan fuera del
repositorio público.

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

PANDA apunta a dispositivos de bajo consumo. Verifica la memoria en estado
estable antes de desplegar en hardware — una sola muestra de `ps` no es fiable
debido al ruido del GC; toma varias:

```bash
make build
for i in 1 2 3 4 5; do
  ./bin/panda --config testdata/mac-config.yaml >/dev/null 2>&1 &
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
| Frontend | PWA (app web vanilla + service worker) |
| Acceso a LLM | Endpoint `/v1/messages` compatible con Anthropic (p. ej. DeepSeek) |

## Hoja de ruta

La fase 3 (memoria + voz + seguridad) está en curso. La capa de memoria, el motor
Dreaming, el sistema de skills, el panel PWA y el endurecimiento de la ejecución
están implementados; la entrada por voz está completa en código y a la espera de
validación con hardware de micrófono.

## Contribuciones

Las contribuciones son bienvenidas. Para mantener la coherencia del código,
cumple estos gates de ingeniería antes de abrir un pull request:

- `make vet && make test` deben pasar.
- `gofmt -l internal/ cmd/ adapters/` debe estar vacío.
- Mantén la cobertura de pruebas de los módulos centrales por encima de ~60 %
  cuando sea posible.

Consulta el [manual de desarrollo](docs/guides/DEVELOPMENT.md) para las
convenciones completas: envoltura de errores (`%w` / `errors.Is`), límites de
complejidad, nada de código muerto y reglas de concurrencia.

## Licencia

Publicado bajo la [Licencia MIT](LICENSE).

## Agradecimientos

Inspirado por la teoría de planificación multiagente distribuida (ATC-MARL) y por
los patrones de memoria de Hermes y OpenClaw. Construido por Xenith.
