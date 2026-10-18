# Condiciones de Uso y Descargo de Responsabilidad de OpenPanda

Idiomas: [English](TERMS.md) (versión vinculante) · [简体中文](TERMS.zh-CN.md) · [日本語](TERMS.ja.md) · Español · [Deutsch](TERMS.de.md)

**Esta traducción al español se ofrece solo como referencia; en caso de discrepancia rige la [versión en inglés](TERMS.md).**

*Vigente para todas las versiones de OpenPanda distribuidas a partir del 2026-10-11 inclusive.*

Estas Condiciones de Uso y Descargo de Responsabilidad (las «Condiciones»)
se aplican al software OpenPanda (el «Software») publicado por Xustalis
(el «Licenciante», «nosotros»), bajo cualquiera de las dos vías de
distribución — la GNU Affero General Public License v3.0 o posterior
(`LICENSE`) o una licencia comercial negociada (`COMMERCIAL.md`).

**Contenido:** 1. Aceptación · 2. Qué hace el Software · 3. Sin garantía
· 4. Asunción del riesgo · 5. Servicios de terceros · 6. Limitación de
responsabilidad · 7. Indemnización · 8. Actividades de alto riesgo ·
9. Funciones experimentales · 10. Privacidad y datos · 11. Uso lícito;
exportación · 12. Marcas · 13. Colaboradores · 14. Sin obligación de
soporte · 15. Cambios · 16. Ley aplicable · 17. Disposiciones varias

## 1. Aceptación; relación con las licencias

Al descargar, instalar, copiar, modificar, distribuir o usar el Software,
usted acepta estas Condiciones. Si no las acepta, no use el Software. Si
usa el Software en nombre de una organización, declara estar autorizado
a vincularla, y «usted» se refiere a esa organización. Debe tener al
menos la mayoría de edad legal en su jurisdicción para aceptar estas
Condiciones por sí mismo; de lo contrario, un padre, tutor legal u
organización responsable debe aceptarlas en su nombre y es responsable
de su uso.

La aceptación mediante el aviso de primera ejecución o de reconsentimiento
se registra en su propio dispositivo con una marca temporal UTC
(`terms_accepted_at` en la configuración de la UI) y, cuando el almacén
del despliegue está abierto, una entrada en su cadena de auditoría
firmada; ambos registros permanecen en sus máquinas.

Estas Condiciones son condiciones de uso; no modifican ni sustituyen la
licencia bajo la que se le entrega su copia. Para copias entregadas bajo
la AGPL, las disposiciones de exclusión de garantía, limitación de
responsabilidad, indemnización y marcas que siguen operan como
Condiciones Adicionales del tipo que la sección 7 de dicha licencia
permite expresamente; en la medida en que alguna disposición de estas
Condiciones pudiera leerse como una restricción adicional a los permisos
que la AGPL concede, dicha disposición se separa y la AGPL prevalece.
Si dispone de un acuerdo de licencia comercial firmado, se aplican sus
propias cláusulas de garantía, indemnidad y responsabilidad; donde dicho
acuerdo guarda silencio, estas Condiciones rigen como referencia.

## 2. Qué hace el Software — léalo, por favor

OpenPanda es un orquestador, no un entorno aislado. Coordina herramientas
de línea de comandos de agentes de IA y ejecución directa de shell en una
o varias máquinas unidas en una malla par a par. Según su configuración
puede:

- ejecutar comandos de shell arbitrarios; crear, modificar y eliminar
  archivos; instalar paquetes y, en general, alterar el estado del
  sistema;
- ejecutar CLIs de agentes de terceros (p. ej. Claude Code, OpenAI Codex
  y otros) que generan y ejecutan acciones autónomamente y pueden
  facturar a sus cuentas de API;
- abrir escuchas de red, conectarse a nodos pares y delegar tareas entre
  máquinas;
- realizar operaciones clasificadas como tier-2/irreversibles tras
  aprobación humana;
- comprobar y aplicar actualizaciones de sí mismo desde un canal de
  publicación.

El Software hace estas cosas **bajo su configuración, en hardware que
usted controla, con sus credenciales y por su indicación**. Usted — no
el Licenciante — opera el sistema y es dueño de sus resultados.

## 3. SIN GARANTÍA — el Software se proporciona «TAL CUAL»

EN LA MÁXIMA MEDIDA PERMITIDA POR LA LEY APLICABLE, EL SOFTWARE SE
PROPORCIONA «TAL CUAL» Y «SEGÚN DISPONIBILIDAD», CON TODOS SUS DEFECTOS
Y SIN GARANTÍA NI CONDICIÓN DE NINGÚN TIPO. EL LICENCIANTE, SUS AFILIADAS
Y TODOS LOS COLABORADORES RECHAZAN TODAS LAS GARANTÍAS Y CONDICIONES,
EXPRESAS, IMPLÍCITAS, LEGALES O DE OTRO TIPO, INCLUIDAS SIN LIMITACIÓN
LAS GARANTÍAS O CONDICIONES IMPLÍCITAS DE COMERCIABILIDAD, IDONEIDAD PARA
UN FIN PARTICULAR, TITULARIDAD, NO INFRACCIÓN, EXACTITUD, GOCE PACÍFICO E
INTEGRACIÓN DE SISTEMAS, Y CUALQUIER GARANTÍA DERIVADA DEL CURSO DE LAS
OPERACIONES, EL USO O LA PRÁCTICA COMERCIAL.

SIN LIMITAR LO ANTERIOR, NO SE GARANTIZA QUE EL SOFTWARE CUMPLA SUS
REQUISITOS; QUE FUNCIONE DE FORMA ININTERRUMPIDA, OPORTUNA, SEGURA O
LIBRE DE ERRORES; QUE LOS DEFECTOS SE CORRIJAN; NI QUE SEA COMPATIBLE CON
NINGÚN HARDWARE, SOFTWARE O SERVICIO DE TERCEROS EN PARTICULAR. DONDE LA
LEY APLICABLE NO PERMITA LA EXCLUSIÓN DE GARANTÍAS IMPLÍCITAS, ESTA
SECCIÓN SE APLICA EN LA MÁXIMA MEDIDA PERMITIDA Y TODA GARANTÍA
INEVITABLE SE LIMITA EN DURACIÓN AL PERÍODO MÁS BREVE PERMITIDO POR LA
LEY.

LA MISMA BASE «TAL CUAL» SE APLICA A LA DOCUMENTACIÓN, LOS ARCHIVOS DE
EJEMPLO, LAS CONFIGURACIONES DE MUESTRA, LOS SCRIPTS DE ADAPTADORES Y
CUALQUIER OTRO MATERIAL DISTRIBUIDO CON EL SOFTWARE.

## 4. Asunción del riesgo — operación autónoma

Usted entiende, acepta y asume el riesgo de que:

- **Los comandos y las acciones de los agentes pueden ser destructivos,
  irreversibles o inseguros.** La salida producida por modelos de IA es
  probabilística y puede ser errónea, dañina, insegura o destructiva.
  Configurar niveles de permisos, colas de aprobación, aislamiento y
  listas de permisos de red, y supervisar lo que el sistema ejecuta, es
  responsabilidad suya. Los avisos de aprobación y las barreras son
  ayudas de flujo de trabajo, no garantías de seguridad.
- **Usted es responsable de la malla que construye.** Cada nodo confía
  en todo par que posea el secreto compartido; un nodo comprometido puede
  comprometer toda la malla (véase `SECURITY.md`/`SECURITY.en.md`).
  La gestión de claves, el cifrado de transporte, el enlace de escuchas
  y las decisiones de admisión son exclusivamente suyos.
- **Los costes son suyos.** Los CLIs de agentes, las API de modelos, el
  ancho de banda, la computación y otros servicios de pago invocados a
  través del Software se facturan a sus cuentas bajo sus propios
  acuerdos con esos proveedores. Los reintentos, las cadenas de
  delegación y los agentes descontrolados pueden consumir cuota de API
  sin límite. El Licenciante no es responsable de ningún cargo incurrido.
- **Los datos son suyos.** Las copias de seguridad, las instantáneas y la
  confidencialidad de archivos, credenciales y datos personales en sus
  sistemas son su responsabilidad. El material dirigido a proveedores de
  agentes de terceros se transmite bajo su configuración y sus términos —
  revise sus políticas de privacidad antes de habilitar esas rutas.
- **No es asesoramiento profesional.** Los planes, análisis, respuestas
  y código producidos mediante el Software no constituyen asesoramiento
  legal, médico, financiero, de seguridad u otro asesoramiento
  profesional, y no se declaran correctos ni aptos para uso alguno.

## 5. Servicios y componentes de terceros

El Software orquesta, invoca o interoperar con CLIs de agentes de
terceros, proveedores de modelos, registros de paquetes y canales de
publicación (los «Servicios de Terceros»). Los Servicios de Terceros se
rigen únicamente por sus propios términos y políticas. El Licenciante no
los controla, no los avala ni asume responsabilidad alguna por ellos, su
salida, su disponibilidad, sus cambios o los datos que usted les
transmita, y no responde de actos u omisiones de ningún Servicio de
Terceros. Los componentes de terceros incluidos se enumeran en
`THIRD_PARTY_NOTICES.md` y se le licencian bajo sus propias licencias,
que rigen dichos componentes.

## 6. LIMITACIÓN DE RESPONSABILIDAD

EN LA MÁXIMA MEDIDA PERMITIDA POR LA LEY APLICABLE, EN NINGÚN CASO EL
LICENCIANTE, SUS AFILIADAS NI NINGÚN COLABORADOR O DISTRIBUIDOR DEL
SOFTWARE SERÁN RESPONSABLES ANTE USTED O TERCEROS POR DAÑOS INDIRECTOS,
INCIDENTALES, ESPECIALES, CONSECUENTES, EJEMPLARES O PUNITIVOS, NI POR
LUCRO CESANTE, PÉRDIDA DE INGRESOS, DATOS, FONDO DE COMERCIO, OPORTUNIDAD
DE NEGOCIO O AHORROS ESPERADOS, NI POR EL COSTE DE BIENES O SERVICIOS
SUSTITUTOS, QUE SURJAN DEL SOFTWARE O DE ESTAS CONDICIONES O ESTÉN
RELACIONADOS CON ELLOS — INCLUIDOS SIN LIMITACIÓN LOS DAÑOS CAUSADOS POR
COMANDOS O ACCIONES EJECUTADOS POR EL SOFTWARE O POR LOS AGENTES QUE
COORDINA, PÉRDIDA O CORRUPCIÓN DE DATOS, INCIDENTES DE SEGURIDAD O ACCESO
NO AUTORIZADO, EXPOSICIÓN EN RED, FALLOS O CARGOS DE SERVICIOS DE
TERCEROS, O INTERRUPCIÓN DEL NEGOCIO — CUALQUIERA QUE SEA SU CAUSA Y BAJO
CUALQUIER TEORÍA DE RESPONSABILIDAD (CONTRATO, RESPONSABILIDAD
EXTRACONTRACTUAL, NEGLIGENCIA, RESPONSABILIDAD OBJETIVA, ESTATUTO U OTRA),
AUNQUE UNA PARTE HAYA SIDO ADVERTIDA DE LA POSIBILIDAD DE DICHOS DAÑOS Y
AUNQUE UN REMEDIO NO CUMPLA SU PROPÓSITO ESENCIAL.

EN LA MEDIDA EN QUE LA RESPONSABILIDAD NO PUEDA EXCLUIRSE POR COMPLETO,
LA RESPONSABILIDAD AGREGADA DEL LICENCIANTE QUE SURJA DEL SOFTWARE O DE
ESTAS CONDICIONES O ESTÉ RELACIONADA CON ELLOS SE LIMITA AL IMPORTE QUE
USTED PAGÓ AL LICENCIANTE POR EL SOFTWARE EN LOS DOCE MESES ANTERIORES AL
HECHO QUE DIO LUGAR A LA RECLAMACIÓN — LO QUE, PARA LA EDICIÓN
COMUNITARIA, ES CERO.

ESTA SECCIÓN NO EXCLUYE NI LIMITA LA RESPONSABILIDAD QUE LA LEY
IMPERATIVA NO PERMITE EXCLUIR O LIMITAR, COMO LA RESPONSABILIDAD POR
DOLO O NEGLIGENCIA GRAVE, O POR MUERTE O LESIONES PERSONALES, DONDE Y EN
LA MEDIDA EN QUE LA LEY PROHÍBA LA EXCLUSIÓN.

TODA RECLAMACIÓN O CAUSA DE ACCIÓN QUE SURJA DEL SOFTWARE O DE ESTAS
CONDICIONES O ESTÉ RELACIONADA CON ELLOS DEBE PRESENTARSE DENTRO DE UN
(1) AÑO DESDE QUE LA RECLAMACIÓN DEVENGA; DESPUÉS QUEDA PERMANENTEMENTE
PROHIBIDA, SALVO QUE UNA LEY IRRENUNCIABLE EXIJA UN PLAZO MAYOR.

## 7. Indemnización

En la máxima medida permitida por la ley aplicable, usted indemnizará,
defenderá y mantendrá indemnes al Licenciante y a todos los colaboradores
y distribuidores del Software frente a todas y cada una de las
reclamaciones, daños, pérdidas, responsabilidades, costes y gastos
(incluidos honorarios razonables de abogados) que surjan de o estén
relacionados con: (a) su uso, operación o despliegue del Software;
(b) acciones ejecutadas en sistemas bajo su control por el Software o por
los agentes que coordina; (c) su modificación o redistribución del
Software; (d) su incumplimiento de estas Condiciones o de cualquier
licencia aplicable o términos de terceros; o (e) su violación de la ley
aplicable o de derechos de terceros.

## 8. Actividades de alto riesgo

El Software no está diseñado, certificado ni garantizado para entornos en
los que su fallo o mal funcionamiento pudiera provocar muerte, lesiones
personales o daños ambientales o materiales graves — incluidos sistemas
de soporte vital, sistemas de armas, comunicaciones de emergencia o
control crítico de seguridad de aeronaves, vehículos o instalaciones
industriales. Si decide desplegarlo en tales contextos, lo hace
enteramente bajo su propio riesgo, y la indemnización de la Sección 7 se
aplica a las reclamaciones resultantes.

## 9. Funciones experimentales

Las funciones documentadas como experimentales o de vista previa (por
ejemplo la superficie de integración `panda rpc`, el OAuth de suscripción
y la voz) se proporcionan solo para evaluación; pueden estar incompletas,
cambiar de forma incompatible o eliminarse en cualquier versión. Todas
estas Condiciones se les aplican en su totalidad — suponga que se
romperán.

## 10. Privacidad y tratamiento de datos

El Software es local-first: no envía telemetría al Licenciante y transmite
su código, prompts y archivos solo donde su propia configuración los
dirige (por ejemplo a proveedores de agentes de terceros o a canales de
publicación/actualización como GitHub, cada uno regido por su propia
política de privacidad). Cualquier dato personal tratado a través de su
despliegue lo trata usted como responsable; el cumplimiento de la ley de
protección de datos en sus sistemas es su responsabilidad.

## 11. Uso lícito; control de exportación

Solo puede usar el Software conforme a la ley aplicable. No puede usarlo
para violar derechos de terceros, para realizar vigilancia o intrusión
ilegal, para crear o distribuir malware, ni para ningún fin ilícito.
Usted declara que no se encuentra en un territorio sometido a embargo
integral, que no figura en ninguna lista de sanciones o partes
restringidas aplicable, y que no exportará, reexportará ni transferirá
el Software en violación de la ley de control de exportaciones o
sanciones aplicable.

## 12. Marcas

«OpenPanda» y las marcas asociadas son marcas del Licenciante. Ni estas
Condiciones ni la AGPL conceden licencia de marca alguna: no puede usar
el nombre ni las marcas para identificar productos o servicios derivados,
ni para insinuar respaldo, sin permiso escrito. Véase `NOTICE`.

## 13. Colaboradores

Cada descargo, limitación de responsabilidad y protección de estas
Condiciones se extiende a cada colaborador individual y distribuidor del
Software. En la máxima medida permitida por la ley, ningún colaborador es
personalmente responsable de nada de lo cubierto por las Secciones 3 a 7.

## 14. Sin obligación de mantenimiento ni soporte

El Licenciante no tiene obligación de proporcionar actualizaciones,
correcciones, mantenimiento o soporte, ni de continuar desarrollando o
distribuyendo el Software. Las versiones pueden añadirse, cambiarse o
retirarse en cualquier momento. El soporte, la indemnización a su favor
o los compromisos de SLA existen solo bajo un acuerdo comercial firmado.

## 15. Cambios a estas Condiciones

El Licenciante puede revisar estas Condiciones periódicamente. Una
revisión rige para las versiones publicadas con posterioridad; las
Condiciones incluidas con una versión determinada siguen rigiendo esa
versión. La tarjeta de consentimiento de la aplicación es solo un
resumen — en caso de discrepancia, prevalecen `LICENSE` y estas
Condiciones.

## 16. Ley aplicable y fuero

Estas Condiciones y toda controversia que surja de ellas o del Software o
esté relacionada con ellos se rigen por las leyes de la jurisdicción en
la que el Licenciante esté domiciliado o principalmente establecido, sin
atender a sus normas de conflicto de leyes. Los tribunales de esa
jurisdicción tienen jurisdicción exclusiva sobre tales controversias,
salvo que una parte pueda solicitar medidas cautelares u otra tutela
provisional por uso indebido de propiedad intelectual ante cualquier
tribunal competente, y salvo que la ley imperativa de protección del
consumidor le conceda un fuero distinto. No se aplica la Convención de
las Naciones Unidas sobre los Contratos de Compraventa Internacional de
Mercaderías.

## 17. Disposiciones varias

- **Divisibilidad.** Si alguna disposición de estas Condiciones se
  considera inaplicable, se aplica en la máxima medida permitida y el
  resto permanece vigente.
- **No renuncia.** No hacer valer una disposición no constituye renuncia
  a ella.
- **Subsistencia.** Las Secciones 3 a 7 y 12 a 17 subsisten a la
  terminación de su uso del Software o de sus derechos sobre él.
- **Sin representación.** Nada en estas Condiciones ni en su uso del
  Software crea una sociedad, joint venture, relación laboral o de
  representación entre usted y el Licenciante.
- **Cesión.** El Licenciante puede ceder o transferir estas Condiciones
  en relación con una reorganización, venta de activos o transferencia
  del proyecto. Usted no puede ceder sus derechos ni obligaciones bajo
  ellas sin el consentimiento previo por escrito del Licenciante; toda
  cesión pretendida sin consentimiento es nula.
- **Fuerza mayor.** El Licenciante no responde de ningún incumplimiento
  o demora derivado de causas fuera de su control razonable.
- **Notificaciones.** El Licenciante puede notificar sobre el Software y
  estas Condiciones mediante publicación en el repositorio del proyecto,
  en el sitio web del proyecto o en notas de versión; dicha notificación
  es eficaz desde su publicación.
- **Encabezados.** Los encabezados de sección son solo de conveniencia y
  no afectan a la interpretación.
- **Acuerdo completo.** Junto con la licencia bajo la que se le entrega
  su copia, estas Condiciones constituyen el acuerdo completo entre usted
  y el Licenciante sobre el uso del Software, sustituyendo entendimientos
  previos sobre ese asunto.
- **Idioma.** Estas Condiciones se publican en inglés, chino, japonés,
  español y alemán; rige la versión en inglés.
- **Contacto.** Xustalis — fing2024@outlook.com.
