import type { Messages } from './index'

const es: Messages = {
  // Shell / navigation
  'app.name': 'OpenPanda',
  'app.tagline': 'Orquestación personal de tareas entre tus dispositivos',
  'nav.queue': 'Cola',
  'nav.ask': 'Preguntar',
  'nav.projects': 'Proyectos',
  'nav.nodes': 'Nodos',

  // Token gate
  'token.title': 'Conecta con tu nodo',
  'token.description':
    'Introduce el token del panel de la configuración de este nodo (network.panel_token en config.yaml).',
  'token.label': 'Token del panel',
  'token.submit': 'Conectar',
  'token.invalid': 'Token rechazado — compruébalo e inténtalo de nuevo.',
  'token.logout': 'Desconectar',

  // Common
  'common.loading': 'Cargando…',
  'common.error': 'Algo salió mal.',
  'common.retry': 'Reintentar',
  'common.cancel': 'Cancelar',
  'common.save': 'Guardar',
  'common.close': 'Cerrar',
  'common.empty': 'Aún no hay nada aquí.',

  // Task states
  'state.submitted': 'enviada',
  'state.queued': 'en cola',
  'state.dispatched': 'asignada',
  'state.waiting_context': 'esperando contexto',
  'state.running': 'en ejecución',
  'state.review': 'pendiente de revisión',
  'state.done': 'completada',
  'state.failed': 'fallida',
  'state.cancelled': 'cancelada',
  'state.expired': 'expirada',

  // Queue view
  'queue.subtitle': 'Todas las tareas que conoce este nodo, en vivo.',
  'queue.allStates': 'Todos los estados',
  'queue.allProjects': 'Todos los proyectos',
  'queue.empty': 'Sin tareas. Pide algo en Preguntar para crear una.',
  'queue.title': 'Título',
  'queue.project': 'Proyecto',
  'queue.state': 'Estado',
  'queue.owner': 'Responsable',
  'queue.updated': 'Actualizada',

  // Detail view
  'detail.back': 'Volver a la cola',
  'detail.approve': 'Aprobar',
  'detail.reject': 'Rechazar',
  'detail.cancelTask': 'Cancelar tarea',
  'detail.rejectedViaWeb': 'rechazada vía panel web',
  'detail.id': 'ID de tarea',
  'detail.project': 'Proyecto',
  'detail.owner': 'Nodo responsable',
  'detail.attempt': 'Intento',
  'detail.created': 'Creada',
  'detail.updated': 'Actualizada',
  'detail.risk': 'Riesgo',
  'detail.result': 'Resultado',
  'detail.timeline': 'Cronología',

  // Ask view
  'ask.subtitle': 'Una entrada — una respuesta o una tarea delegada.',
  'ask.hint': 'Pregunta lo que quieras. OpenPanda responde, ejecuta herramientas o delega tareas a tus dispositivos.',
  'ask.placeholder': 'Pregunta a OpenPanda… (Enter para enviar, Shift+Enter para nueva línea)',
  'ask.authorize': 'Autorizar acciones de riesgo',
  'ask.voice': 'Entrada de voz',
  'ask.send': 'Enviar',
  'ask.thinking': 'Pensando…',
  'ask.taskCreated': 'Tarea creada.',

  // Projects view
  'projects.subtitle': 'Un proyecto delimita colas de tareas y memoria por proyecto.',
  'projects.namePlaceholder': 'nombre del nuevo proyecto',
  'projects.create': 'Crear',
  'projects.empty': 'Aún no hay proyectos — crea uno arriba.',

  // Nodes view
  'nodes.subtitle': 'El directorio de capacidades: cada dispositivo al que este nodo puede delegar.',
  'nodes.empty': 'Todavía no hay nodos registrados.',
  'nodes.lastSeen': 'Visto por última vez',
  'nodes.never': 'nunca',
}

export default es
