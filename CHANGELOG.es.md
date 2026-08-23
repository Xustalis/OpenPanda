# Registro de cambios

[English](CHANGELOG.md) · [简体中文](CHANGELOG.zh-CN.md) · [日本語](CHANGELOG.ja.md) · [Español](CHANGELOG.es.md) · [Deutsch](CHANGELOG.de.md)

## Acerca del proyecto

OpenPanda (**Open** **P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant) es un kernel de orquestación de tareas personal: un binario `panda` corre en cada uno de tus dispositivos, los nodos se descubren por un bus WebSocket autenticado, un modelo de entrada convierte cada petición en respuesta directa o en una especificación de tarea ejecutable, y el planificador enruta cada tarea al dispositivo y agente más adecuados. La CLI es la interfaz principal del kernel —`panda` a secas entra en un REPL interactivo— y la consola web es una cáscara fina sobre el mismo almacén y motor.

## Reglas de versiones

- Las versiones siguen `MAJOR.MINOR.PATCH`. El proyecto está en desarrollo inicial (`0.0.x`): un parche puede añadir funciones, corregir errores y —excepcionalmente— introducir cambios importantes, que siempre se listan bajo **Cambios importantes**.
- Un lanzamiento se corta etiquetando `vX.Y.Z`; cada commit desde la etiqueta anterior pertenece a la sección de la nueva versión. `[Unreleased]` recoge el trabajo desde la última etiqueta.
- Cada versión se documenta en cuatro categorías: **Añadido** (nuevas funciones), **Corregido** (correcciones), **Mejorado** (mejoras y refinamientos), **Cambios importantes** (requiere acción al actualizar).
- Cada entrada nombra el cambio y su efecto visible en una a tres líneas; se cita el commit que lo introdujo cuando ayuda a la arqueología.
- Este archivo en inglés es el canónico. Las traducciones zh-CN / ja / es / de lo replican y pueden retrasarse brevemente alrededor de un lanzamiento.

## [Unreleased]

## [0.0.2] - 2026-08-22

El lanzamiento centrado en la CLI: el rediseño del kernel (etapas A–C) aterriza —cada capacidad web gana su par en CLI—, la REPL se convierte en la puerta principal, y la CLI gana memoria de conversación, reporte de tareas en vivo y renderizado de Markdown según el destino.

### Añadido

- **Familias de comandos CLI** — cada capacidad web tiene su par en CLI: `panda session | task | memory | config | agents | project`, todas compartiendo la capa de servicios del panel; `panda ask` gana `--output-format json|stream-json` para uso sin terminal (a4cba5f).
- **Cola local consciente de recursos** — `core.Submit` pasa a asíncrona: orden por secuencia de arrastre → prioridad → FIFO, controlada por un registro de bloqueos de recursos más `MaxConcurrent`; las tareas con recursos disjuntos adelantan a una cola bloqueada; las tareas ganan `priority`/`seq`/`session_id`/`resource_keys` (SQLite v9) (0e8d850).
- **Memoria de conversación de la REPL** — presupuesto de 24k caracteres con expulsión alineada por pares (un turno del usuario nunca se reproduce sin su respuesta), persistida en `~/.local/state/openpanda/conversation.json`; `/new`, `/history`, `!!` y `panda ask --continue` (f0a1b9f).
- **Reporte de tareas fuera de banda** — un observador de la REPL imprime una línea ✓/✗ cuando una tarea alcanza estado terminal (tablero, consola web, delegaciones entre nodos) sin perturbar la línea de entrada; las preguntas en línea nunca se notifican dos veces (f0a1b9f).
- **Tablero de tareas en vivo** — `panda queue --watch` y `/tasks watch` redibujan la cola cada 2s con filas coloreadas por estado; Ctrl-C sale de la vista, no del proceso (f0a1b9f).
- **`internal/mdtext`** — renderizado de Markdown según el destino: énfasis ANSI en TTYs con color, texto plano para tuberías y consolas desnudas, siempre eliminado antes del TTS; los deltas en streaming se renderizan línea a línea con las mismas reglas (e94f72f).
- **Progreso en vivo del agente** — los adaptadores emiten notas de progreso NDJSON en stderr, registradas como eventos `EvProgress` con límite: `panda task <id>` y la línea de tiempo del panel muestran qué hace el agente mientras corre (93a453a).
- **Política de inyección** — `injection.model: auto|always|never`: las credenciales nativas del agente ganan por defecto; cada inyección se anuncia en la salida de la tarea y queda en el registro de auditoría (852b27e).
- **Enrutamiento con costo** — la selección de agente puntúa capacidad × cost_tier con bono de `preferred_agents`, con repliegue al siguiente mejor agente (852b27e).
- **Revisión de memoria** — topes configurables (`memory.limits`), topics multifichero con inyección selectiva estilo manifiesto, sedimentación de dream de bajo peso (852b27e).
- **`internal/hwinfo`** — paquete compartido de sondeo de hardware que respalda `panda detect` y el nuevo endpoint `GET /api/self` (852b27e, 1a97fd7).
- **Ajustes de app y topics de memoria en el panel** — `GET/PUT /api/settings/app` con almacenamiento de políticas validado; la API de memoria gana topics por fichero; la página de memoria se productiza y los ajustes se agrupan, i18n sincronizado en cinco idiomas (1a97fd7).
- **`panda init`** — arranque inicial interactivo que escribe `config.yaml` + `capabilities.yaml`; `config.ResolvePath` unifica la resolución (bandera > variable de entorno > config de usuario > valor del sistema) (f5610fc).
- **Pulido de la consola** — componentes compartidos `PageHeader`/`ErrorState`, toasts globales y diálogos de confirmación en acciones destructivas (45ee941).
- **Ergonomía de la REPL** — menú de comandos con barra bajo el prompt, banner figlet en ASCII puro, degradación a inglés/ASCII con TERM=linux, y autodescubrimiento de `--card` (`./capabilities.yaml` → `/etc/openpanda/capabilities.yaml`) (f0a1b9f).
- **`scripts/deploy-pi.sh`** — despliegue de Orange Pi con un comando: compilación cruzada, reemplazo atómico del binario, instalación systemd, chequeo de salud (d7bc87f).

### Corregido

- **Tiempo de espera del adaptador durante toda la ejecución** — un CLI colgado a mitad de flujo (tubería abierta, sin salida) bloqueaba para siempre el bucle de lectura; el timeout solo cubría la cola tras el EOF del stdout. Ambos adaptadores inician el CLI en su propio grupo de procesos con un hilo watchdog que mata todo el árbol al vencimiento (332f2d4).
- **Compatibilidad con la API de herramientas de Anthropic** — los bloques tool_use ahora siempre llevan `input` (objeto vacío para herramientas sin argumentos); los proveedores estrictos compatibles con Anthropic (DeepSeek /anthropic) antes rechazaban los turnos siguientes con un 400. Nombres con puntos renombrados a guiones bajos para satisfacer `^[a-zA-Z0-9_-]+$` (93a453a).
- **codex no podía inicializarse bajo un padre no interactivo** (EPERM escribiendo su BD de estado y alias de PATH antes del primer turno) — corre con `-s danger-full-access`, confinado por el sandbox externo de PANDA (332f2d4).
- **Las fallas del agente registraban una razón vacía** — el diagnóstico del adaptador ahora se refleja en Stderr, así `store.Fail` y los resultados llevan el error real (93a453a).
- **Tormenta de reconexión por marcado mutuo** — la respuesta hello final del perdedor de la deduplicación salía por la conexión del registro en lugar de la entrante, así nunca vinculaba la identidad del par y remarcaba cada segundo (869 reconexiones en 15 minutos en hardware real; ahora 1) (93a453a).
- **Falta de work_path** se manifestaba como un engañoso fork/exec ENOENT que culpaba al binario del comando — el demonio crea todas las raíces de almacenamiento al arrancar (f0a1b9f).
- **Banderas finales tragadas silenciosamente** dentro de los posicionales (`panda task <id> --config x` perdía la configuración) — todos los subcomandos ahora adelantan las banderas (f0a1b9f).
- **Bucle de autocompletado** — `/e` se pegaba a `/exit ` y el retroceso lo reactivaba (f0a1b9f).
- **La migración SQLite v9 crasheaba** en bases heredadas creadas antes de que existiera la tabla `tasks`; ahora la crea si falta (0e8d850).
- **Los errores de API llegan como guía, no como ruido de transporte** — 401/403 apuntan a `model.api_key`, 404 a `base_url`/nombre del modelo, 429/5xx persistentes nombran limitación de tasa, las fallas de conexión sugieren revisar la red (df47725).
- **Compuertas y endurecimiento** — `make measure` referenciaba una config inexistente; deriva de gofmt; versión de Go incorrecta en el README; `.gitignore` sin `.openpanda/`; el par fantasma de la config de ejemplo loopeaba avisos; el panel ganó middleware `securityHeaders` (cacde7b).

### Mejorado

- **Disciplina de respuesta** — el prompt de entrada exige respuestas que empiezan por la conclusión; los prompts de agente llevan un anexo de salida: el mensaje final reporta qué se hizo, el detalle queda en los eventos de `panda task <id>` (93a453a).
- **Resiliencia del streaming** — `streamWithRetry` repite caídas transitorias (429/5xx/red) con backoff mientras nada fue entregado; `deltaGuard` impide que el JSON de tarea fluya a burbujas y mantiene reiniciables las caídas a mitad de JSON; un bucle de herramientas agotado converge con una llamada final sin herramientas; la ejecución de herramientas fija la instantánea de registro del momento de clasificación, evitando «unknown tool» durante el hot-switch de MCP (df47725).
- **Navegación lateral agrupada** — secciones colapsables (Tareas / Dispositivos y agentes / Personal / Sistema) con el prompt recast como la persona «directora de orquesta» (f5610fc).
- **Pruebas de endpoints del panel** — diecisiete pruebas cierran los vacíos de mayor riesgo: CRUD de sesiones más un git real de extremo a extremo (tallado de worktree por HTTP, diff, merge), enmascaramiento de clave de modelo (el secreto crudo nunca sale), fallo de arranque de MCP como 400, ciclo de vida de skills, CRUD de recordatorios, endpoints de sistema (ad884bf).
- **Carga silenciosa de configuración en comandos interactivos** — las superficies interactivas silencian el ruido slog del cargador; el demonio conserva el registro completo (f0a1b9f).

### Cambios importantes

- **`panda` a secas ahora abre la REPL interactiva** en lugar del demonio sin interfaz; el kernel pasó a un subcomando explícito `panda daemon`. La unidad systemd, el LaunchAgent, los lanzadores de Windows y los objetivos del Makefile fueron actualizados — los despliegues que invoquen `panda` directamente deben cambiar a `panda daemon` (f0a1b9f).

## [0.0.1] - 2026-08-19

Pre-lanzamiento inicial de código abierto: el conjunto completo del kernel (demonio, CLI, delegación P2P, cadena de auditoría, migraciones, planificador, panel SSE, consola web embebida, REPL interactiva, ciclo de vida de instalación multiplataforma) más la capa asistente (sentidos del agente, recordatorios, MCP, chat en worktrees, tablero kanban). Compuertas en verde todo el trayecto: build / vet / pruebas completas / `-race` / compilación cruzada.

### Añadido

- **Cimientos del kernel** — máquina de estados de tareas con leases y recuperación de caídas, bus WebSocket autenticado, directorio de capacidades y el pipeline de ejecución local con el adaptador OpenCode (Sprint 0–1: 1be8f85..307e13a).
- **Delegación P2P** — enrutamiento de tareas entre nodos, transferencia por niveles de contexto, modelo de permisos por tiers (Tier 1 automático / Tier 2 aprobación), acceso GPIO y puntuación de planificación DCPS (3040e18, 6324a87).
- **Cadena de defensa** — detección de desviación de scope, detección de bucles de reintento y clasificación de comandos con tabla de comandos destructivos (590cacc, c647c96).
- **Memoria Hermes y skills** — notas diarias, dreaming con sedimentación, memoria de proyecto y skills cargables; `panda skill` gestiona las aprobaciones desde la CLI y la consola lleva una vista de skills (9a41b3e, c36cad1).
- **Sidecar de voz** — palabra de activación, STT, TTS y VAD (con puerta de hardware), con overrides `OPENPANDA_WAKE_KEYWORD` / `OPENPANDA_WAKE_MODEL` (84faf08).
- **Despliegue en hardware real** — tres nodos verificados en Mac / Windows / Orange Pi, enrutamiento por scope y la forma kernel sin interfaz (0aa9f73, 7f1f8bd).
- **Auditoría y migraciones** — cadenas de auditoría `prev_hash`, migraciones SQLite por PRAGMA `user_version`, protección contra slow-DoS, timeout duro del cliente MCP (7582754).
- **Mecanismos del planificador** — puntuación ponderada DCPS descontada por la frescura del heartbeat TMB (vida media de 30 minutos); accept/decline por capacidad; reenrutado automático ante rechazo excluyendo a los rechazadores históricos (f454909, 7385a89).
- **Comandos de panel CLI de una sola pasada** — `panda status`, `panda queue` y `panda task | cancel | approve | reject | logs` inspeccionan el nodo y gestionan tareas sin entrar en la REPL (307e13a).
- **REPL interactiva** — comandos con barra sobre cada superficie del panel (`/ask`, `/tasks`, `/approve`, `/nodes`, `/web`…), i18n en cinco idiomas, motor ask opcional (6119493).
- **Consola web embebida** — reconstruida en Vite + Preact + TypeScript y plegada al binario vía `go:embed`: vistas de cola/detalle/ask/proyectos/nodos, SSE en vivo, cinco idiomas de UI (61cc519, c9768c1).
- **Rutas de escritura del panel + SSE** — `POST /api/ask` vía el paquete compartido `askengine`, proyectos, nodos, cancelación, registros y el flujo de cambios `/api/events` (b4fb9f5).
- **`panda web`** — consola loopback de config cero con URL de auto-login de token efímero (47517e3).
- **`panda install` / `uninstall` / `doctor`** — ciclo de vida multiplataforma: registro persistente en PATH, autochequeo independiente, desinstalación segura por lista blanca con confirmación + respaldo zip (86b9b9d).
- **Tablero kanban** — formulario de creación, ciclo de prioridad, reordenado por arrastre por columna, aprobaciones en línea (da9c9e1).
- **Sesiones de chat en worktrees de git** — respuestas en streaming, cadena de pensamiento en vivo, pliegue de resumen exactamente una vez (c36cad1).
- **Integración MCP con hot-reload** — un servidor stdio, validado levantándolo realmente antes del intercambio; las herramientas se unen sin reinicio (c36cad1).
- **Recordatorios programados** — auto-programados por el agente vía la herramienta `reminder.set`; Web Push más cuentas regresivas SSE; CLI `panda reminder` (c36cad1).
- **Sentidos del agente** — herramientas de sistema `time.now` y `weather.get`: el modelo no tiene reloj ni ventana (c36cad1).
- **Adaptador codex + visibilidad** — sondeo de CLIs instalados con pruebas de conectividad; `panda detect` escanea hardware a un borrador de capabilities.yaml (c36cad1).
- **`panda metrics [--csv]` y `panda audit verify [--task <id>]`** — exportación de métricas de delegación y verificación de la cadena de auditoría (7582754).
- **`scripts/smoke-delegate`** — verificador de delegación entre procesos: exit 0 significa que una tarea de capacidad solo-de-par alcanzó done en un par.
- **Documentación open source** — READMEs en cinco idiomas, CONTRIBUTING con compuertas de fusión (`make gate`) y la hoja de ruta pública (51031eb).

### Corregido

- **Aleteo de conexión por marcado mutuo** — dos nodos marcándose mutuamente producían un ciclo interminable de ~1s conectar/desconectar; el desempate determinista en `ensurePeer` (gana el id de nodo lexicográficamente menor) deja exactamente una conexión TCP (879b42d).
- **Vacíos de autorización del protocolo de cable** — result/decline/accept/context-ack verifican que el remitente sea el ejecutor actual; guardas CAS cierran carreras TOCTOU; `waiting_context` siempre lleva lease; las fallas locales terminalizan sin dejar zombis (9622538).
- **Bypasses de clasificación de comandos** — valores `env -S` clasificados recursivamente, `php -r` escaneado, `find -exec` / `tar --checkpoint-action` / `git push/commit` fallan-cerrados a Tier 2 (f5db449).
- **Gestión de grupos de proceso** — muerte de todo el árbol al cancelar (Unix `Setpgid`, Windows `taskkill /T`) y timeout duro de adaptador de 630s (f5db449).
- **Canales de inyección de memoria** — escrituras atómicas para Hermes/Projects/skills, entrada externa marcada `[ext]`, memoria vallada en `<memory_data>` con preámbulo de datos-no-instrucciones (a742585).
- **Propagación de cancelación** — `task_cancel` se propaga saltando a saltos a los nodos en ejecución a lo largo de la cadena de delegación (574632a).
- **Escrituras transaccionales** — los UPDATE de estado de tarea y sus INSERT de auditoría se commit en una transacción (c5d34d4).
- **Barrido integral (D1–D32)** — huérfanos de delegación terminalizados, copias reenviadas con lease, HMAC del hello atado a una ventana de 5 minutos, NetworkGuard fijado a los endpoints configurados, captura de salida acotada (1694b7d).
- **Consola en pantalla blanca en clones frescos** — un `index.html` con hash restaurado por git apuntaba a assets ignorados; el placeholder commiteado es ahora estable y `make web` vigila que aterrice el build real (ab87f90).
- **Subcomandos desconocidos iniciaban un demonio residente** (`panda statsu`) — ahora hacen exit 2 con el uso (a742585).
- **Valores por defecto del wake de voz** — palabras clave integradas reales por backend (`hey_jarvis` / `porcupine`) (4ea73bf).
- **Correcciones de la auditoría del pre-lanzamiento** — `panda help` existe; residuos de marca «PANDA» eliminados de prompts y ejemplos; `config.example.yaml` documenta `mcp:` y `model.api_type`; enlace muerto de la hoja de ruta corregido (2f001c0).

### Mejorado

- **Muro duro de memoria** — la memoria personal jamás se inyecta en conversaciones de workspace (ancladas a worktree); la memoria de proyecto llega solo vía el ContextPack del nodo ejecutor (da9c9e1).
- **Los adaptadores de agente se unen al modelo de tiers** — los no declarados son Tier 2 por defecto y se rechazan antes de engendrar el subproceso (a4d2d9e).
- **Formato de cable OpenAI junto al de Anthropic** — el modelo de entrada habla ambos tipos de endpoints, con streaming en ambos (c36cad1).
- **Endurecimiento de ficheros secretos** — configs con `api_key` / `shared_secret` / `panel_token` se auto-chmodan a 0600 con guía de variables de entorno (6275fd4).
- **El panel usa loopback por defecto** (`127.0.0.1:7840`); los binds no-loopback advierten de HTTP plano (a742585).
- **La reconexión de pares reemplaza conexiones obsoletas** — una conexión nueva de la misma identidad se intercambia en el registro; la remoción coincide por identidad de conexión (7911bbe).
- **La clasificación `-c` de intérpretes es por lista blanca** — solo el código demostrablemente de salida pura queda en Tier 1 (f5db449).
- **Línea base de despliegue documentada** — `ws://` plano solo por loopback/Tailscale/LAN confiable; TLS + `wss://` en el Internet público (7582754).

### Cambios importantes

- **Proyecto renombrado a OpenPanda** — ruta de módulo `github.com/Xustalis/OpenPanda`, variables de entorno con prefijo `OPENPANDA_`, unidades `openpanda.service` / `com.openpanda.node.plist`, BD por defecto `openpanda.db`; el binario CLI conserva el nombre corto `panda` (ac71bb1, 6f2083e).

## Seguimientos aplazados

Aplazados deliberadamente para mantenerlos visibles:

- Atajos de teclado para la consola (nuevo chat, tarea rápida, cambio de vista).
- Superficie de navegador compañera para el asistente.
- Vistas git de primera clase en la consola (estado de ramas, historial, remotos).
- Gestión de worktrees desde la consola (listar/podar/inspeccionar).
- Personalidad y presentación del asistente ajustables por el usuario.
- Caché de búsquedas web para recortar fetches repetidos y latencia.
- Niveles de esfuerzo de razonamiento por tarea (bajo/medio/alto).
