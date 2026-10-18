# OpenPanda Terms of Use and Disclaimer

**Translations (provided for convenience only; the English text governs):**
**[简体中文](TERMS.zh-CN.md) · [日本語](TERMS.ja.md) · [Español](TERMS.es.md) · [Deutsch](TERMS.de.md)**
**（各语言译本仅供参考；中英文及其他版本如有歧义，以英文版为准。）**

*Effective for all OpenPanda releases distributed on or after 2026-10-11.*

These Terms of Use and Disclaimer ("Terms") apply to the OpenPanda
software (the "Software") published by Xustalis (the "Licensor", "we"),
under either distribution track — the GNU Affero General Public License
v3.0 or later (`LICENSE`) or a negotiated commercial license
(`COMMERCIAL.md`).

**Contents:** 1. Acceptance · 2. What the Software does · 3. No warranty
· 4. Assumption of risk · 5. Third-party services · 6. Limitation of
liability · 7. Indemnification · 8. High-risk activities ·
9. Experimental features · 10. Privacy and data · 11. Lawful use; export
· 12. Trademarks · 13. Contributors · 14. No support obligation ·
15. Changes · 16. Governing law · 17. Miscellaneous

## 1. Acceptance; relationship to the licenses

By downloading, installing, copying, modifying, distributing, or using
the Software you accept these Terms. If you do not accept them, do not
use the Software. If you use the Software on behalf of an organization,
you represent that you are authorized to bind it, and "you" refers to
that organization. You must be at least the age of legal majority in
your jurisdiction to accept these Terms yourself; otherwise a parent,
legal guardian, or responsible organization must accept them on your
behalf and is responsible for your use.

Acceptance through the first-run or re-consent prompt is recorded on your
own device with a UTC timestamp (`terms_accepted_at` in the UI config)
and, where the deployment's store is open, an entry in its signed audit
chain; both records stay on your machines.

These Terms are conditions of use; they do not modify or replace the
license under which your copy is conveyed. For copies conveyed under the
AGPL, the warranty-disclaimer, liability-limitation, indemnification,
and trademark provisions below operate as Additional Terms of the kind
section 7 of that license expressly permits; to the extent any provision
of these Terms could nonetheless be read as a further restriction on the
permissions the AGPL grants, that provision is severed and the AGPL
controls. If you hold a signed commercial license agreement, its own
warranty, indemnity, and liability clauses apply; where that agreement
is silent, these Terms apply as the baseline.

## 2. What the Software does — please read this

OpenPanda is an orchestrator, not a sandbox. It coordinates AI agent
command-line tools and direct shell execution across one or more
machines joined into a peer-to-peer mesh. Depending on your
configuration it can:

- execute arbitrary shell commands; create, modify, and delete files;
  install packages; and otherwise change system state;
- run third-party agent CLIs (e.g. Claude Code, OpenAI Codex and
  others) that generate and carry out actions autonomously and may bill
  your API accounts;
- open network listeners, connect to peer nodes, and delegate tasks
  between machines;
- perform operations classified as tier-2 / irreversible after human
  approval;
- check for and apply updates to itself from a release channel.

The Software does these things **under your configuration, on hardware
you control, with your credentials, and at your direction**. You — not
the Licensor — operate the system and own its outcomes.

## 3. NO WARRANTY — the Software is provided "AS IS"

TO THE MAXIMUM EXTENT PERMITTED BY APPLICABLE LAW, THE SOFTWARE IS
PROVIDED "AS IS" AND "AS AVAILABLE", WITH ALL FAULTS AND WITHOUT
WARRANTY OR CONDITION OF ANY KIND. THE LICENSOR, ITS AFFILIATES, AND ALL
CONTRIBUTORS DISCLAIM ALL WARRANTIES AND CONDITIONS, EXPRESS, IMPLIED,
STATUTORY OR OTHERWISE, INCLUDING WITHOUT LIMITATION IMPLIED WARRANTIES
OR CONDITIONS OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE,
TITLE, NON-INFRINGEMENT, ACCURACY, QUIET ENJOYMENT, AND SYSTEM
INTEGRATION, AND ANY WARRANTY ARISING FROM COURSE OF DEALING, USAGE, OR
TRADE PRACTICE.

WITHOUT LIMITING THE FOREGOING, NO WARRANTY IS GIVEN THAT THE SOFTWARE
WILL MEET YOUR REQUIREMENTS; THAT IT WILL OPERATE UNINTERRUPTED, TIMELY,
SECURE, OR ERROR-FREE; THAT DEFECTS WILL BE CORRECTED; OR THAT IT IS
COMPATIBLE WITH ANY PARTICULAR HARDWARE, SOFTWARE, OR THIRD-PARTY
SERVICE. WHERE APPLICABLE LAW DOES NOT PERMIT THE EXCLUSION OF IMPLIED
WARRANTIES, THIS SECTION APPLIES TO THE GREATEST EXTENT PERMITTED AND
ANY UNAVOIDABLE WARRANTY IS LIMITED IN DURATION TO THE SHORTEST PERIOD
PERMITTED BY LAW.

THE SAME "AS IS" BASIS APPLIES TO DOCUMENTATION, EXAMPLE FILES, SAMPLE
CONFIGURATIONS, ADAPTER SCRIPTS, AND ANY OTHER MATERIALS DISTRIBUTED
WITH THE SOFTWARE.

## 4. Assumption of risk — autonomous operation

You understand, accept, and assume the risk that:

- **Commands and agent actions can be destructive, irreversible, or
  unsafe.** Output produced by AI models is probabilistic and may be
  wrong, harmful, insecure, or destructive. Configuring permission
  tiers, approval queues, sandboxing, and network allowlists, and
  supervising what the system executes, is your responsibility.
  Approval prompts and guardrails are workflow aids, not safety
  guarantees.
- **You are responsible for the mesh you build.** Every node trusts
  every peer holding the shared secret; a compromised node can
  compromise the whole mesh (see `SECURITY.md` / `SECURITY.en.md`).
  Key management, transport encryption, listener binding, and admission
  decisions are solely yours.
- **Costs are yours.** Agent CLIs, model APIs, bandwidth, compute, and
  other paid services invoked through the Software are billed to your
  accounts under your own agreements with those providers. Retries,
  delegation chains, and runaway agents can consume API quota without
  bound. The Licensor is not responsible for any charges incurred.
- **Data is yours.** Backups, snapshots, and the confidentiality of
  files, credentials, and personal data on your systems are your
  responsibility. Material routed to third-party agent providers is
  transmitted under your configuration and their terms — review their
  privacy policies before enabling such paths.
- **No professional advice.** Plans, analyses, answers, and code
  produced through the Software are not legal, medical, financial,
  safety, or other professional advice and are not represented to be
  correct or fit for any use.

## 5. Third-party services and components

The Software orchestrates, invokes, or interoperates with third-party
agent CLIs, model providers, package registries, and release channels
("Third-Party Services"). Third-Party Services are governed solely by
their own terms and policies. The Licensor does not control, endorse, or
assume any responsibility for them, their output, their availability,
changes to them, or for data you transmit to them, and is not liable for
acts or omissions of any Third-Party Service. Bundled third-party
components are listed in `THIRD_PARTY_NOTICES.md` and are licensed to
you under their own licenses, which govern those components.

## 6. LIMITATION OF LIABILITY

TO THE MAXIMUM EXTENT PERMITTED BY APPLICABLE LAW, IN NO EVENT WILL THE
LICENSOR, ITS AFFILIATES, OR ANY CONTRIBUTOR OR DISTRIBUTOR OF THE
SOFTWARE BE LIABLE TO YOU OR ANY THIRD PARTY FOR ANY INDIRECT,
INCIDENTAL, SPECIAL, CONSEQUENTIAL, EXEMPLARY, OR PUNITIVE DAMAGES, OR
FOR ANY LOSS OF PROFITS, REVENUE, DATA, GOODWILL, BUSINESS OPPORTUNITY,
OR ANTICIPATED SAVINGS, OR FOR THE COST OF SUBSTITUTE GOODS OR SERVICES,
ARISING OUT OF OR RELATING TO THE SOFTWARE OR THESE TERMS — INCLUDING
WITHOUT LIMITATION DAMAGES CAUSED BY COMMANDS OR ACTIONS EXECUTED BY THE
SOFTWARE OR BY AGENTS IT COORDINATES, DATA LOSS OR CORRUPTION, SECURITY
INCIDENTS OR UNAUTHORIZED ACCESS, NETWORK EXPOSURE, THIRD-PARTY SERVICE
FAILURES OR CHARGES, OR INTERRUPTION OF BUSINESS — HOWEVER CAUSED AND
UNDER ANY THEORY OF LIABILITY (CONTRACT, TORT, NEGLIGENCE, STRICT
LIABILITY, STATUTE, OR OTHERWISE), EVEN IF A PARTY HAS BEEN ADVISED OF
THE POSSIBILITY OF SUCH DAMAGES AND EVEN IF A REMEDY FAILS OF ITS
ESSENTIAL PURPOSE.

TO THE EXTENT LIABILITY CANNOT BE WHOLLY EXCLUDED, THE AGGREGATE
LIABILITY OF THE LICENSOR ARISING OUT OF OR RELATING TO THE SOFTWARE OR
THESE TERMS IS LIMITED TO THE AMOUNT YOU PAID THE LICENSOR FOR THE
SOFTWARE IN THE TWELVE MONTHS PRECEDING THE EVENT GIVING RISE TO THE
CLAIM — WHICH, FOR THE COMMUNITY EDITION, IS ZERO.

THIS SECTION DOES NOT EXCLUDE OR LIMIT LIABILITY THAT MANDATORY LAW DOES
NOT PERMIT TO BE EXCLUDED OR LIMITED, SUCH AS LIABILITY FOR INTENT OR
GROSS NEGLIGENCE, OR FOR DEATH OR PERSONAL INJURY, WHERE AND TO THE
EXTENT THE LAW PROHIBITS THE EXCLUSION.

ANY CLAIM OR CAUSE OF ACTION ARISING OUT OF OR RELATING TO THE SOFTWARE
OR THESE TERMS MUST BE BROUGHT WITHIN ONE (1) YEAR AFTER THE CLAIM
ACCRUES; AFTER THAT IT IS PERMANENTLY BARRED, EXCEPT WHERE NON-WAIVABLE
LAW REQUIRES A LONGER PERIOD.

## 7. Indemnification

To the maximum extent permitted by applicable law, you will indemnify,
defend, and hold harmless the Licensor and all contributors and
distributors of the Software from and against any and all claims,
damages, losses, liabilities, costs, and expenses (including reasonable
attorneys' fees) arising out of or relating to: (a) your use, operation,
or deployment of the Software; (b) actions executed on systems under
your control by the Software or by agents it coordinates; (c) your
modification or redistribution of the Software; (d) your breach of these
Terms or of any applicable license or third-party terms; or (e) your
violation of applicable law or of any third party's rights.

## 8. High-risk activities

The Software is not designed, certified, or warranted for environments
where its failure or malfunction could lead to death, personal injury,
or severe environmental or property damage — including life-support
systems, weapons systems, emergency communications, or safety-critical
control of aircraft, vehicles, or industrial facilities. If you choose
to deploy it in such contexts you do so entirely at your own risk, and
the indemnification in Section 7 applies to resulting claims.

## 9. Experimental features

Features documented as experimental or preview (for example the
`panda rpc` embedding surface, subscription OAuth, and voice) are
provided for evaluation only; they may be incomplete, change
incompatibly, or be removed in any release. All of these Terms apply to
them in full — assume they will break.

## 10. Privacy and data handling

The Software is local-first: it sends no telemetry to the Licensor and
transmits your code, prompts, and files only where your own
configuration routes them (for example to third-party agent providers
or release/update channels such as GitHub, each governed by its own
privacy policy). Any personal data processed through your deployment is
processed by you as controller; compliance with data-protection law on
your systems is your responsibility.

## 11. Lawful use; export control

You may use the Software only in compliance with applicable law. You may
not use it to violate others' rights, to conduct unlawful surveillance
or intrusion, to create or distribute malware, or for any unlawful
purpose. You represent that you are not located in a comprehensively
embargoed territory, are not on any applicable sanctions or restricted-
party list, and will not export, re-export, or transfer the Software in
violation of applicable export-control or sanctions law.

## 12. Trademarks

"OpenPanda" and associated marks are trademarks of the Licensor. Neither
these Terms nor the AGPL grants any trademark license: you may not use
the name or marks to brand derivative products or services, or to imply
endorsement, without written permission. See `NOTICE`.

## 13. Contributors

Every disclaimer, limitation of liability, and protection in these Terms
extends to each individual contributor to and distributor of the
Software. To the fullest extent permitted by law, no contributor is
personally liable for anything covered by Sections 3 through 7.

## 14. No obligation to maintain or support

The Licensor has no obligation to provide updates, fixes, maintenance,
or support, or to continue developing or distributing the Software.
Releases may be added, changed, or withdrawn at any time. Support,
indemnity in your favor, or SLA commitments exist only under a signed
commercial agreement.

## 15. Changes to these Terms

The Licensor may revise these Terms from time to time. A revision takes
effect for releases published after it; the Terms shipped with a given
release continue to govern that release. The in-application consent card
is a summary only — in case of discrepancy, `LICENSE` and these Terms
prevail.

## 16. Governing law and venue

These Terms, and any dispute arising out of or relating to them or to
the Software, are governed by the laws of the jurisdiction in which the
Licensor is domiciled or principally established, without regard to its
conflict-of-laws rules. The courts located in that jurisdiction have
exclusive jurisdiction over such disputes, except that a party may seek
injunctive or other provisional relief for misuse of intellectual
property in any court of competent jurisdiction, and except where
mandatory consumer-protection law grants you a different forum. The
United Nations Convention on Contracts for the International Sale of
Goods does not apply.

## 17. Miscellaneous

- **Severability.** If any provision of these Terms is held
  unenforceable, it is enforced to the maximum permissible extent and
  the remainder stays in force.
- **No waiver.** A failure to enforce any provision is not a waiver of
  it.
- **Survival.** Sections 3 through 7 and 12 through 17 survive any
  termination of your use of or rights in the Software.
- **No agency.** Nothing in these Terms or in your use of the Software
  creates a partnership, joint venture, employment, or agency
  relationship between you and the Licensor.
- **Assignment.** The Licensor may assign or transfer these Terms in
  connection with a reorganization, sale of assets, or transfer of the
  project. You may not assign your rights or obligations under them
  without the Licensor's prior written consent; any purported assignment
  without consent is void.
- **Force majeure.** The Licensor is not liable for any failure or delay
  resulting from causes beyond its reasonable control.
- **Notices.** The Licensor may give notice concerning the Software and
  these Terms by posting in the project repository, on the project
  website, or in release notes; such notice is effective when posted.
- **Headings.** Section headings are for convenience only and do not
  affect interpretation.
- **Entire agreement.** Together with the license under which your copy
  is conveyed, these Terms are the entire agreement between you and the
  Licensor concerning use of the Software, superseding prior
  understandings on that subject.
- **Language.** These Terms are published in English, Chinese, Japanese,
  Spanish, and German; the English version governs.
- **Contact.** Xustalis — fing2024@outlook.com.
