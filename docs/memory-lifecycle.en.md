# Long-term memory and the memory graph

[中文](memory-lifecycle.md)

The graph shows retained long-term facts and the files that own them. A name needs only one confirmed mention. Daily history, task output, skill descriptions and dream-report prose do not create knowledge nodes.

## Admission and categories

A lasting memory is a confirmed fact, stable preference or reusable convention that should remain useful in later conversations. Keep each assertion atomic and preserve its user, device or project scope. Guesses, examples, temporary instructions and one-time outcomes do not qualify.

| Category | Content | Target | Example stable key |
| --- | --- | --- | --- |
| identity | Name, occupation | user / USER.md | user.name |
| preference | Language, theme, response style | user / USER.md | preference.language |
| personal | Residence, time zone, lasting personal needs | user / USER.md | user.timezone |
| environment | Reusable device and environment facts | memory / MEMORY.md | environment.os |
| convention | Lasting global conventions and corrections | memory / MEMORY.md | logging.format |
| project | Architecture, build/test commands, project conventions | project / project MEMORY.md | test.command |

Do not retain today's requirements, schedules, weather, todos, task success/failure summaries, raw output, questions, uncertain guesses or irrelevant text as long-term facts. Existing redaction rules check credentials. An explicit request to remember something does not make temporary information durable.

The entry model classifies facts using conversational context. Go validates the category, key, scope and explicit transient-content markers again. Common English/Chinese identity, preference and environment assertions can be inferred; other assertions require category and key. These conservative heuristics are not full natural-language understanding: the model must still extract only confirmed, atomic facts.

## Updates and legacy data

memory_add upserts a stable attribute. Keys describe the attribute independently of its current value: changing preference.language updates the same fact. Common attributes such as name, language and theme use canonical keys across English and Chinese phrasing. Supply separate keys for independent devices, services or preferences, such as environment.server_a.os and preference.editor.

For an unclassified legacy entry, use memory_read, then memory_replace with a unique text match and category/key. Ambiguous or empty matches fail. A personal fact update reconciles same-key copies across USER.md, MEMORY.md and topic files under one store lock, moving it into its canonical layer. Validation or capacity failures preserve existing data. Project facts update only within their owning project. Coexisting values need separate keys, for example independent allergens.

Reclassifying a legacy entry or changing its attribute key also removes copies of the replaced attribute. Ordinary cross-file write failures restore the original file bytes; this is not a cross-file transaction across power loss or process crashes.

Files remain §-separated. New entries include readable metadata:

    [fact:identity:user.name] My name is Alice
    §
    [fact:preference:preference.language] User prefers English answers

Web and CLI editors can still maintain legacy prose. The graph projects only admitted entries without deleting source files. Unclear legacy entries need explicit category/key before appearing. Node identity comes from the owning file and attribute key, so value changes update the existing node. Word frequency and co-occurrence do not infer relationships.

## Daily history, dreams and projects

Daily logs record operational history, deduplicate identical same-day content, ignore empty entries and preserve provenance. Task titles and model-generated summaries are external evidence. Rephrasing task output with a model does not turn it into a trusted observation.

Dreaming still runs while idle, at most once a day. Durable admission precedes recurrence, ranking and provenance gates. Future-dated logs are ignored. A newer conflicting observation suppresses an older value even if the older one has more repetitions; same-day conflicts require an explicit correction. Existing retained assertions take precedence, so old logs cannot undo a later user edit. Profile facts go to USER.md; environment facts and global conventions go to MEMORY.md. Only qualifying trusted observations may promote. Repeated external task history cannot open that channel; a sweep may legitimately add nothing.

DREAMS.md retains consolidation receipts and the existing failure-recovery behavior. Promotion edges require both a receipt and a currently retained qualifying fact. Updated or forgotten assertions lose obsolete promotion links.

Project knowledge lives in projects/<name>/MEMORY.md. Project tasks and summaries live in projects/<name>/daily/ and never feed personal dreaming. Project tools cannot access personal memory or another project's memory. Both personal and project daily logs use the existing retention windows: archive after 30 days and delete after 90 days, enforced by the background scheduler.

Renaming a project carries its long-term memory, daily history and scoped skills together. Project history writers share the outer store lock with rename and delete operations so an in-flight history write finishes before the directory moves.

A destination conflict or file-move failure preserves the project name and active pointer. A metadata failure restores the original memory directory. This coordinates memory files and project metadata; task and session associations are still updated by their own stores, without a crash transaction across all stores. Removing a project fact also clears old copies of that attribute within the project.

The console uses the existing theme and style tokens, explains each memory surface, and displays fact categories and sources. The graph supports refresh, search and keyboard selection; project switching ignores late responses from earlier requests. On narrow screens the detail card follows the graph, and touch controls remain visible.
