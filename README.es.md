# 🐼 OpenPanda

**Sistema Operativo de Agentes Personales de Código Abierto y Orquestador Multidispositivo Local-First**

> Conecta todos tus dispositivos en una malla peer-to-peer privada y "contrata" agentes de IA de terminal (Claude Code, Codex, Grok, etc.) para colaborar entre máquinas como un equipo unificado.

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![Release: v0.0.8-preview](https://img.shields.io/badge/release-v0.0.8--preview-blue.svg)](https://github.com/Xustalis/OpenPanda/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
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
- **Tarjetas de capacidad dinámicas**: Cada nodo declara su perfil de hardware (CPU, GPU, RAM, SO) y herramientas disponibles.
- **Enrutamiento inteligente de tareas**: Asigna compilaciones pesadas a servidores potentes y tareas de sensores a placas de bajo consumo.
- **Malla P2P privada**: Comunicación directa por WebSocket autenticado y cifrado. Tu código y memoria nunca salen de tus dispositivos.

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
- **Delegación contextual**: Al transferir una tarea entre máquinas, la memoria del proyecto viaja con ella.

### 5. 🖥️ Tres interfaces unificadas
- **TUI interactiva de terminal**: Construida con Bubble Tea, con navegación por flechas, progreso en vivo y redirección en marcha.
- **Consola Web integrada**: Tablero Kanban, streaming en tiempo real vía SSE, diseño adaptable para móviles y login automático.
- **CLI para scripts**: Comandos rápidos como `panda ask` para integrar en scripts y pipelines.

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
| `panda doctor` | Diagnóstico de PATH, configuración, adaptadores y base de datos |
| `panda version` | Mostrar la versión del binario |

---

## 🤝 Contribuir

¡Agradecemos las contribuciones de la comunidad! Consulta [CONTRIBUTING.es.md](CONTRIBUTING.es.md), [SECURITY.md](SECURITY.md) y [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) antes de enviar un Pull Request.

---

## 📄 Licencia

OpenPanda es software de código abierto bajo la [Licencia MIT](LICENSE).
