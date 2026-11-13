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
- Al desbordarse el contexto, el reintento reduce el presupuesto a la mitad, con un máximo de 32.000 bytes. Las notas pueden reducirse a 12.000 bytes con omisiones explícitas y UTF-8 válido. La reanudación conserva los requisitos completos para candidatos que arranquen en frío. `prompt_bytes` y `context_budget_bytes` en `exec_agent_start` miden bytes, no tokens.
- La revisión y el resumen usan el mismo modelo con configuración breve independiente: máximo de 2.048 tokens de salida, un reintento de transporte y una solicitud para desactivar el razonamiento extendido, según soporte del proveedor. La revisión dispone de 45 segundos; los fallos siguen pasando a revisión humana. La configuración de conversación y agentes no cambia. La herramienta de lectura `model_list` consulta modelos configurados/activos sin crear una tarea de agente.

Estos cambios requieren desplegar el binario y los adaptadores actualizados; la recarga de configuración no sustituye el código en ejecución.

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
