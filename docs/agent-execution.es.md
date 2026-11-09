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
