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
                               │ WebSocket directo P2P (Sin nube externa)
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
- **Descubrimiento LAN + fijación TOFU**: Los nodos se encuentran en la LAN con admisión confirmada por huella; `panda nodes verify` fija la clave Ed25519 de cada par.
- **Tarjetas de capacidad dinámicas**: Cada nodo declara su perfil de hardware (CPU, GPU, RAM, SO) y herramientas disponibles.
- **Enrutamiento inteligente de tareas**: Asigna compilaciones pesadas a servidores potentes y tareas de sensores a placas de bajo consumo según capacidad medida; `panda nodes drain` retira un nodo para mantenimiento.
- **Delegación con worktree**: Una tarea de archivos delegada a otro nodo lleva su checkout allí y trae el resultado de vuelta.
- **Despacho de actuadores**: Las tareas pueden accionar actuadores físicos (servo, micrófono, cámara, notificación, MCU serie) en el nodo que posee el hardware.
- **Malla P2P privada**: Comunicación directa por WebSocket autenticado y cifrado y plano de datos UDP cifrado con atravieso NAT. Tu código y memoria nunca salen de tus dispositivos.

### 2. 🤖 Orquestación universal de agentes y failover automático
- **Adaptadores listos para usar**: Compatible con Claude Code, OpenAI Codex, Grok Build, DeepSeek Harness, OpenCode y comandos de shell.
- **Inyección de modelos de respaldo**: Si un agente agota su cuota de tokens o fallan sus credenciales (401/403), OpenPanda inyecta automáticamente modelos alternativos configurados.
- **Trazabilidad total en tiempo real**: Visualiza comandos bash, ediciones de archivos y llamadas a herramientas en tu terminal o navegador.

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
- **TUI interactiva de terminal**: Construida con Bubble Tea, con modo de pantalla alternativa a pantalla completa, navegación por teclado, asistente de primer arranque, progreso en vivo y redirección en marcha.
- **Consola Web integrada**: Tablero Kanban, streaming en tiempo real vía SSE, gestión de habilidades, cancelación de sesiones y login automático.
- **CLI para scripts**: Comandos rápidos como `panda ask` para integrar en scripts y pipelines.
- **Árboles de sesión con bifurcación**: `panda session fork <id> --at N` bifurca la conversación en cualquier turno; en un repositorio, el worktree hijo se ramifica desde la rama del padre y hereda el código producido. `panda session tree` muestra la familia; `/fork` hace lo mismo en REPL/TUI.
- **Compactación automática**: El historial desbordado se pliega en un resumen corriente escrito por el modelo en vez de descartarse; el hilo almacenado permanece íntegro.
- **`panda rpc`**: Superficie de incrustación NDJSON sobre stdio (`status`, `ask` en streaming, `session.*`); `scripts/panda_rpc.py` es un cliente de referencia solo-stdlib.
- **OAuth de suscripción**: `panda auth login anthropic` inicia sesión en Claude Pro/Max vía PKCE con refresco transparente del token.

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
brew tap Xustalis/openpanda
brew install openpanda
```

**Windows (PowerShell):**
```powershell
irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
```

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

1. En el dispositivo A: ejecuta `panda pair` para obtener el código de emparejamiento.
2. En el dispositivo B: ejecuta `panda nodes add <dirección-dispositivo-A>`.
*¡Ambos dispositivos quedan conectados en tu malla P2P!*

---

## 🛠️ Referencia de comandos

| Comando | Descripción |
|---|---|
| `panda` | Iniciar la TUI interactiva completa de Bubble Tea |
| `panda ask "<consulta>"` | Ejecución directa: responder, ejecutar herramienta o delegar |
| `panda web` | Iniciar la consola web integrada con inicio de sesión automático |
| `panda nodes` | Listar dispositivos conectados en la malla P2P |
| `panda pair` | Generar código de emparejamiento para nuevos nodos |
| `panda queue` | Ver tareas pendientes, en ejecución y en revisión |
| `panda approve <id>` | Aprobar una acción irreversible de nivel 2 |
| `panda project list` | Gestionar proyectos y contexto del espacio de trabajo |
| `panda session` | Listar, bifurcar y reanudar sesiones (`session tree` muestra la familia) |
| `panda skill` | Explorar, importar e instalar habilidades de flujo de trabajo (Hub, URL o archivo) |
| `panda auth login` | Iniciar sesión OAuth en una suscripción de modelo (p. ej. `anthropic`) |
| `panda rpc` | API NDJSON sobre stdio para incrustar OpenPanda |
| `panda doctor` | Diagnóstico de PATH, configuración, adaptadores y base de datos |
| `panda version` | Mostrar la versión del binario |

---

## 🗺️ Hoja de ruta

| Versión | Tema |
|---|---|
| **v0.0.8** (base estable) | Orquestación multiagente en una sola máquina, plenamente utilizable: clasificación de intención, despacho, bucle de supervisión, failover, aprobación por niveles, política de idioma en prompts |
| **v0.0.9** (estable) — "Periapsis" | Arquitectura de transporte híbrido/DTN completada: enrutado mesh ponderado por latencia, bundles DTN sobre el cable, presupuestos de tokens, copias sombra, ruta de actuadores, plano de datagramas UDP con hole punching NAT coordinado por la malla, payloads cifrados, planes de contacto, identidad de nodo Ed25519, compilación lite — más aprobaciones recordadas, atribución de ejecución, un chip de nodo en la barra lateral web y pulido de la CLI cotidiana |
| **v0.0.10** (actual) — "Apoapsis" | Hacia fuera, a la LAN y al borde: descubrimiento con admisión confirmada por huella y pinning TOFU de claves, tareas de archivo delegadas que viajan con su worktree, el bucle de clarificación, despacho de actuadores con cinco drivers de referencia (MCU serie incluido), el adaptador Pi y un ejecutor generic sin Python — más árboles de sesión con bifurcación, compactación automática, la superficie de incrustación `panda rpc`, OAuth de suscripción, cadenas de eventos/auditoría firmadas, sandbox a nivel de SO y una ronda de rendimiento en régimen. Relicenciado a AGPL-3.0-or-later con licencia comercial dual |
| **v0.0.x (más allá)** | Estabilidad, rendimiento y ajuste de casos límite |
| **v0.1.0** | Capacidades de escritorio y un control y una gestión más potentes — calidad comercial |

---

## 🔭 Visión

La arquitectura de OpenPanda está diseñada para clústeres heterogéneos a gran escala: control de enjambres de drones, planificación de comunicaciones para constelaciones de satélites en el espacio profundo y flotas coordinadas de vehículos autónomos. Estos escenarios comparten un problema fundamental: **cada nodo tiene distinta capacidad de cómputo, distintas capacidades y distintas tareas, y aun así todos deben coordinarse, planificarse y supervisarse en conjunto.**

Hoy OpenPanda sirve a los dispositivos y agentes de un desarrollador. El objetivo a largo plazo es extender el mismo núcleo de orquestación a nodos autónomos a escala de clúster.

---

## 🤝 Contribuir

¡Agradecemos las contribuciones de la comunidad! Consulta [CONTRIBUTING.es.md](CONTRIBUTING.es.md), [SECURITY.md](SECURITY.md) y [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) antes de enviar un Pull Request.

---

## 📄 Licencia

OpenPanda se distribuye bajo doble licencia:

- **Comunitaria** — [GNU Affero General Public License v3.0 o posterior](LICENSE) (AGPL-3.0-or-later)
- **Comercial** — licencia propietaria para integración en código cerrado u oferta alojada; consulta [COMMERCIAL.md](COMMERCIAL.md)

Las versiones publicadas antes del cambio siguen disponibles bajo la licencia MIT.
