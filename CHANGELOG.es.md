# Registro de cambios

[English](CHANGELOG.md) · [简体中文](CHANGELOG.zh-CN.md) · [日本語](CHANGELOG.ja.md) · [Español](CHANGELOG.es.md) · [Deutsch](CHANGELOG.de.md)

## Instalación con un comando

**macOS / Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh
```

**Windows (PowerShell)**

```powershell
irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
```

**macOS (Homebrew)**

```sh
brew install Xustalis/openpanda/openpanda
```

Tras instalar, ejecuta `panda init` para configurar el nodo, o simplemente escribe `panda` para entrar en el REPL. Una instalación antigua se actualiza in situ con el mismo comando — los datos del usuario se conservan.

## Acerca del proyecto

OpenPanda (**Open** **P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant) es un kernel de orquestación de tareas personal: un binario `panda` corre en cada uno de tus dispositivos, los nodos se descubren por un bus WebSocket autenticado, un modelo de entrada convierte cada petición en respuesta directa o en una especificación de tarea ejecutable, y el planificador enruta cada tarea al dispositivo y agente más adecuados. La CLI es la interfaz principal del kernel —`panda` a secas entra en un REPL interactivo— y la consola web es una cáscara fina sobre el mismo almacén y motor.

## Reglas de versiones

- Las versiones siguen `MAJOR.MINOR.PATCH`. El proyecto está en desarrollo inicial (`0.0.x`): un parche puede añadir funciones, corregir errores y —excepcionalmente— introducir cambios importantes, que siempre se listan bajo **Cambios importantes**.
- Un lanzamiento se corta etiquetando `vX.Y.Z`; cada commit desde la etiqueta anterior pertenece a la sección de la nueva versión. `[Unreleased]` recoge el trabajo desde la última etiqueta.
- Cada versión se documenta en cuatro categorías: **Añadido** (nuevas funciones), **Corregido** (correcciones), **Mejorado** (mejoras y refinamientos), **Cambios importantes** (requiere acción al actualizar).
- Cada entrada nombra el cambio y su efecto visible en una a tres líneas; se cita el commit que lo introdujo cuando ayuda a la arqueología.
- Este archivo en inglés es el canónico. Las traducciones zh-CN / ja / es / de lo replican y pueden retrasarse brevemente alrededor de un lanzamiento.

## [Unreleased]

## [0.0.7] - 2026-08-31

La versión de usabilidad: la tarjeta de capacidades —el archivo que dice al planificador qué puede hacer este nodo— ahora se puede editar desde todas las superficies (CLI, REPL, TUI y la consola web) sin reiniciar el daemon; añadir un segundo dispositivo es un flujo de producto en lugar de un rompecabezas de archivos de configuración; y cada resultado de tarea recibe ahora un resumen legible para humanos, de modo que el usuario ve lo que pasó en lugar de una pared de stdout crudo.

### Añadido

- **Edición estructurada de la tarjeta en todas partes** — `panda card native add|remove`, `panda card agent add|remove|set`, `panda card manual add|remove` (subcomandos estructurados, no solo el editor); las mismas operaciones desde `/card` en el REPL y el TUI; y un editor completo de tarjetas en la consola web (`/api/card` más endpoints agent/native/manual). Todas las rutas de escritura pasan por el mismo validador + `.bak` + tubería de escritura atómica, de modo que una edición defectuosa no puede corromper la tarjeta (1b8e2b7).
- **Emparejamiento de dispositivos** — `panda pair` genera un secreto compartido, imprime las instrucciones de incorporación para el nuevo dispositivo y escribe la configuración de ambos lados; `panda nodes add <addr>` añade un peer y lo marca en vivo sin reinicio; el CTA "Invitar un dispositivo" de la consola web ahora conecta con el flujo real de emparejamiento de la página de nodos (763bff6, 5748cec).
- **Recarga en caliente de la tarjeta** — editar la tarjeta (desde cualquier superficie) dispara `ReloadCard`: el planificador vuelve a leer, vuelve a registrar capacidades y difunde un latido con la nueva tarjeta a todos los peers conectados, de modo que los cambios se propagan sin reiniciar el daemon (3d6feeb).
- **TUI Bubble Tea** — `panda` ahora entra en un frontal Bubble Tea con una ruta de aprobación tier-2 funcional (tarjeta de aprobación en línea, `y` para aprobar, `n` para aparcar en `/approve`); el REPL clásico sigue disponible con `PANDA_CLASSIC_REPL=1` (06cca6a).
- **Resumen de tareas por LLM** — después de cada tarea en línea (éxito o fallo), el motor llama al modelo de entrada para producir un resumen legible de lo ocurrido; el resumen se muestra en el REPL, el TUI y la consola web antes de la salida cruda, de modo que el usuario ve "qué se hizo + salida clave" (éxito) o "por qué falló + qué hacer a continuación" (fallo) en lugar de stdout/stderr crudos. Un fallo del modelo degrada con elegancia: se omite el resumen y se muestra la salida cruda (esta versión).
- **Web: streaming de pensamiento y progreso de tareas** — el razonamiento del modelo se transmite al chat como un bloque de pensamiento plegable (03a4301); los mensajes de tarea muestran progreso y resultado en lugar de solo la carga útil (4ba931f).
- **Reanudación tier-2 remota** — cuando una tarea tier-2 se aprueba tras ser delegada a un nodo remoto, la reejecución ocurre en el ejecutor (donde pertenece el trabajo), no en la máquina del aprobador (3d6feeb).
- **Guardia de recuperación para goroutines residentes** — el nuevo `internal/guard` envuelve las goroutines de larga duración: un panic se registra con su pila completa y desencadena un apagado controlado en lugar de dejar un proceso medio muerto en ejecución; un panic en el bucle de lectura de una conexión del bus solo cierra esa conexión.
- **Apagado elegante en Windows** — los eventos de consola CTRL_CLOSE/LOGOFF/SHUTDOWN ahora activan la misma ruta de apagado ordenado que SIGTERM en unix (`SetConsoleCtrlHandler`, ventana de limpieza breve).
- **Colores en la consola de Windows** — la paleta del TUI habilita colores en la TTY de la consola de Windows cuando TERM no está definido; `dumb` y `NO_COLOR` siguen teniendo prioridad.
- **`make build-darwin-amd64`** — objetivo de compilación para Mac Intel, junto a los demás objetivos por plataforma.

### Corregido

- **Hallazgos de seguridad P0 cerrados** — el recorrido de rutas plan_id/stage_id (lectura y exfiltración arbitraria de directorios mediante `../../../../` en los directorios de trabajo de etapa) queda bloqueado por validación de ID + aserción de prefijo raíz; la entrega de resultados tras la desconexión de un peer se persiste en un outbox y se reentrega al reconectar (revisión P0-2); se corrigió la semántica de interrupción/salida del TUI para que Ctrl+C realmente salga (763bff6, 5129461).
- **Endurecimiento de seguridad P1** — la dirección de escucha por defecto cambió de `0.0.0.0:7836` a `127.0.0.1:7836` (el daemon ya no enlaza todas las interfaces por defecto); `context_fetch` ahora exige que el peer esté en la cadena de delegación de la tarea; la indisponibilidad del supervisor aparca la tarea para revisión en lugar de aceptar silenciosamente un resultado no verificado (763bff6).
- **Modelo de entrada: sin turnos de usuario duplicados** — los proveedores estrictos (compatibles con Anthropic) devolvían 400 en conversaciones donde la repetición de sesión duplicaba o dejaba colgando un turno de usuario; el paso de normalización ahora fusiona turnos consecutivos de texto plano del mismo rol (8174e78).
- **Tiempos de orquestación y carrera de mensajes web** — el tiempo del juez ya no se carga a la etapa en ejecución (un marcador de traza `judge_start` separado); el bucle de supervisión traza la ejecución antes del resultado de la ronda, de modo que las rutas continuar→continuar no ocultan la reejecución; el estado optimista de turnos web se extrae a `chatstate.ts` y, en error, se elimina la burbuja optimista para que la respuesta del asistente no caiga dentro de un mensaje de usuario (97d5c62).
- **Carrera entre cancelación y aceptación del ejecutor** — una cancelación que llegaba durante la ventana de aceptación del ejecutor se descartaba; ahora la cancelación se encola y se aplica cuando la aceptación termina (a19b33b).
- **Gate de Windows y apretón de manos de marcación mutua deterministas** — el gate de CI multiplataforma ahora pasa en Windows; el desempate de la marcación mutua es determinista sin importar el orden de llegada (526c731).
- **CI: trabajos de gate en paralelo** — el flujo de gate ahora ejecuta build/vet/test/typecheck como trabajos en paralelo, limita el detector de carreras a los paquetes que lo necesitan y aplica gate al typecheck de la consola web (3f302f1).
- **Exclusión mutua en las migraciones** — las migraciones de esquema se ejecutan bajo `BEGIN IMMEDIATE` y re-verifican `user_version` dentro de la transacción, de modo que dos procesos que abren la misma base de datos aplican cada versión exactamente una vez; un binario anterior al esquema de la base de datos ahora falla explícitamente en lugar de continuar en silencio.
- **Web: un único bus de eventos** — la consola ahora mantiene una sola conexión SSE con contador de referencias, autenticada con cabecera `Authorization` (sin token en la URL), con reconexión automática de retroceso exponencial, y distribuye los eventos change y trace a todos los suscriptores.
- **Web: carrera de flujo de sesión** — las escrituras de streaming solo se aplican mientras la sesión está activa; cambiar de sesión en pleno flujo ya no mezcla burbujas entre hilos, y las cargas de transcripción obsoletas se abortan al cambiar.
- **Web: robustez y accesibilidad** — un límite de errores de nivel superior con reintento; trampa de foco en la paleta de comandos y los diálogos de confirmación; tarjetas kanban operables por teclado (Enter/Space, con foco visible); el sondeo del sistema se pausa cuando la pestaña está oculta y omite sondeos aún en curso; claves de lista estables.
- **`panda skill --help` / `panda reminder --help`** — imprimen el uso y salen con 0 en lugar de tratar `--help` como un verbo desconocido.
- **CI: reparadas las patas del gate y el instalador** — reparadas las cuatro patas fallidas del gate y el pipeline del instalador (7c418b0).
- **CLI: los bloques de pensamiento plegados ya no anuncian una clave que no puede desplegarlos** (e772598).

### Mejorado

- **Dirección de escucha por defecto** — el daemon ahora enlaza `127.0.0.1:7836` por defecto en lugar de `0.0.0.0:7836`. Los despliegues existentes que dependían del valor anterior deben fijar `network.listen_addr` explícitamente en `config.yaml` o mediante `OPENPANDA_LISTEN_ADDR`.
- **Directorio de configuración del sistema según plataforma** — el directorio de respaldo para la configuración del sistema sigue siendo `/etc/openpanda` en unix y `%ProgramData%\OpenPanda` en Windows.
- **Una sola ruta de inicialización del almacén** — el daemon y el panel web abren el almacén mediante la misma función (`cmd/panda/store.go`); el panel ya no omite el directorio del pool de artefactos.
- **Panel web: los escaneos de eventos se desacoplan del número de conexiones** — las huellas de tareas/nodos/recordatorios se cachean durante un intervalo de sondeo, por lo que la carga de escaneo se mantiene prácticamente constante aunque crezcan los suscriptores.

## [0.0.6] - 2026-08-27

El lanzamiento de computación entre dispositivos toma forma: una solicitud que necesita máquinas distintas para pasos distintos es ahora un plan de primera clase cuyas etapas corren donde está el hardware, y ambas superficies —el CLI y la consola web— ganaron la capa de presentación que les faltaba: retroalimentación en vivo mientras un ask converge, Markdown de verdad en el navegador, y el editor de entrada que el uso diario exige.

### Añadido

- **Plan plane — tuberías cuyas etapas corren en máquinas distintas** — una etapa ES una tarea ordinaria (máquina de estados CAS, lease, reintentos, supervisión, estacionamiento en revisión), así que una tubería hereda todo lo que una tarea ya tiene; el directorio de trabajo de una etapa terminada se empaqueta, se trocea y viaja por el bus a la máquina que ejecuta la siguiente etapa. Dos entradas: `panda plan example > train.yaml`, `panda plan run train.yaml [--dry-run]`, `panda plan show <id>` — o una frase por `panda ask`, con el modelo emitiendo un plan precisamente cuando una solicitud debe cambiar de máquina. Ninguna etapa lleva consentimiento tier-2: una etapa irreversible se estaciona en revisión para una persona (c10b8af).
- **Enrutado por hardware declarado** — `resource_profile` es un filtro duro (`ledger.Fits`) y el puntuador ordena por capacidad libre + profundidad de cola + nivel, descontado por frescura de latido, así que dos tareas publicadas a la vez caen en dos máquinas; el prompt de entrada lleva el hardware real de cada nodo para que el modelo llene el filtro de enrutado con visión, no a ciegas (c10b8af).
- **`panda voice`** — palabra de activación → ASR → la misma tubería de entrada → TTS: la entrada de mascota de escritorio para un dispositivo sin teclado (c10b8af).
- **`panda card show | rescan | edit | set`** — una familia de comandos sobre la tarjeta de capacidades: imprimirla (y de qué archivo vino), re-escanear hardware y CLIs de agente instaladas (`rescan` imprime un diff, `--write` lo aplica guardando un `.bak`, las decisiones escritas a mano se preservan), abrirla en `$EDITOR`, o fijar campos sin interacción. `panda detect`, el re-escaneo de tarjeta y el panel comparten ahora una sola capa de detección (`internal/hwinfo`) (fdb56b8).
- **Una capa de presentación para el CLI** — `internal/cliui`: una paleta resuelta una sola vez, y una línea de estado viva (spinner, verbo, tiempo transcurrido, conteo de tokens — ambos ya se registraban, solo que nunca se mostraban) que degrada a línea estática en tuberías. El editor de línea aprende pegado con delimitadores y entrada multilínea (un prompt multilínea pegado es un solo ask, y el historial lo recuerda como uno), búsqueda incremental de historial con Ctrl-R, y completado en posición de argumento para los ids que nadie re-escribe. Los comandos desconocidos reciben un did-you-mean; `/help` se imprime en línea agrupado por intención; los nuevos comandos cubren lo que una sesión busca después del primer ask (`/cost`, `/model`, `/status`, `/doctor`, `/export`, `/clear`), más `@file` para adjuntar y `!cmd` directo, para no abandonar el prompt (c538ab6).
- **La superficie de chat web alcanza al resto** — un renderizador Markdown escrito a mano (cero `innerHTML`, sin dependencia de sanitizador; 29 tests de node) reemplaza el `**bold**` literal y las ``` vallas en las respuestas; la acción principal del compositor durante el streaming es un botón de detener (el lector SSE acepta un AbortSignal); el autoscroll deja de arrastrar la vista cuando el lector sube; paleta Cmd+K sobre el mismo vocabulario de navegación que la barra lateral; cajón de hilos móvil en lugar de `display:none` (c538ab6).
- **Una página de estado** — `docs/status.md` registra qué funciona, qué solo está construido y qué falta, con el estado de verificación de la tubería insignia (76c5b69).
- **Las filas de nodos obsoletas se pueden eliminar** — `panda nodes remove <id>` y un botón Eliminar en las tarjetas de nodos sin conexión borran una fila del directorio que ningún par vivo respalda (una máquina renombrada, una identidad cambiada, un nodo dado de baja). La fila del nodo local y los nodos en línea se rechazan — ambos se vuelven a registrar solos, así que "eliminarlos" sería un no-op vestido de mensaje de éxito.
- **Herramientas de notas de versión** — el flujo de publicación publica la sección del CHANGELOG de la versión más los comandos de instalación por plataforma como cuerpo del release, y falla la construcción si falta la sección; la página del release 0.0.5 se reescribió a ese estándar, con cuerpo solo en inglés y selector de idioma; cada CHANGELOG ahora abre con la instalación de un comando (4e12779, c25a3cb, 98e10df, 600ffb3).

### Corregido

- **Las tareas en cola y las etapas de plan ahora enrutan de verdad** — la ruta de cola conservaba un atajo local de "si puedo, lo hago yo" que el propio enrutador había eliminado, y era la vía que tomaba cada tarea del panel y cada etapa de plan: el filtro de hardware nunca corría donde la tubería insignia realmente corre (una etapa de GPU se quedaba en la Pi siempre que la Pi tuviera la habilidad; una ráfaga de tareas se quedaba entera en el nodo que las aceptó). La decisión pertenece al planificador; una lista de habilidades vacía significa "sin restricción", no "nadie coincide"; las etapas de plan obtienen una clave de recurso cada una, de modo que las etapas independientes se despliegan en abanico en vez de hacer cola entre sí (a5b792e).
- **El resultado de una ejecución terminada cabe en una trama** — la salida de `task_result` se recorta al tamaño de trama del bus, así el resultado de una ejecución completada llega al remitente en lugar de desbordar la trama y desaparecer (c1310da).
- **Las vallas de memoria no se pueden cerrar desde dentro** — la valla `<memory_data>` envolvía el cuerpo sin tocar las etiquetas del propio cuerpo: una entrada que contuviera la etiqueta de cierre literal terminaba la valla antes de tiempo y el resto se leía como instrucciones — y la memoria la pueden escribir las herramientas del propio modelo, el panel y los candidatos promovidos del sueño. Las etiquetas internas se neutralizan; el texto permanece visible para auditoría (3f18994).
- **Un nodo ya no describe el hardware de otra máquina** — cada una era un valor fijado donde debía ir una sonda: el nombre de nodo por defecto era "macbook", así que todo nodo que nunca ejecutó `panda init` se anunciaba con el nombre de la laptop del autor; macOS/Windows no tenían fuente de machine-id, así que renombrar la máquina parecía otro nodo; el sandbox de Windows arrancaba PATHEXT/SYSTEMROOT/TEMP — exactamente por eso un nodo de cómputo Windows no podía lanzar un adaptador; `python3` no es un nombre de intérprete portable (se sondea ahora, `py -3` primero en Windows); una tarea con tiempo agotado colgaba para siempre en Windows porque el harness no podía matar un árbol de procesos (`taskkill /T` ahora); una tarjeta que anuncia una habilidad nativa cuyo comando no existe ganaba la ruta y moría con 127 (podada al cargar); una GPU cuyo tamaño ninguna sonda podía leer escribía 0 y quedaba excluida justamente del trabajo para el que existe (desconocido es un tercer estado ahora); `deploy-pi.sh` usaba por defecto la dirección LAN de un desarrollador (ahora es obligatoria) (fdb56b8).
- **Regresiones de i18n cerradas** — chino fijado en la ruta de voz, la salida de planes de ask/repl, los resúmenes de conversación, los errores de desinstalación y un consejo de `panda help` pasaron a `internal/i18n` en los cinco idiomas — los usuarios de ja/es/de veían chino en esas superficies (c538ab6).
- **La REPL abre en menos de un segundo, no después del tiempo de espera del dial de pares** — el arranque interactivo marcaba a cada par configurado **en serie** antes del banner y luego esperaba a que se asentaran: un par sin conexión quemaba el timeout completo (10 s) del marcador como silencio muerto antes del primer prompt. La REPL, `panda session` y `panda voice` ahora marcan a los pares en segundo plano (un par sin conexión es rutina en una sesión larga, y su fallo ya no imprime líneas WARN a mitad de tecleo), y el `panda ask` de un solo uso marca en paralelo — un par inalcanzable deja de bloquear a uno alcanzable.

## [0.0.5] - 2026-08-25

El parche del laboratorio de tres dispositivos: el primer clúster real macOS + Orange Pi + Windows —instalado desde los instaladores públicos, enlazado por LAN y conducido de extremo a extremo— destapó que las tareas en cola nunca salían de su nodo de origen, que el consentimiento tier-2 moría en la frontera de delegación, y que una CLI de agente bloqueada podía atraer enrutamiento y colgarse minutos. Cinco commits, todos verificados en ese hardware.

### Añadido

- **`panda task add --requires`** — declara las capacidades que una tarea necesita (`--requires gpio:read`, separadas por comas); una tarea en cola sin coincidencia local se enruta al dispositivo que las tiene, la misma política de planificador raíz que `panda ask` siempre ha usado (c4e1bc7).

### Corregido

- **Las tareas en cola ahora se enrutan entre dispositivos** — las tareas de `panda task add` y de la consola web solo las reclamaba y ejecutaba el nodo de origen: una tarea que requería una capacidad que solo otro dispositivo tenía fallaba sin más (`route: no capability matches` al presentar `pi.uptime` desde un Mac). Al reclamar, el planificador consulta ahora al planificador raíz; sin coincidencia local, la reclamación se redirige a un peer capaz (protección de bucles por nodos rechazados, lease para detectar un ejecutor muerto), y el resultado del peer completa la fila del origen. Verificado en las tres direcciones del laboratorio: Mac→OrangePi, OrangePi→Mac, Windows→OrangePi (c4e1bc7).
- **La autorización tier-2 viaja con la delegación** — el consentimiento de `--authorize` era local al nodo que presentaba, así que una tarea de agente delegada a un peer rebotaba en la capa de defensa del ejecutor aunque el usuario la hubiera aprobado. El consentimiento se propaga por el bus autenticado y el ejecutor lo respeta: una Orange Pi sin credenciales que presenta una tarea coding autorizada al claude del Mac ahora completa en lugar de morir en review (c4e1bc7).
- **Las CLIs de agente bloqueadas ya no atraen enrutamiento** — la tarjeta de capacidades es estática, pero una CLI instalada puede estar inutilizable: un `claude.exe` en la máquina Windows sin sesión iniciada y sin clave de modelo anunciaba `agent:*` a la flota, el enrutamiento le envió una tarea coding, y quedó colgada minutos antes de fallar con un error de red. Tanto la cadena de respaldo local como el resumen de capacidades anunciado por hello ahora filtran por viabilidad — CLI en el PATH *y* un modelo alcanzable (credenciales propias o inyección); el resumen de esa máquina Windows ahora anuncia solo `win.sysinfo` (2db530f).
- **`panda web` ya no muere con el puerto ocupado** — un segundo `/web` (o un proceso residual) fallaba con `bind: address already in use` e imprimía un token para copiar a mano. La consola avanza a un puerto cercano y lo dice; el navegador se abre ya autenticado (el token no se imprime), y `/web` en marcha reabre el navegador con la sesión iniciada. `--no-browser` sigue imprimiendo una URL con token para uso manual (c4e1bc7).
- **El hello de peer informa la versión real** — las tres rutas de hello anunciaban un `0.1.0-dev` hardcodeado, así que `panda nodes` mostraba versiones erróneas en una flota con versiones mixtas; ahora informan `version.Version` (los tres dispositivos del laboratorio muestran 0.0.5) (2db530f).
- **La tarjeta de capacidades junto a la configuración resuelta gana a `./capabilities.yaml`** — arrancar un daemon desde un directorio que casualmente contiene un capabilities.yaml (un checkout del repo, la tarjeta de otro nodo) cargaba en silencio la tarjeta equivocada; la tarjeta escrita por init junto al archivo de configuración ahora tiene prioridad, y `--card` sigue mandando como vía explícita (2db530f).
- **El directorio de datos de Windows ya no colisiona con el prefijo de instalación** — el directorio de estado por defecto `%LOCALAPPDATA%\openpanda` y el prefijo de instalación `%LOCALAPPDATA%\OpenPanda` son el mismo directorio en NTFS sin distinción de mayúsculas: el almacén SQLite, la memoria y los proyectos vivían dentro del prefijo de instalación y una desinstalación se los llevaba por delante. El directorio de datos pasa a `%LOCALAPPDATA%\openpanda-data`; los nodos Windows que vienen de 0.0.4 arrancan con un almacén nuevo (fc50721).
- **Los instaladores sobreviven a una API de GitHub con límite de tasa y a un stack HTTP de WinPS 5.1 roto** — `api.github.com` permite 60 peticiones sin autenticar por IP y hora; al agotarse, ambos instaladores resuelven ahora la última versión vía el redirect 302 de `/releases/latest`. `install.ps1` fuerza TLS 1.2 al inicio, prefiere el `curl.exe` incluido (Windows 10 1803+) con `Invoke-WebRequest` como respaldo, y añade timeouts para que un proxy WinINET roto falle rápido en vez de colgarse. Ambos comportamientos se dieron de verdad durante la instalación real de los tres dispositivos (109b567).
- **Push del tap de Homebrew autenticado** — el paso de actualización del tap del flujo de release fallaba con `could not read Username` cuando el token del job carecía del permiso; la URL de push ahora lleva el token incrustado (6868a63).

## [0.0.4] - 2026-08-25

> GA: release de nodos distribuidos. Para detalles completos ve la [sección 0.0.4 en CHANGELOG.md](CHANGELOG.md).
>
> Destacados: tipo de nodo physical / vm + identidad estable, guardia singleton por host (paquete `nodeidentity`), endurecimiento del protocolo de adaptadores + tests de contrato, `/api/self` + `/api/nodes` y página Nodes en la web, utilidades de laboratorio de 3 nodos, y la corrección raíz del fallo de SQLite `SQLITE_CANTOPEN 14` en instalaciones Homebrew / cualquier cwd. Desde la beta: cachés de decisión del modelo de entrada, system prompt por capas, onboarding web sin configuración, harness de adaptadores compartido, UX de autorización tier-2, barrido de instalador/desinstalador, resumen de changelog en el actualizador, `panda init` de una pregunta y FAQ por escenarios.

### Añadido

- `node.kind = physical | vm` + identidad estable. VM exige `node.identity`; peer hello v2 transporta ambos campos; migración v10 de `employee_cache` rellena `DEFAULT 'physical'`.
- Singleton daemon guard (`nodeidentity`): `flock(2)` en Unix / `LockFileEx` en Windows; un segundo daemon sobre la misma identidad sale limpio con diagnóstico.
- Protocolo unificado `{ok, result, exit_code}` con captura de stderr como diagnóstico; `tests/adapter_contract_test.py` nuevo.
- Rutas `/api/self` + `/api/nodes` y pestaña Nodes en la web con tabla running/last-seen.
- `scripts/lab/*` + `scripts/scenario-model/` + `scripts/task-timeline/` + plan `docs/testing/distributed-lab-plan.md`.

### Corregido

- **Fallo de arranque en Homebrew / cualquier cwd (SQLITE_CANTOPEN 14)**: 1) `Default()` ahora usa `UserDataDir()` según plataforma, 2) `resolveRelativePaths()` en `Load()` reubica rutas relativas antiguas al directorio del YAML, 3) `storage.Open()` crea el padre de la DB, 4) `panelStore()` crea el directorio de storage completo en entradas REPL/web/queue.

### Mejorado

- `panda nodes` gana columna `Kind` (physical | vm).

## [0.0.3] - 2026-08-23

### Añadido

- **Registro multiagente de adaptadores** — `internal/agents` es la única fuente de verdad para los CLI de agentes que PANDA delega (script adaptador, binario de sondeo, comando de instalación, URL de docs). `panda detect`, `panda agents`, la API de ajustes web y el sondeo de disponibilidad del commander leen de ahí, de modo que añadir un agente es cambiar una sola entrada.
- **Cuatro nuevos adaptadores de agente** — Grok Build, DeepSeek Harness (`dsh`), OpenClaw y Hermes se unen a Codex, Claude Code y OpenCode: cada uno un pequeño puente Python headless que ejecuta el CLI y devuelve `{ok, result, exit_code}`.
- **`panda agents`** — `list` (por defecto) sondea cada agente en PATH con la mejor versión posible; `test <name>` ejecuta una comprobación de conectividad; `install|update <name>` imprime el comando de instalación + enlace a la documentación. Cuando no hay nada instalado, la salida lista el comando de instalación y la URL de descarga de cada agente faltante.
- **Lista de agentes en ajustes web** — la lista de agentes de la página de ajustes muestra ahora, para cada agente que falta, su comando de instalación y un enlace de descarga (`/api/agents` devuelve `install_hint` + `install_url`).
- **Revisión superior de tareas (`superior task review`)** — tras ejecutarse un agente, el modelo de entrada evalúa el resultado contra los criterios de éxito de la tarea (`entry.Supervise`, salida `done`/`continue`). Un veredicto `continue` re-delega la instrucción de continuación (lo que falta + siguiente paso) a la cadena de agentes, en bucle hasta que el revisor acepta el trabajo o se agota un presupuesto de rondas (5 por defecto).
- **Enrutado terminal por riesgo** — una tarea reversible completada cae en **done** (已完成); una tarea irreversible (Tier-2) aceptada — pushes, borrados, cambios de estado irreversibles — se aparca en **review** (待审批) con su resultado a la espera de tu firma; una tarea que el revisor sigue rechazando se aparca en **review** con la marca `needs_followup`. Los eventos de revisión se reproducen en el detalle de tarea web.
- **Instalador de una línea** — `scripts/install.sh` (POSIX) y `scripts/install.ps1` (PowerShell) descargan el archivo de release correspondiente, verifican su SHA-256, desempaquetan el binario y sus adaptadores de agente en un prefijo por usuario y enlazan `panda` en el `PATH`, con un servicio de arranque automático opcional (`panda daemon` al iniciar sesión). Un tap de Homebrew (`brew tap Xustalis/openpanda && brew install openpanda`) cubre macOS.
- **Empaquetado de releases** — `scripts/package.sh` (y `make package`) compilan de forma cruzada todas las plataformas soportadas en `dist/panda-<version>-<os>-<arch>.tar.gz` / `.zip` más un `checksums.txt`, listos para GitHub Releases.
- **Auto-actualización** — `panda web` y el `/web` de la REPL comprueban el canal de releases en busca de un CLI más nuevo mientras se ejecutan; la consola web descarga y verifica la actualización y, una vez la cola de tareas está inactiva, la aplica en un clic (intercambio atómico del binario, refresco de adaptadores, reinicio). Descartar una actualización descargada no deja residuos; en Windows el `.old` de respaldo del intercambio se limpia en el siguiente arranque.

### Corregido

- **El banner multilínea de `--version`** (p. ej. Hermes) ya no ensucia la tabla de agentes de una línea — la salida de versión se trunca a su primera línea tanto en la CLI como en la API de ajustes web.

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
