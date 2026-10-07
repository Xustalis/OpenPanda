# OpenPanda Licensing: Community & Commercial

OpenPanda is dual-licensed. The same codebase is offered under the
GNU Affero General Public License v3 or later (AGPL-3.0-or-later) for
open-source use, and under a separate commercial license for cases the
AGPL does not cover.

## Community license — AGPL-3.0-or-later

Free forever. You may run, study, modify and redistribute OpenPanda
under the AGPL terms. The obligations that matter in practice:

- **Distribute a copy** (binary or source)? → provide the Corresponding
  Source under AGPL.
- **Modify it and let users interact over a network** (OpenPanda is a
  daemon with WebSocket/web console — this counts)? → offer those users
  the source of your modified version (AGPL §13).
- Run it unmodified on your own machines, for yourself or your
  organization, including in a business? → **no obligations**. Internal
  use is always free.

The AGPL also carries an express patent grant and retaliation clause
(§10–11) and protects your freedom to install modified versions on
hardware you own.

## Commercial license

For situations where AGPL compliance does not fit, e.g.:

- embedding OpenPanda into a closed-source product or appliance;
- offering a modified/extended OpenPanda as a hosted service without
  publishing your source;
- distributing under your own EULA, or needing indemnity, support or
  SLA commitments;
- using the "OpenPanda" name/marks for a commercial offering.

**Contact: fing2024@outlook.com**

Commercial terms are negotiated per deployment; they do not affect the
community edition, which remains fully AGPL.

## Why this model

The AGPL keeps the community edition genuinely free — improvements can
never be taken private without the taker giving back — while the
commercial license funds development. Dual licensing only works because
the copyright is aggregated: every external contributor signs `CLA.md`,
granting the project the right to offer their contribution under both
tracks.

## FAQ

**Can a company use OpenPanda internally for free?**
Yes. Merely running the software — even in production, even in a large
company — triggers no source-disclosure duty. AGPL obligations start
when you *convey* copies or *offer network access to a modified
version*.

**We run a modified node on our LAN. Must we publish source?**
Only if users outside your organization interact with it over a
network. Internal-facing modifications shared only inside your
organization do not have to be published (though conveying the software
to anyone still requires giving *them* the source).

**Does using an OpenPanda adapter/driver make my code AGPL?**
No. Agent adapters run as separate processes speaking a stdio wire
protocol; mere inter-process communication is not a derivative work.

**Can I fork the last MIT-licensed release?**
Yes — versions tagged before the license change remain under MIT and
always will. Only new development from the cutover commit forward is
AGPL/commercial.

**I'm a contributor — what happens to my code?**
Your copyright stays yours. The CLA (`CLA.md`) lets the project ship
your work under both the AGPL and commercial terms; the community
edition always remains free software.
