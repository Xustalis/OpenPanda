# Modelos y concurrencia de agentes y subagentes

Abre **Configuración → Agents** para elegir modelos principales e hijos y límites de concurrencia y delegación. Sustituye los modelos del ejemplo por IDs disponibles en cada CLI.

```yaml
injection:
  model: never
routing:
  max_concurrent: 6
  agent_concurrency:
    codex: 2
    claude_code: 2
  agent_models:
    codex: [my-root-model]
    claude_code: [my-claude-model]
  subagent_models:
    codex: [my-child-model]
    claude_code: [my-child-claude-model]
delegation:
  enabled: true
  max_concurrent: 3
  fanout: 3
  max_children: 12
  max_depth: 2
```

- `routing.max_concurrent` limita llamadas activas y admisión de cola de este proceso. Cero hereda la capacidad de la tarjeta.
- `agent_concurrency` añade límites por agente; las claves pueden ser nombres de tarjeta o archivos de adaptador. Los alias de un adaptador comparten el límite de archivo.
- `subagent_models` afecta a hijos creados por OpenPanda. Una clave ausente hereda el modelo principal; `[]` explícito usa el predeterminado del CLI. Vaciar el campo restaura la herencia.
- `delegation.max_concurrent` limita hijos activos entre todos los padres; cero no añade límite. `fanout` limita hijos creados y esperados por padre; `max_children` limita el total acumulado y `max_depth` las generaciones.
- Reducir límites no interrumpe llamadas activas. Un padre esperando hijos no ocupa una plaza de llamada.
- Son límites de llamadas activas de un proceso, no cuotas del clúster ni límites de agentes internos del proveedor o sesiones inactivas. Procesos independientes no comparten un semáforo estricto.
- En un lote de varias subtareas con proyecto y `fanout` efectivo mayor que uno, las hermanas sin `paths`, `isolate` ni `workdir` se aíslan automáticamente y pueden ejecutarse en paralelo, sujetas a los límites del worker, del agente y de delegación; sus cambios se integran con la fusión existente, que detecta conflictos. Una tarea única sin ámbito sigue bloqueando todo el proyecto. Los `paths` disjuntos explícitos evitan la copia; las rutas solapadas en un árbol compartido siguen serializadas.

La selección es nativa del CLI. Los modelos inyectados y las variables explícitas de modelo mantienen prioridad. El ejemplo usa `injection.model: never` y credenciales propias del CLI; no lo copies si necesitas credenciales de Panda. Usa `injection.agents` para cambiar endpoint y credenciales. Los CLI de un modelo usan el primer candidato; OpenCode consulta su catálogo. ZCode admite `provider/model`; las plantillas generic usan `{model}`.

OpenCode y dsh en modo headless usan la configuración de su propia CLI. OpenCode usa la selección de Panda cuando se especifica un modelo, una lista de preferencias o `OPENPANDA_OPENCODE_AUTO_MODEL=1`. dsh aplica un parche temporal al proveedor solo al inyectar un modelo. Claude Code conserva la conexión del modelo de los ajustes del usuario y las herramientas de solo lectura en modo restringido.

Guardar aplica al proceso actual inmediatamente; los otros procesos locales sincronizan aproximadamente cada segundo y antes de ejecutar. Las llamadas activas conservan su modelo.

```sh
panda config routing set agent_models codex my-root-model
panda config routing set subagent_models codex my-child-model
panda config routing set max_concurrent 6
panda config routing set agent_concurrency codex 2
panda config delegation set max_concurrent 3
panda config delegation set fanout 3
panda config routing get --json
```

API: `GET /api/settings/execution`, `PUT /api/settings/execution` (Bearer token).

## Plazos de ejecución y contexto

- El cambio de candidato, los reintentos transitorios y el rescate de credenciales comparten un plazo por ronda: el límite de la tarea o el global, más el margen de cierre. Tras cancelar no se inician candidatos nuevos. Un panic cierra su propio intento y libera recursos.
- Los fallos de proveedor/sesión de OpenCode terminan la llamada inmediatamente; un idle terminal después de una respuesta también cierra una CLI que mantiene abierto el canal. El idle inicial, los errores de herramientas y los eventos de otras sesiones no completan la tarea. Las tareas Codex gestionadas conservan sesiones en disco; los diagnósticos independientes siguen siendo efímeros por defecto. Se admite `thread.started` y los eventos antiguos.
- El contexto tiene presupuestos flexibles de 12.000 / 32.000 / 64.000 bytes según complejidad; si se desconoce, 32.000. Se conservan requisitos, criterios de aceptación, alcance y restricciones del protocolo. Los cuerpos e índices de skills comparten el presupuesto restante. El índice de memoria personal selecciona hasta seis temas relevantes en 4.000 bytes, manteniendo el aislamiento de proyectos. `panda help` permite consultar el catálogo completo.
- Al desbordarse el contexto, el reintento reduce el presupuesto a la mitad, hasta 32.000 bytes. Las notas pueden reducirse a 12.000 bytes con omisiones explícitas y UTF-8 válido. Los requisitos completos se conservan para recuperación; las continuaciones elegibles envían solo instrucciones nuevas. `exec_agent_start` registra el tamaño completo preparado y `prompt_context` el transmitido, en bytes, no tokens.
- La revisión y el resumen usan el mismo modelo con configuración breve independiente: máximo de 2.048 tokens de salida, un reintento de transporte y una solicitud para desactivar el razonamiento extendido, según soporte del proveedor. La revisión dispone de 45 segundos; los fallos siguen pasando a revisión humana. La configuración de conversación y agentes no cambia. La herramienta de lectura `model_list` consulta modelos configurados/activos sin crear una tarea de agente.

Estos cambios requieren desplegar el binario y los adaptadores actualizados; la recarga de configuración no sustituye el código en ejecución.

## Habilidades autónomas y contexto reducido

- El modelo de entrada y los agentes pueden buscar y leer habilidades activas según la necesidad, también durante la ejecución, sin que el usuario las nombre. Las coincidencias normales envían metadatos y rutas; una habilidad `always` se precarga si su cuerpo cabe en 3.200 bytes y en el presupuesto restante. Al desactivar `routing.panda_tools` se mantiene la carga de cuerpos dentro del presupuesto.
- Usa `panda skill search <keywords>` y `panda skill show <name>` (alias `read`), o `/skills search` y `/skills show` en TUI. Distingue nombres iguales con `--scope project --key project-name`; lee referencias con `--path references/guide.md`. Las respuestas incluyen `next_offset`, `sha256`, recursos y directorio. Continúa con `--offset N --sha256 HASH` para detectar cambios. Cada página usa 6.000 bytes por defecto, hasta 16.000, respetando UTF-8.
- Los directorios, zip y tar.gz conservan scripts, referencias y recursos, con un límite expandido total de 10 MiB y 1.024 archivos; se rechazan enlaces y rutas que escapen. Las descargas iniciadas por agentes quedan pending hasta aprobarlas en primer plano. En ejecución solo se ven habilidades globales, del proyecto y dispositivo actuales. Previsualizar pending no activa ni cuenta como uso. La biblioteca se resuelve localmente en el nodo ejecutor; no se añade sincronización entre dispositivos.
- La entrada expone primero los schemas esenciales; `tool_search` descubre los demás y `query="*"` pagina el catálogo. Se conservan seis rondas de trabajo, con hasta dos rondas extra exclusivamente de descubrimiento que expongan nuevos schemas. El MCP integrado usa `panda mcp --compact`: descubre con `panda_tools` y ejecuta con `panda_tool_call`. Sin la opción se conserva el catálogo completo. Configuración MCP externa, autorización y límites mantienen sus reglas.
- Solo las sesiones Claude Code/Codex que recibieron contexto completo correctamente en este episodio pueden recibir incrementos, con adapter, checkpoint, instrucciones estáticas y huellas de reglas raíz coincidentes. Inicio, reinicio, reglas cambiadas, otros adapters, desbordamiento y candidatos en frío reciben entrada completa. Un rechazo claro del checkpoint exacto, antes de observar trabajo con herramientas, permite una recuperación completa.
- La página de habilidades ofrece búsqueda, identificación por ámbito, previsualización de recursos y paginación. `skill_load` muestra lecturas reales; `prompt_context`, modos completo/incremental/recuperación y bytes reales/completos; `loaded_skills`, solo cuerpos precargados en ese intento. Coincidir o leer no demuestra cumplimiento ni éxito. Los tokens de lectura/escritura de caché informados se contabilizan aparte; menos bytes y costes estimados no equivalen al ahorro facturado.

## Síntesis de planes y vista de ejecución

- Un plan generado por el modelo puede indicar `final_stage`, que debe ser el único destino alcanzable desde todas las ramas. Si falta en un plan con varias etapas, se añade un agent de síntesis dependiente de todos los destinos, dentro del límite de 64 etapas. Una etapa única se marca como final; el YAML manual conserva su estructura. La síntesis respeta capacidades, dispositivos fijados, agentes permitidos y permisos.
- Se conservan las comprobaciones, políticas de herramientas y límites de turnos de cada etapa. Los criterios del usuario se incorporan a la etapa final; los identificadores en conflicto conservan ambos requisitos. Los resultados textuales, acotados y sin secretos, se guardan en `.panda-stage-reports/<stage>.json` y viajan por el transporte causal de artefactos existente. Son resultados candidatos anteriores a la aceptación final.
- Tras terminar todas las etapas, CLI/TUI devuelve la respuesta final; la conversación Web conserva los informes y la entrega. Los planes desplegados muestran dependencias, etapa final, estados actualizados y distinguen el destino de envío del dispositivo ejecutor reportado. Cada tarea permite consultar agente, modelo y evidencia. Una columna común representa profundidad de dependencias, no cálculo simultáneo medido.
- Se utilizan el emparejamiento, el enrutamiento por capacidades y el transporte existentes. Esto no certifica equipos físicos Windows/Linux/macOS ni placas, ni garantiza tiempo, coste o calidad globalmente óptimos. No se incluyen líneas de tiempo por dispositivo ni sincronización de bibliotecas de habilidades entre equipos.

## Tareas de investigación y evidencia de ejecución

- El spec admite `task_kind`: `code_question`, `investigation`, `edit` u `operations`. Omitirlo mantiene compatibilidad; no concede permisos. Las investigaciones/preguntas usan `plan` de solo lectura; las ediciones respetan los permisos de la conversación. Una investigación profunda se delega al coding harness; un fallo de delegación no convierte una lista de directorios en una revisión terminada.
- La investigación aporta evidencia de implementación, llamadores/pruebas relevantes, alcance y puntos sin verificar. El análisis estático no exige compilar/probar. Una búsqueda limitada no demuestra ausencia global. Las ediciones informan verificaciones relevantes; las operaciones comprueban la poscondición. Solo lectura no ejecuta automáticamente comandos acceptance.
- `execution_environment`, local al worker por attempt, registra cwd, commit/rama/dirty de git (incluidos archivos sin seguimiento) y huellas acotadas de `AGENTS.md`/`CLAUDE.md` raíz. No es un índice completo de reglas ni una comprobación de dependencias; no registra sus textos ni ejecuta comandos del proyecto. Los fallos de git quedan unknown.
- `adapter_execution_profile`, desde un límite de proceso confiable, registra preferencia de modelo solicitada, inyección/herramientas, restricción/consentimiento, límites, solicitud de reanudación y capacidades declaradas. Las sesiones persistentes conservan la configuración inicial. No confirma la configuración nativa: modelo/esfuerzo/versión no confirmados quedan unknown. Excluye prompts, credenciales, URL del proveedor, cuerpos MCP y plantillas de comandos.
- Son eventos locales del worker, sin nueva UI específica ni sincronización entre nodos. No implican un registro completo de afirmaciones/evidencias ni un verificador de finalización. Requieren desplegar el nuevo binario.

## Límites de enrutamiento, permisos y planes

- Las preguntas conceptuales conservan la vía rápida. Las preguntas sobre símbolos/rutas concretos requieren leer el código y no admiten respuestas sin herramientas. Las investigaciones profundas del repositorio y sus seguimientos en contexto (incluido Side) requieren un coding harness. La falta de despacho, la cola, la espera de aprobación o los fallos no se convierten en trabajo completado mediante resúmenes de convergencia. Este enrutamiento no cambia el esfuerzo de razonamiento del CLI nativo ni verifica cada afirmación contra evidencia.
- La política global de herramientas es un límite superior; cada tarea solo puede restringirla. `extended` no concede permisos adicionales del SO. Codex usa `workspace-write` normalmente y `read-only` para trabajo restringido, sin cambiar a `danger-full-access` por la política de herramientas. Las investigaciones/preguntas de código tipadas también son de solo lectura aunque exista consentimiento guardado; no pueden usar comandos native sin sandbox de solo lectura.
- Las herramientas de entrada de una conversación `plan` necesitan una declaración explícita de solo lectura, al igual que los atajos locales. `task_submit`/`task_budget` mantienen la delegación restringida. Las herramientas MCP desconocidas carecen de esa declaración y se deniegan por defecto; aún no se implementa una configuración confiable de capacidades MCP de solo lectura. Leer skills observa el contenido existente sin instalar implícitamente los integrados.
- Los planes multietapa conservan `task_kind`, permisos de conversación y selección de agentes, e incorporan una instantánea acotada del espacio de origen. La falta de almacenamiento de artifacts, los fallos de empaquetado o los archivos omitidos provocan un fallo antes de crear tareas. Las etapas remotas usan directorios del nodo receptor, nunca el cwd absoluto de origen. Los resultados se transfieren mediante artifacts y **no se escriben ni fusionan automáticamente en el checkout original**.
- La clasificación con mapa del espacio de trabajo o herramientas de lectura evita cachés persistentes basadas solo en rutas, para no reutilizar conclusiones anteriores a cambios sin commit. Las observaciones de verificación requieren resultados completados y sin error de herramientas de comandos, con coincidencia exacta del programa y argumentos. No cuentan `echo`, subcadenas, comandos pendientes o fallidos, ni se infieren rutas de composiciones shell complejas. Estas observaciones no equivalen a una aceptación independiente aprobada.
- Side tiene su propio historial y la misma superficie de herramientas (incluido despacho) y controles de permisos/aprobación del hilo principal. El historial principal es solo referencia: Side no añade turnos ni lo compacta. El enrutamiento rápido/profundo y la configuración de razonamiento son capas distintas; no garantizan paridad con la herramienta nativa.
- Los planes remotos conservan la memoria del proyecto sin adjuntar otro checkout; mantienen la instantánea fuente y el orden de las entradas predecesoras.

## Aceptación, bloqueos y planificación de etapas

- Las etapas generadas o YAML admiten `acceptance`, `success_definition`, `tools_policy` y `max_turns`. Se validan rutas, políticas y el límite de 0..200 turnos; la política de herramientas no concede autorización ni amplía una ejecución restringida.
- El veredicto `blocked` requiere cambiar condiciones externas o entradas y deja la tarea en revisión. Aprobar no convierte trabajo incompleto conocido en terminado; hay que continuarlo o rechazarlo.
- Las ramas independientes de una misma máquina pueden ejecutarse en paralelo, sujetas a dependencias, capacidad y bloqueos de recursos. Dentro de un plan y prioridad se ordenan por la ruta crítica ponderada restante; prevalece el orden manual de la cola.

## Recuperación de conversaciones y progreso

Los resúmenes de razonamiento del proveedor se guardan separados de la respuesta: solo se muestran, no se inventan ni se reproducen al modelo. Se guardan puntos de control del texto, resumen y progreso; tras recargar, Detener cancela el mismo ID de operación y conserva lo recibido.

La compactación mantiene el historial original y un cursor del prefijo resumido para limitar el contexto. No puede recuperar mensajes eliminados ni resúmenes nunca guardados por versiones anteriores. Se respetan las credenciales y endpoints nativos; Codex inyectado usa Responses. Un saldo agotado requiere corregir la cuenta después del rescate permitido, no repetir la tarea indefinidamente.

Al terminar, la conversación original guarda un informe de entrega separado (hasta 24.000 caracteres; si se recorta, enlaza la salida original). El modelo solo recibe el resumen breve. Los pasos reales, rutas modificadas, verificación independiente y pendientes se ven por defecto; el informe y la cronología completos se pueden desplegar. `done` no implica verificación si no hay comprobaciones registradas. Los informes antiguos solo recuperan salida del mismo intento para evitar que un reintento los reescriba. TUI muestra resultado, pasos, verificación y atención tanto en primer plano como al completar tareas en segundo plano.

- Mientras esperan hijos, los padres liberan capacidad de ejecución y bloqueos del árbol, conservando latidos, cancelación y exclusión del dispositivo. Recuperan capacidad y bloqueos antes de continuar. La espera pertenece a un intento y se limpia al reintentar, cancelar o recuperar tras un reinicio.
- Los errores de integración aislada, las bases ausentes y los conflictos bloquean la tarea en revisión con cambios de entrada necesarios y conservan el espacio de trabajo. Aprobar no certifica la entrega. Los hermanos comparten una instantánea inmutable por lote y respetan los directorios explícitos.
- El grafo completo se publica en una transacción. Los artefactos llevan metadatos causales generados por el núcleo: conservan cambios y eliminaciones, los descendientes prevalecen sobre sus ancestros, los cambios concurrentes diferentes entran en conflicto y los iguales se integran. Los planes nuevos solo se envían a nodos que anuncian compatibilidad; varias instantáneas antiguas sin una base fiable se bloquean. Las salidas del plan no se escriben automáticamente en el checkout original.

## Tareas de trabajo, TaskSync y entrega controlada

`work` gestiona objetivos e historial; `task` sigue representando una ejecución del núcleo. La página Web de tareas y colaboración muestra tareas locales, espacios y decisiones. `todo start` reutiliza su ejecución vinculada; `work run` crea una solicitud nueva. Los todos locales son la fuente de verdad y las tareas remotas son una caché autorizada por cuenta.

Conecta con `panda connection connect --root https://example.com/tsync/ --id team` y autoriza en el navegador externo. Panda no recoge contraseñas de TaskSync. Los permisos iniciales son `tasks:read,members:read`. Ejecutar requiere además `execution:read,execution:claim,execution:report`; entregar requiere `delivery:write` y comunicar la aceptación personal requiere `delivery:report-decision`. Solicita permisos adicionales al reconectar con `--scopes`. Credenciales y claves de firma quedan en archivos restringidos junto a la base de datos y no aparecen en las salidas ni exportaciones.

La conexión solo sincroniza hasta que una política local con caducidad habilita investigación, ejecución o acción final. Configúrala en Web o con `panda work policy --connection team --space SPACE --file policy.json`; omite `--file` para consultarla. Vincula cuenta e IDs explícitos de tareas/proyectos, con límites de tokens y concurrencia. Una tarea sin asignar solo recibe investigación privada limitada, sin reclamarla ni cambiar su estado. Consulta con `panda work research INSTANCE/WORKSPACE/SPACE/TASK --connection team`. La memoria personal de investigación no se inyecta en ejecuciones de proyecto.

Usa los detalles del espacio o CLI work binding para vincular explícitamente la memoria de proyectos. --connection y --space seleccionan el ámbito; omite --remote-project para todo el espacio. Omite --local-project para ver los vínculos o pasa un valor vacío para eliminar uno. El vínculo de proyecto tiene prioridad sobre el del espacio. Los nombres no crean vínculos. Cambiar uno pausa el trabajo y elimina los resúmenes privados, sin conceder permisos de directorios ni acciones.

El primer adaptador de acción final publica un texto exacto en un directorio absoluto autorizado, fija la identidad del directorio y nunca sobrescribe archivos. Los demás agentes, shell y herramientas de navegador son solo de preparación en este flujo. Un borrador no completa el objetivo; la cobertura total exige una afirmación humana separada. Identidad, inicio de sesión y extensión del navegador tienen su propia aceptación posterior.

```sh
panda work runs --connection team
panda work result REQUEST
panda work export REQUEST
panda work approve ACTION --fingerprint HASH
panda work reject ACTION --fingerprint HASH
panda work accept DELIVERY --revision 1
panda work return DELIVERY --revision 1 --reason 'Faltan pruebas'
panda work pause REQUEST
```

`before_submit` pide consentimiento local al contenido, destino, cuenta, intento y versión de entrada exactos antes de publicar. `after_execution` exige preautorización explícita y después aceptación personal de una versión de entrega. Devolverla no deshace acciones realizadas. Web y `/work` del REPL comparten resultados persistidos y decisiones, separados de las autorizaciones de operaciones del núcleo.

Tras una respuesta perdida o reinicio, las acciones dispatching/unknown se concilian mediante comprobantes, sin enviarlas de nuevo. El outbox persistente solo reintenta hechos; los resultados completados pendientes de confirmación aparecen pendientes de sincronizar. Consulta el estado de conexión y diagnósticos de resultados. Al recibir revocaciones o cambios de cuenta/entrada se invalida el trabajo derivado y se limpia el contenido ya no legible. No se puede borrar remotamente una copia sin conexión. Antes de revertir, pausa solicitudes nuevas y conserva puntos de control de ejecución y outbox. Un binario antiguo puede rechazar el nuevo esquema; no sobrescribas hechos nuevos con una base antigua.

Crear o editar tareas remotas requiere taskWrite negociado y tasks:write tanto en la conexión como en el espacio. Usa work add --connection team --space SPACE --title Title --remote-priority 2; para editar, indica la clave completa y la revisión de origen con --if-match. --remote-project selecciona el proyecto de origen; una cadena vacía lo elimina. --start-at/--due-at aceptan JSON con kind, value y timezone opcional; null elimina la fecha. Las horas flotantes no adoptan la zona del dispositivo. work run --connection team solicita solo la generación actual según la política local y guarda la intención antes de obtener el turno. Rechaza planes locales y no incrementa la generación. El usuario crea las repeticiones en TaskSync. Reintenta con el mismo --request. Las respuestas de escritura solicitan sincronización y no sustituyen directamente la caché del feed.

MCP ofrece panda_work_list, panda_work_show, panda_work_history, panda_work_request y panda_work_result. Los agentes de proyecto solo consultan su proyecto local y las tareas remotas vinculadas explícitamente; ningún argumento amplía ese alcance. Los resultados verifican también el proyecto de ejecución para proteger el contexto anterior tras cambiar un vínculo. Las páginas usan nextOffset y sha256; un contexto desconocido se rechaza. MCP no permite editar políticas, firmar, autorizar acciones finales, aceptar entregas ni consultar investigación privada.
