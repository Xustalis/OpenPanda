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
- Las escrituras paralelas requieren `paths` disjuntos, otro `workdir` o `isolate`. Los ámbitos solapados o desconocidos siguen serializados.

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
