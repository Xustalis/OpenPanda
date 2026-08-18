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
}

export default es
