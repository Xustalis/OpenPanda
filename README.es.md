# 🐼 OpenPanda

**Sistema Operativo de Agentes Personales de Código Abierto y Orquestador Multidispositivo Local-First**

> Conecta todos tus dispositivos en una malla peer-to-peer privada y "contrata" agentes de IA de terminal (Claude Code, Codex, Grok, etc.) para colaborar entre máquinas como un equipo unificado.

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![Release](https://img.shields.io/github/v/release/Xustalis/OpenPanda?label=release&color=blue)](https://github.com/Xustalis/OpenPanda/releases)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.26-00ADD8)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-3776AB)
![Platforms](https://img.shields.io/badge/plataformas-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)
![Memory](https://img.shields.io/badge/Memoria%20RSS-~20MB-brightgreen)
![Local First](https://img.shields.io/badge/Dependencia%20Cloud-Cero%20(Local--First)-success)

---

## ⚡ ¿Por qué OpenPanda?

Los asistentes de codificación de IA actuales (**Claude Code, OpenAI Codex, Grok Build, OpenCode**) son increíblemente potentes, pero están **atrapados dentro de un único terminal en una única máquina**.

En la práctica, tu flujo de trabajo diario abarca máquinas heterogéneas:
- Un **portátil ligero** donde escribes ideas y revisas cambios.
- Una **estación de trabajo o servidor Linux** con GPU y CPU potentes para compilaciones pesadas, Docker y entrenamiento.
- Una **Raspberry Pi o SBC** que maneja demonios 24/7 y hardware IoT.

**OpenPanda es el orquestador que faltaba.** No reemplaza tus agentes CLI favoritos: los **contrata**:

```
┌─────────────────────────────────────────────────────────────┐
│                      Tú: Una sola orden                     │
│               (TUI Terminal / Consola Web / Voz)            │
└──────────────────────────────┬──────────────────────────────┘
                               │
                  ┌────────────▼────────────┐
                  │     🐼 OpenPanda OS     │
                  │   Enrutar, orquestar,   │
                  │   verificar y asegurar  │
                  └────────────┬────────────┘
                               │ Enlaces P2P directos (WS + UDP sellado, sin nube)
     ┌─────────────────────────┼─────────────────────────┐
     │                         │                         │
┌────▼──────────────┐   ┌──────▼────────────┐   ┌────────▼────────────┐
│  MacBook (Worker) │   │  Servidor Linux   │   │  Raspberry Pi / SBC │
│  - Pruebas rápidas│   │  - Builds pesados │   │  - GPIO / Sensores  │
│  - Claude Code    │   │  - Codex / Docker │   │  - Demonios 24/7    │
└───────────────────┘   └───────────────────┘   └─────────────────────┘
```

Das una instrucción desde **cualquier** dispositivo. OpenPanda analiza la tarea, la delega a la máquina con los recursos y herramientas adecuados, supervisa la ejecución del agente, valida los resultados y te devuelve el resultado en tiempo real.

---

## 🌟 ¿Qué puede hacer OpenPanda?

### 1. 🌐 Colaboración multidispositivo P2P heterogénea
- **Descubrimiento LAN + emparejamiento estilo Bluetooth**: Los nodos se encuentran en la LAN con admisión confirmada por huella; `panda pair` incorpora un dispositivo con un código corto confirmado en ambos extremos, y `panda nodes verify` fija la clave Ed25519 de cada par.
- **Tarjetas de capacidad dinámicas**: Cada nodo declara su perfil de hardware (CPU, GPU, RAM, SO) y herramientas disponibles.
- **Enrutamiento inteligente de tareas**: Asigna compilaciones pesadas a servidores potentes y tareas de sensores a placas de bajo consumo según capacidad medida; `panda nodes drain` retira un nodo para mantenimiento.
- **Delegación con worktree**: Una tarea de archivos delegada a otro nodo lleva su checkout allí y trae el resultado de vuelta.
- **Malla multisalto**: Los pines estrictos (`--preferred`, `node:` de etapa) y el plano de datos de contexto/artefactos enrutan a través de varios relevos en una malla parcial: un nodo a dos saltos se direcciona mediante el estado de enlaces difundido, y un ejecutor que no alcanza el origen obtiene su snapshot e insumos por el salto que le entregó la tarea.
- **Despacho de actuadores**: Las tareas pueden accionar actuadores físicos en el nodo que posee el hardware; `drivers/` incluye siete drivers de referencia (servo, micrófono, cámara, notificación, MCU serie, captura de pantalla, entrada GUI), con variantes `.ps1` para el trío de escritorio, así que los nodos Windows de serie no necesitan git-bash.
- **Par computer-use**: Un nodo con sesión de escritorio anuncia `hardware:screen_capture` (capturar la pantalla) y `hardware:gui_input` (clic, teclear, tecla, scroll — tier 2 porque un clic no se puede deshacer), así el agente de esa máquina ve y conduce su GUI; los demonios sin cabeza nunca presumen una pantalla que no pueden ver. El contenido de pantalla solo viaja entre nodos de confianza mesh.
- **Artefactos recuperables**: Lo que una tarea produce (una captura, un informe) se empaqueta y regresa a ti: `panda task artifact <id>` lo lista y extrae, y la consola previsualiza imágenes y texto en línea.
- **Malla P2P privada**: Comunicación directa por WebSocket autenticado (cifrado AEAD por conexión negociado en el hello firmado) y plano de datos UDP cifrado con atravieso NAT. Tu código y memoria nunca salen de tus dispositivos.
- **Zonas de confianza**: Cada par lleva un nivel fijado por el operador (`panda nodes trust <id> mesh|guarded|open`) que limita lo que una tarea delegada puede exponerle y cómo se admite el trabajo entrante desde él; el contenido de pantalla es exclusivo de mesh de extremo a extremo — degradar la confianza corta una transferencia de pantalla en pleno vuelo.

### 2. 🤖 Orquestación universal de agentes y failover automático
- **Adaptadores listos para usar**: Diez adaptadores incluidos (Claude Code, OpenAI Codex, OpenCode, Grok Build, DeepSeek Harness, Antigravity, Hermes, Pi, ZCode, OpenClaw), un puente generic para todo lo demás y comandos de shell.
- **Registro multimodelo y failover transparente**: `panda model add|switch|test` gestiona varios proveedores por nodo tras un cortacircuitos de salud; si un agente agota su cuota o fallan sus credenciales (401/403), se inyecta automáticamente un modelo alternativo y la tarea continúa.
- **Ejecución multiharness**: `panda ask --agents a,b --mode parallel` reparte una instrucción a varios agentes como un plan y recoge los veredictos.
- **Trazabilidad total en tiempo real**: Visualiza comandos bash, ediciones de archivos y llamadas a herramientas en tu terminal o navegador.
- **Tablón de mensajes entre hermanos** (experimental): Las tareas que comparten un plan o un padre intercambian notas breves (una línea de estado, un hallazgo, un puntero a archivos producidos) en un tablón de ranuras acotado; cada agente lee lo publicado por sus pares en su siguiente ronda en lugar de bloquearse esperando sus resultados, y un presupuesto por episodio evita que el canal se vuelva ruido.

### 3. 🛡️ Seguridad autónoma y aprobación humana (Human-in-the-Loop)
- **Evaluación de riesgos por niveles**: Las tareas reversibles (leer código, compilar, ejecutar tests) se completan de forma autónoma.
- **Puertas de aprobación interactivas**: Las acciones irreversibles (`git push`, modificar bases de datos, borrar archivos) se pausan para tu confirmación explícita.
- **Disyuntores y prevención de bucles**: Detección activa de ciclos infinitos para evitar el consumo innecesario de tokens.

### 4. 🧠 Memoria de doble capa y habilidades evolutivas
- **Aislamiento estricto**: Las preferencias personales (`USER.md`) están separadas del contexto del proyecto (`MEMORY.md`).
- **Habilidades en automejora**: Crea y refina guías de procedimientos `SKILL.md` que se vuelven más inteligentes con el uso.
- **Skills Hub y descubrimiento autónomo**: Un catálogo curado sin conexión (`panda skill hub`), importación desde ruta, URL o archivo (`panda skill import`), y un asistente que encuentra e instala la habilidad que una tarea necesita mientras se ejecuta.
- **Delegación contextual**: Al transferir una tarea entre máquinas, la memoria del proyecto viaja con ella.

### 5. 🖥️ Interfaces unificadas e incrustación
- **TUI interactiva de terminal**: Construida con Bubble Tea, con modo de pantalla alternativa a pantalla completa, navegación por teclado, asistente de primer arranque, progreso en vivo y redirección en marcha; `PANDA_CLASSIC_REPL=1` conserva el REPL de línea clásico.
- **Consola Web integrada**: Tablero Kanban, streaming en tiempo real vía SSE, gestión de habilidades, cancelación de sesiones y login automático; `panda web --lan` la comparte en la LAN tras un token efímero, con notificaciones Web Push.
- **Entrada por voz** (experimental): `panda voice` ejecuta el bucle despertar → transcribir → despachar → hablar a través de los scripts sidecar incluidos en `extensions/voice` (requiere sus propias dependencias Python).
- **CLI para scripts**: Comandos rápidos como `panda ask` para integrar en scripts y pipelines.
- **Autoactualización**: `panda update check|apply` (o `panda upgrade`) prueba un binario preparado y lo intercambia en el sitio.
- **Árboles de sesión con bifurcación**: `panda session fork <id> --at N` bifurca la conversación en cualquier turno; en un repositorio, el worktree hijo se ramifica desde la rama del padre y hereda el código producido. `panda session tree` muestra la familia; `/fork` hace lo mismo en REPL/TUI.
- **Compactación automática**: El historial desbordado se pliega en un resumen corriente escrito por el modelo en vez de descartarse; el hilo almacenado permanece íntegro.
- **`panda mcp`**: Expone las herramientas propias del nodo (tareas, memoria, habilidades…) como un servidor MCP sobre stdio, de modo que cualquier agente que hable MCP puede manejar el nodo por el protocolo estándar.
- **`panda rpc`** (experimental): Superficie de incrustación NDJSON sobre stdio (`status`, `ask` en streaming, `session.*`); `scripts/panda_rpc.py` es un cliente de referencia solo-stdlib.
- **OAuth de suscripción** (experimental): `panda auth login anthropic` inicia sesión en Claude Pro/Max vía PKCE con refresco transparente del token.

### 6. 🪶 Ultraligero (~20MB de memoria)
- Binario estático único en Go puro (SQLite en modo WAL).
- Funciona sin esfuerzo en una SBC de 20 \$ (Raspberry Pi) o en servidores potentes.

---

## 🚀 Inicio rápido (3 minutos)

### Paso 1: Instalación

**macOS / Linux:**
```bash
curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh
```

**macOS (Homebrew):**
```bash
brew install Xustalis/openpanda/openpanda
```

**Windows (PowerShell):**
```powershell
irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
```

**Desde el código fuente** (Go ≥ 1.26, sin CGO — compila cruzado limpio):
```bash
git clone https://github.com/Xustalis/OpenPanda.git && cd OpenPanda
make build && ./bin/panda init
```

¿Ya lo tienes instalado? `panda update` lo actualiza en el sitio; tus datos se conservan.

### Paso 2: Inicializar el nodo

```bash
panda init
```
*El asistente interactivo configura el nombre del nodo, proveedores de modelos (DeepSeek, Claude, OpenAI, Ollama, etc.) y genera la tarjeta de capacidades.*

### Paso 3: Usar OpenPanda

- **Iniciar la TUI de terminal:**
  ```bash
  panda
  ```
- **Iniciar la consola web (abre el navegador automáticamente):**
  ```bash
  panda web
  ```
- **O hacer una consulta directa:**
  ```bash
  panda ask "Revisar el estado del sistema y resumir tareas pendientes"
  ```

### Conectar un segundo dispositivo en 30 segundos

1. En el dispositivo A: ejecuta `panda pair <dirección-dispositivo-B>` para iniciar el emparejamiento y mostrar el código.
2. En el dispositivo B: ejecuta `panda pair confirm` para verificar el mismo código y completar el alta.
*¡Ambos dispositivos quedan conectados en tu malla P2P!*

---

## 🛠️ Referencia de comandos

| Comando | Descripción |
|---|---|
| `panda` | Iniciar la TUI interactiva completa de Bubble Tea |
| `panda ask "<consulta>"` | Ejecución directa: responder, ejecutar herramienta o delegar (`--agents a,b --mode parallel` para una corrida multiagente) |
| `panda web` | Iniciar la consola web integrada con inicio de sesión automático (`--lan` para compartirla) |
| `panda voice` | Hablar con el nodo en lugar de teclear (experimental) |
| `panda model` | Registro multimodelo: `status`, `add`, `switch`, `test` |
| `panda nodes` | Listar dispositivos conectados; `nodes trust <id> mesh\|guarded\|open` fija el nivel de un par |
| `panda pair` | Emparejar un dispositivo nuevo por LAN (código corto confirmado en ambos extremos) |
| `panda queue` | Ver tareas pendientes, en ejecución y en revisión (`--watch` para actualización en vivo) |
| `panda approve <id>` | Aprobar una acción irreversible de nivel 2 |
| `panda plan run <archivo.yaml>` | Pipelines multietapa y multidispositivo (`plan example` genera un ejemplo) |
| `panda task board <id>` | Ver los tablones de mensajes entre hermanos que una tarea lee (ámbito plan/padre y sus hijos) |
| `panda task artifact <id>` | Listar o extraer el artefacto de salida de una tarea (`--list`, `--file <ruta>`, `--out <dir>`) |
| `panda session` | Listar, bifurcar y reanudar sesiones (`session tree` muestra la familia) |
| `panda project list` | Gestionar proyectos y contexto del espacio de trabajo |
| `panda card` | Ver, reescanear y editar la tarjeta de capacidades de este nodo |
| `panda skill` | Explorar, importar e instalar habilidades de flujo de trabajo (Hub, URL o archivo) |
| `panda auth login` | Iniciar sesión OAuth en una suscripción de modelo (p. ej. `anthropic`) |
| `panda mcp` · `panda rpc` | Servir las herramientas del nodo por MCP stdio · superficie de incrustación NDJSON |
| `panda update` | Buscar actualizaciones o autoactualizar en el sitio (`upgrade` aplica directamente) |
| `panda doctor` | Diagnóstico de PATH, configuración, adaptadores y base de datos |
| `panda version` | Mostrar la versión del binario |

---

## 🗺️ Hoja de ruta

| Versión | Tema |
|---|---|
| **v0.0.8** (base estable) | Orquestación multiagente en una sola máquina, plenamente utilizable: clasificación de intención, despacho, bucle de supervisión, failover, aprobación por niveles, política de idioma en prompts |
| **v0.0.9** (estable) — "Periapsis" | Arquitectura de transporte híbrido/DTN completada: enrutado mesh ponderado por latencia, bundles DTN sobre el cable, presupuestos de tokens, copias sombra, ruta de actuadores, plano de datagramas UDP con hole punching NAT coordinado por la malla, payloads cifrados, planes de contacto, identidad de nodo Ed25519, compilación lite — más aprobaciones recordadas, atribución de ejecución, un chip de nodo en la barra lateral web y pulido de la CLI cotidiana |
| **v0.0.10** — "Apoapsis" | Hacia fuera, a la LAN y al borde: descubrimiento con admisión confirmada por huella y pinning TOFU de claves, tareas de archivo delegadas que viajan con su worktree, el bucle de clarificación, despacho de actuadores con cinco drivers de referencia (MCU serie incluido), el adaptador Pi y un ejecutor generic sin Python — más árboles de sesión con bifurcación, compactación automática, la superficie de incrustación `panda rpc`, OAuth de suscripción, cadenas de eventos/auditoría firmadas, sandbox a nivel de SO y una ronda de rendimiento en régimen. Relicenciado a AGPL-3.0-or-later con licencia comercial dual |
| **v0.0.11** — "Rendezvous" | Apretando la propia malla: cifrado AEAD por conexión negociado dentro del hello firmado, `panda pair` estilo Bluetooth, balizas de descubrimiento firmadas e identidades `k:<pub>` estables, fijación de nodos estricta que conserva el destino a través de rutas multisalto, custodia outbox que reenvía veredictos tras cortes de enlace, arbitraje de autoridad del daemon, watchdog de silencio de 240 s, tramas binarias de datos negociadas — y un plano de datos de malla parcial que retransmite contexto y artefactos a lo largo de la cadena de delegación. Este corte además trae el par computer-use (`hardware:screen_capture`, `hardware:gui_input`; drivers POSIX y `.ps1` de Windows, anunciados solo bajo una sesión GUI real), artefactos de tarea recuperables (`panda task artifact`, vista previa en la consola), y una regla de confianza exclusiva de mesh para el contenido de pantalla que sigue los bytes por envíos, pulls y entregas de entrada entre etapas |
| **v0.0.12** (preliminar) — "Constellation" | La flota dibujada como un solo cielo y los agentes volando en formación: las tareas del mismo proyecto corren en paralelo mediante ámbitos de ruta declarados y worktrees aislados bajo límites de delegación de punta a punta; una tarea en marcha acepta una nota encolada o un steer en vivo, y una pausa por presupuesto de rondas puede continuarse en vez de solo aceptarse o rechazarse. La consola web se consolida en un único lugar de trabajo — cola, planes y proyectos se funden en la página Control, una página de espacio de trabajo dibuja toda la flota como mapa de topología en vivo y despacha turnos directamente al hilo elegido, y una página de perfil responde quién es este nodo y cuánto ha trabajado. Debajo: leases de ejecución entre procesos, recuperación de desborde de contexto, política de tokens por modelo sobre un catálogo actualizado a la generación vigente, autocompletado para PowerShell, pprof en el daemon — y un cerco de ciclo de vida por intento de dispensación que reenruta la custodia vencida, ordena la entrega del outbox tras cortes de enlace y evita que veredictos obsoletos caigan sobre intentos rotados |
| **v0.1.1** (actual) — "Constellation" | El parche que cierra las costuras de la línea de calidad de lanzamiento: el cliente de entrada habla OpenAI Responses y Google Gemini de forma nativa, OpenCode Go / Console se une a los proveedores integrados, la plantilla de un hilo y su fijación de nodo llegan al terminal junto a la consola (`/team`, `/node`, `panda ask --team/--node`, el tablero de pendientes), el presupuesto de despacho sigue al equipo que elegiste en vez de un 3 fijo, las escrituras entre procesos son atómicas tras un bloqueo de archivo, las sondas de adaptador dejan de repetirse y el cortacircuitos de modelos escala, y una tarea puede fijar su propio directorio de trabajo. En seguridad, un proyecto ya no puede convertir una petición autenticada en un visor de todo el disco, `sandbox.mode: strict` se niega a ejecutar sin su backend, y la consola limita los cuerpos de petición, los flujos de eventos y las sondas de agentes. La configuración y los datos guardados no cambian — sin migración de esquema, sin claves renombradas — y el protocolo no añade fotogramas; el único punto incompatible es el plano de datagramas UDP frente a un nodo 0.1.0: actualiza la malla entera. |
| **v0.1.0** (estable anterior) — "Constellation" | El primer corte estable de la línea de calidad de lanzamiento y el primero con instaladores reforzados (protección contra degradaciones en todas las rutas, firma del release verificada): el conjunto Constellation con las correcciones de corrección y seguridad de la auditoría de lanzamiento — extracción de artefactos multi-salto a través de relés, los fallos que el modelo declara ya no se registran como completados, el trabajo rechazado vuelve a un origen capaz, fijación de identidad en el hello, admisión acotada de paquetes de proyecto, corrección del plano UDP, denegación de lectura de secretos en modo strict, y las API de hilos/skills de la consola independiente. Verificado en una cadena Mac → relé → nube por WAN, incluido un árbol de proyecto de 3 MiB extraído de extremo a extremo — y desde el primer corte, dictado en la consola (`POST /api/voice/transcribe`), un modelo fijado por agente (`injection.agents`) y paneles de dispositivos en el espacio de trabajo. |
| **v0.0.x** — el núcleo | **Pulir el núcleo hasta que cada función entregada quede probada.** Las pistas Experimental maduran a estable mediante validación en flota real — descubrimiento LAN con admisión por huella, emparejamiento + AEAD por sesión, zonas de confianza, el tablero de mensajes entre tareas, árboles de sesión/RPC/OAuth, delegación con worktree viajero, el bucle de clarificación, drivers de actuadores, computer-use y la entrada de voz — más el primer encendido en hardware real de los drivers de referencia; estabilidad, rendimiento y ajuste de casos límite durante todo el ciclo |
| **v0.1.x** — el asistente | **Especialización en un asistente que hace cosas por ti.** Una capa de escritorio delgada sobre el núcleo (residente en bandeja, atajo global, notificaciones nativas, autoactualización); la capa de malla densa — recomposición dinámica de planes sub-main scout→plan, relevo tipo TURN para NAT simétrico, deudas del tablero (e2e multidispositivo, pull por MCP, vista del tablero); y una «presencia» mínima y honesta — un motor de colisiones sobre la lista de lo no resuelto que cabalga el ciclo dream y solo habla cuando merece la pena. Calidad comercial, límites declarados con honestidad |
| **v0.2.x** — autoevolución | **OpenPanda itera sobre sí mismo.** La automodificación llega como conjuntos de parches producidos por agentes a través de un pipeline build → gate → merge humano que coexiste con el canal de releases firmado; los self-builds firmados por el nodo se vuelven una tercera clase de procedencia bajo las zonas de confianza; `self_evolution.mode` (off → propose → auto) conservando siempre el último release firmado como respaldo — el paradigma completo de autoevolución de agentes |

---

## 🔭 Visión

La arquitectura de OpenPanda está diseñada para clústeres heterogéneos a gran escala: control de enjambres de drones, planificación de comunicaciones para constelaciones de satélites en el espacio profundo y flotas coordinadas de vehículos autónomos. Estos escenarios comparten un problema fundamental: **cada nodo tiene distinta capacidad de cómputo, distintas capacidades y distintas tareas, y aun así todos deben coordinarse, planificarse y supervisarse en conjunto.**

Hoy OpenPanda sirve a los dispositivos y agentes de un desarrollador. El objetivo a largo plazo es extender el mismo núcleo de orquestación a nodos autónomos a escala de clúster.

---

## 📚 Documentación

La documentación vive en [`docs/`](docs/README.md): [estado del proyecto](docs/status.md) (qué está verificado y qué solo compilado), la [guía de instalación](docs/install.md), el [FAQ](docs/faq.md), la referencia del [protocolo del bus](docs/protocol.md), además de borradores de diseño e informes históricos. Ten en cuenta que la mayoría de los documentos internos están en chino.

---

## 🤝 Contribuir

¡Agradecemos las contribuciones de la comunidad! Consulta [CONTRIBUTING.es.md](CONTRIBUTING.es.md), [SECURITY.es.md](SECURITY.es.md) (el texto canónico es el [SECURITY.md](SECURITY.md) en chino) y [CODE_OF_CONDUCT.es.md](CODE_OF_CONDUCT.es.md) antes de enviar un Pull Request, y ejecuta `make gate` en local. Las contribuciones no triviales requieren asentir al [CLA](CLA.es.md); las correcciones menores y los cambios en `adapters/` están exentos.

---

## 📄 Licencia

OpenPanda se distribuye bajo doble licencia:

- **Comunitaria** — [GNU Affero General Public License v3.0 o posterior](LICENSE) (AGPL-3.0-or-later)
- **Comercial** — licencia propietaria para integración en código cerrado u oferta alojada; consulta [COMMERCIAL.es.md](COMMERCIAL.es.md)

Las versiones publicadas antes del cambio siguen disponibles bajo la licencia MIT.

El uso de OpenPanda está sujeto a los [Términos de uso y exención de responsabilidad](TERMS.es.md) ([en inglés](TERMS.md)).
