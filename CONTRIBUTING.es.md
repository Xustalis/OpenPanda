# Contribuir a OpenPanda

Gracias por tu interés en mejorar OpenPanda. Este documento cubre la cadena de
herramientas, las puertas de ingeniería que todo cambio debe superar y las
convenciones que mantienen el código legible a medida que crece.

## Prerrequisitos

| Herramienta | Versión | Usada para                                    |
| ----------- | ------- | --------------------------------------------- |
| Go          | ≥ 1.22  | kernel, CLI, panel, tests                     |
| Node.js     | ≥ 18    | consola web (`webui/app`)                     |
| Python      | ≥ 3.10  | sidecar de voz, adaptadores de agentes        |

## Primeros pasos

```bash
git clone https://github.com/Xustalis/OpenPanda
cd openpanda
make run            # arranca el daemon con la configuración de ejemplo
make web            # compila la consola en webui/panel/dist (go:embed)
make build-webui    # sidecar del panel independiente con la consola embebida
```

El daemon embebe la consola web en tiempo de compilación. Sin `make web` se
embebe una página de marcador, así que `go build` funciona sin Node — **antes
de enviar cualquier cambio de UI ejecuta `make web`**.

Para conocer rápidamente la superficie interactiva:

```bash
panda repl          # comandos con barra: /tasks /approve /projects /nodes /web …
```

## Puertas de ingeniería

Un pull request se fusiona solo cuando **todas** las puertas están en verde.
Ejecútalas localmente antes de hacer push:

```bash
make gate           # build + vet + test + race (la puerta de fusión)
gofmt -l internal/ cmd/ adapters/ webui/panel/   # no debe imprimir nada
cd webui/app && npm run typecheck                # si tocaste la consola web
```

- **`go test -race ./...` debe pasar** — el kernel es un sistema concurrente
  (registro de pares, almacén de tareas, hub SSE); cualquier hallazgo del
  detector de carreras es bloqueador de release, no un simple aviso.
- Los módulos centrales (`internal/core`, `internal/scheduler`, `internal/storage`)
  deben mantenerse por encima de **~60 % de cobertura** cuando sea viable.
  Las correcciones de errores van acompañadas de un test de regresión que
  falle antes de la corrección.
- Los nuevos protocolos de alambre o comportamientos de delegación necesitan
  un test loopback — sigue el patrón de `internal/core/dedup_test.go` y
  `scripts/smoke-delegate`.

## Convenciones de código

- **Errores**: envuelve con `%w`, comprueba con `errors.Is/As`. Nunca descartes
  un error en silencio; nunca registres en log **y** lo devuelvas desde la misma
  función (una cosa u otra).
- **Los comentarios explican el porqué, no el qué**. Un lector dentro de seis
  meses necesita el invariante, la compensación o la referencia a un incidente
  — no una repetición del código. Las decisiones de concurrencia no obvias
  (orden de cerrojos, por qué un close ocurre fuera del mutex) deben quedar
  documentadas en el propio lugar.
- **Nada de código muerto, nada de abstracciones especulativas**. Tres líneas
  parecidas ganan a una interfaz prematura. Borra el código no usado en vez
  de comentarlo.
- **Concurrencia**: cada mutex documenta qué protege. Ningún cerrojo se mantiene
  durante E/S o un envío por canal. Los remitentes son dueños del cierre;
  el dueño de una goroutine es identificable desde el punto donde se lanzó.
- **Seguridad**: falla cerrado (fail closed). Cualquier cosa que clasifique,
  autorice o edite información sensible debe por defecto tomar la rama
  restrictiva (véase el modelo Tier en `internal/defense`). Las claves de
  configuración nuevas que guarden secretos reciben el tratamiento chmod 0600
  + aviso; nunca registres secretos en el log.

## Estilo de commits

Conventional Commits, en sintonía con el historial:

```
feat(cli): REPL interactivo — comandos con /, consola /web, i18n en 5 idiomas
fix(core): deduplicación mutuo-dial determinista — acaba el flap de 1s
feat(web): consola completa — cola/detalle/pregunta/proyectos/nodos + binario único con go:embed
```

Usa `feat` / `fix` / `docs` / `refactor` / `chore` / `test` como tipo, con un
ámbito sacado de la estructura de primer nivel (`core`, `cli`, `web`,
`scheduler`, `defense`, …). El asunto va en imperativo y es lo suficientemente
específico como para sobrevivir en `git log --oneline`.

## Consola web (webui/app)

- Stack: Vite + Preact + TypeScript, sin dependencias en runtime más allá de
  Preact.
- **Todas las cadenas visibles al usuario pasan por i18n.** Añade la clave a
  todos los locales en `webui/app/src/i18n/` (inglés es el fallback; las
  claves ausentes caen en inglés, así que nunca envíes una clave solo en
  inglés pensando en "arreglarla luego").
- La misma regla para el CLI: `internal/i18n/messages.go`.
- Añadir un idioma: copia el mapa inglés, tradúcelo, registra el locale tanto
  en `internal/i18n/i18n.go` como en `webui/app/src/i18n/index.ts`, y añade
  un enlace en el README. Mantén las claves "grepeables" — la clave es el
  identificador.

## Pull requests

1. Haz fork, crea una rama desde `main` (`feat/…`, `fix/…`).
2. Mantén los PR pequeños y de un solo propósito; una funcionalidad y su
   refactor son dos PR distintos.
3. Actualiza `CHANGELOG.md` bajo `[Unreleased]` — Added / Changed / Fixed.
4. Las actualizaciones del README en 5 idiomas solo son obligatorias si
   cambias el comportamiento visible de la CLI o añades una entrada a la lista
   de funcionalidades; traducir tu párrafo a los otros cuatro idiomas se
   agradece pero no bloquea la fusión — los mantenedores sincronizarán las
   traducciones.
5. `make gate` en verde → PR → revisión.

## Informar sobre problemas de seguridad

No abras un issue público para vulnerabilidades en la autenticación del
transporte, el modelo Tier o las capas de redacción. Repórtalas en privado al
mantenedor (consulta el contacto de seguridad en la configuración del
repositorio) con pasos para reproducirlo. La cadena de auditoría
(`panda audit verify`) está ahí precisamente para verificar las correcciones
frente a manipulaciones.

## Licencia

Al contribuir aceptas que tu trabajo se publica bajo la [Licencia MIT](LICENSE)
del proyecto.
