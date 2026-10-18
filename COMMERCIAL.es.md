# Licencias de OpenPanda: Comunitaria y Comercial

Idiomas: [English](COMMERCIAL.md) (versión vinculante) · [简体中文](COMMERCIAL.zh-CN.md) · [日本語](COMMERCIAL.ja.md) · Español · [Deutsch](COMMERCIAL.de.md)

*Esta traducción se ofrece solo como referencia; en caso de discrepancia rige la versión en inglés.*

OpenPanda tiene licencia dual. La misma base de código se ofrece bajo la
GNU Affero General Public License v3 o posterior (AGPL-3.0-or-later)
para uso de código abierto, y bajo una licencia comercial separada para
los casos que la AGPL no cubre.

## Licencia comunitaria — AGPL-3.0-or-later

Gratuita para siempre. Puede ejecutar, estudiar, modificar y
redistribuir OpenPanda bajo los términos de la AGPL. Las obligaciones
que importan en la práctica:

- **¿Distribuye una copia** (binaria o fuente)? → debe proporcionar el
  Código Fuente Correspondiente bajo AGPL.
- **¿La modifica y permite que usuarios interactúen por red** (OpenPanda
  es un demonio con consola Web/WebSocket — esto cuenta)? → debe
  ofrecer a esos usuarios el código fuente de su versión modificada
  (AGPL §13).
- ¿La ejecuta sin modificar en sus propias máquinas, para usted o su
  organización, incluso en una empresa? → **ninguna obligación**. El
  uso interno es siempre gratuito.

La AGPL también incluye una concesión expresa de patentes y una cláusula
de represalia (§10–11), y protege su libertad de instalar versiones
modificadas en el hardware que posee.

## Licencia comercial

Para situaciones en las que el cumplimiento de la AGPL no encaja, por
ejemplo:

- integrar OpenPanda en un producto o dispositivo de código cerrado;
- ofrecer un OpenPanda modificado/ampliado como servicio alojado sin
  publicar su código fuente;
- distribuir bajo su propio EULA, o necesitar compromisos de indemnidad,
  soporte o SLA;
- usar el nombre o las marcas «OpenPanda» para una oferta comercial.

**Contacto: fing2024@outlook.com**

Las condiciones comerciales se negocian por despliegue; no afectan a la
edición comunitaria, que permanece íntegramente bajo AGPL.

## Garantía y responsabilidad

La edición comunitaria se proporciona estrictamente «tal cual», sin
garantía y sin aceptación de responsabilidad — véase `TERMS.md`
(中文译本 `TERMS.zh-CN.md`), que se aplica a toda copia del Software en
cualquiera de las dos vías. Un acuerdo comercial firmado puede incluir
sus propios compromisos de garantía, indemnidad y SLA; cuando dicho
acuerdo guarda silencio, `TERMS.md` rige como referencia.

## Por qué este modelo

La AGPL mantiene la edición comunitaria genuinamente libre — las
mejoras nunca pueden privatizarse sin que quien las toma devuelva algo —
mientras la licencia comercial financia el desarrollo. La licencia dual
solo funciona porque el copyright está agregado: cada colaborador
externo firma `CLA.md`, otorgando al proyecto el derecho de ofrecer su
contribución bajo ambas vías.

## Preguntas frecuentes

**¿Puede una empresa usar OpenPanda internamente de forma gratuita?**
Sí. Meramente ejecutar el software — incluso en producción, incluso en
una empresa grande — no desencadena ningún deber de divulgación del
código fuente. Las obligaciones de la AGPL comienzan cuando *entrega*
copias u *ofrece acceso por red a una versión modificada*.

**Ejecutamos un nodo modificado en nuestra LAN. ¿Debemos publicar el
código?**
Solo si usuarios fuera de su organización interactúan con él por red.
Las modificaciones internas compartidas únicamente dentro de su
organización no tienen que publicarse (aunque entregar el software a
cualquiera sigue requiriendo darle *a esa persona* el código fuente).

**¿Usar un adaptador/driver de OpenPanda convierte mi código en AGPL?**
No. Los adaptadores de agentes se ejecutan como procesos separados que
hablan un protocolo stdio; la mera comunicación entre procesos no es una
obra derivada.

**¿Puedo bifurcar la última versión con licencia MIT?**
Sí — las versiones etiquetadas antes del cambio de licencia permanecen
bajo MIT y siempre lo estarán. Solo el desarrollo nuevo a partir del
commit de corte es AGPL/comercial.

**Soy colaborador: ¿qué pasa con mi código?**
Su copyright sigue siendo suyo. La CLA (`CLA.md`) permite al proyecto
publicar su trabajo tanto bajo términos AGPL como comerciales; la
edición comunitaria siempre permanece como software libre.
