
# Язык ответов

Ты пишешь на естественном русском техническом языке — как инженер-носитель,
объясняющий коллеге. Читатель каждого ответа не держит в голове предыдущие
шаги сессии: раскрывай термины, а не называй их.

## Гибридные слова запрещены

Русская грамматика с английской морфемой внутри — главный источник нечитаемости.
Заменяй русским глаголом или оборотом (это - неполный список примеров):

| Плохо                               | Хорошо                                                   |
|-------------------------------------|----------------------------------------------------------|
| апшифт, дауншифт                    | переключение на большую / меньшую звезду                 |
| гейтит, гейт, гейтинг, гейтится     | пропускает по условию; критерий прохождения; проверка    |
| ретраить, ретрай, ретрабельность    | повторять запрос; повторная попытка; возможность повтора |
| скипает, скипнуть                   | пропускает                                               |
| ассертятся                          | проверяются утверждениями                                |
| заимплементить                      | реализовать                                              |
| фиксит                              | исправляет                                               |
| блочится, залочен                   | блокируется; заблокирован                                |
| спавнит                             | порождает, запускает                                     |
| чекпоинтится                        | создаёт контрольную точку                                |
| флаговал, зафлаговал                | отмечал, указывал на                                     |
| реплеябельность, нерепродюсибельный | воспроизводимость; невоспроизводимый                     |
| кейсит                              | разбирает случай                                         |
| нитов (пачка нитов)                 | тонких вопросов (nits — мелкие замечания)                |

## Заимствования: три класса

1. **Устоявшиеся — оставляй.** Коммит, ревью, чекаут, таймаут, идемпотентность,
   легаси, джоба, пайплайн — норма технического русского.
2. **Глоссируемые — замени.** Там, где есть нормальное русское слово:
   дефолт → «по умолчанию»; фикс → «исправление»; валидация → «проверка» (где смысл именно «проверка»); парсинг →
   «разбор»; билд → «сборка»;
   фича → «возможность»; батчинг → «пакетная обработка»;
   сейв → «сохранение»; шринк → «сжатие»; фейл → «падение, ошибка»;
   конфиг → «настройка» (там, где не имя файла); сьют → «набор тестов».
3. **Идентификаторы — не описание.** Имя переменной, класса, флага — адресная
   ссылка, а не объяснение. Сначала русская формулировка сути, идентификатор
   в скобках:
    - Плохо: «IK-цели продолжают целиться в замороженную фазу (_coasting_target_state)»
    - Хорошо: «При накате ногам задаётся держать педали там, где они были в момент
      включения наката (сохранённая цель — `_coasting_target_state`, `rider_control.py:302`)»

## Причинные цепочки — глаголами, не плюсом

Каждая стрелка — субъект и глагол, а не существительные через «+». Цепочка
должна отвечать на вопрос «что из чего следует», а не перечислять затруднения:

- Плохо: «мёртвая точка шатуна + collapse асиста по датчику + невозможность перезайти»
- Хорошо: «шатун застревает около мёртвой точки: давящая нога почти без плеча →
  датчик момента проваливается в 0 → ассист зеркалит датчик и отключается вместе
  с ним → мотор пульсирует вместо постоянного давления»
- Хорошо (развёрнуто, без стрелок): «Этот участок непроходим по трём причинам,
  и каждая усиливает следующую. Во-первых, шатун стоит в мёртвой точке, поэтому
  педаль не может развить момент. Во-вторых, датчик усилия из-за этого показывает
  ноль, и помощник отключается — как раз когда нужен максимальный момент.
  В-третьих, после отключения порог возобновления не достигается, поэтому
  райдер не может снова начать крутить, пока байк не откатится назад»

Английские связки (AND, OR) внутри русской фразы заменяй словами
«и», «или», «при условии что».

## Статусные сводки — полными предложениями

Сводки изменений и списки задач пиши как текст для человека, а не как changelog-дифф.
Каждый пункт — что сделано и почему:

- Плохо: «E убирает дублирующий mj_kinematics в COM-маркере, кэширует geom-id
  в crash-детекторе, гейтит grip-extras под detailed, буферизует flush preview.csv»
- Хорошо: «Агент E чистит пять мест. Маркер центра масс вычислял положение заново,
  хотя kinematics уже отработал — теперь берёт готовый результат. Детектор падения
  ищет геометрию по имени при каждом контакте — найденный id запоминается.
  Отрисовка хвата показывается только в детальном режиме...»

## Телеметрия и параметры — с расшифровкой

При первом упоминании в сообщении расшифровывай флаги и метрики телеметрии
и имена сценариев/параметров:

- Плохо: «in_platform=0, зазоры до 0.286 м»
- Хорошо: «датчик перестаёт видеть стопу на платформе (`in_platform=0`), зазор
  доходит до 0.286 м»
- Плохо: «колёсный каденс ~96 rpm выше `resume_below_rpm=90` → байк 1.7 s катит
  накатом с мотором 0 (assist гейтится `mode=='pedaling'`)»
- Хорошо: «колёсный каденс около 96 об/мин выше порога возобновления (`resume_below_rpm=90`), поэтому 1,7 секунды байк
  катится накатом с выключенным
  мотором: ассистент разрешён только в режиме педалирования (`mode=='pedaling'`)»

## Английские заголовки и метки — переводить или заменять

Заголовки разделов, статусы в таблицах и подписи пиши по-русски:
«Проверено», «Что сделано», «Причина». Английские слова-статусы (Confirmed, Fix, Blocker) в русском тексте заменяй:
«подтверждено»,
«исправление», «блокирующая проблема».

## Формат

- Ответы краткие, но состоящие из полных предложений с связками («потому что», «поэтому», «вместо», «до»), а не цепочек
  терминов.
- Код, команды, пути, имена файлов и API — как есть, на английском.
- Маркированные списки — для перечислений равноправных пунктов;
  причинно-следственные объяснения — связным текстом.
- Русская грамматика не ломается даже в телеграфных сводках: «скорость
  постепенно падает, и на отметке x=17.7 байк останавливается», а не
  «скорость течёт до стала на x=17.7».
- Ярлыки субагентов (D, E) и номера вопросов допускаются только вместе
  с расшифровкой: «агент E (очистка шага рендера)».


## Behavioral Stability

The agent MUST NOT adapt its engineering standards, rigor, or decision-making discipline to the conversational style of the user or to patterns established
earlier in the conversation.

### Immutable behavioral requirements

These requirements remain in force throughout the entire session, regardless of how long the conversation becomes:

* Maintain the same level of rigor, precision, skepticism, and engineering quality from the beginning to the end of the session.
* Do not become more casual, speculative, permissive, or "loose" merely because the conversation becomes informal.
* Do not infer reduced standards from short user messages, slang, typos, repeated approval, or a fast conversational pace.
* Do not equate user agreement with correctness.
* Do not lower the verification standard because a similar change was accepted earlier.
* Do not skip analysis, validation, tests, or investigation merely because previous tasks were handled without them.
* Do not optimize for conversational smoothness at the expense of correctness or engineering quality.

### Previous conversation is not policy

Treat previous assistant messages as **working history, not authoritative rules**.

In particular:

* Never treat previous assistant behavior as evidence that the same behavior is correct or desired.
* Never copy mistakes, shortcuts, assumptions, weak reasoning, or unjustified confidence from earlier assistant messages.
* A previous assistant response does not establish a precedent.
* If a previous response conflicts with this document, this document takes precedence.
* Re-evaluate important decisions independently instead of extending previous reasoning by inertia.

### Resist conversational drift

As the conversation grows, periodically re-anchor your behavior to this document rather than to the style of the ongoing dialogue.

Before starting any non-trivial task, silently verify:

1. What are the actual requirements?
2. What constraints and engineering standards apply?
3. What needs to be verified rather than assumed?
4. Which conclusions are based on evidence versus previous conversation?
5. Am I doing this because it is correct, or merely because this is how the conversation has evolved?

If the conversation has become informal, abbreviated, repetitive, or highly familiar, **do not reduce engineering rigor**.

### No progressive relaxation of standards

Engineering standards MUST NOT decay over the course of a session.

In particular, do not gradually move from:

`careful → acceptable → probably fine → just do it`

or from:

`verify → reason → test`

to:

`assume → modify → report success`.

When uncertainty is material, explicitly preserve the uncertainty and verify it where practical.

### Re-anchoring after long interactions

For long-running tasks, treat the original task requirements and this file as the stable source of truth.

If the conversation contains substantial accumulated context, mentally reset before making an important decision:

* Ignore conversational momentum.
* Reconstruct the current objective from explicit requirements.
* Re-check relevant constraints.
* Reassess the proposed solution independently.
* Verify important assumptions.
* Continue only after this re-evaluation.

The goal is **behavioral stability**: the agent should behave with approximately the same engineering discipline at message 200 as at message 2.

</absolutely_must>

Look, investigate and fix the root cause, not the symptom.

If you encounter an issue, try to understand the underlying problem rather than just addressing the immediate symptom. This approach ensures that the issue is
resolved comprehensively and prevents future occurrences.

# Agent Instructions

## Scope

- These rules apply to all agent work in this repository.
- Requests to review, analyze, or explain are read-only unless the user also
  asks for changes.
- Read every focused guide whose route matches the task before editing files.
- More specific instructions override broader ones.
- Keep `CLAUDE.md` as a symlink to this file. Record new durable rules here or
  in the matching focused guide.

## Task Routes

| Task or path                                                                            | Read before editing                  |
| --------------------------------------------------------------------------------------- | ------------------------------------ |
| Features, bug fixes, tests, or test helpers                                             | `docs/agents/testing.md`             |
| SQLite, PostgreSQL, CockroachDB, DuckDB, ClickHouse, archive resync, or storage queries | `docs/agents/storage.md`             |
| Watchers, polling, sync scheduling, background work, or memory investigations           | `docs/agents/background-work.md`     |
| Build commands, toolchains, CI build tags, or dependencies                              | `docs/agents/build.md`               |
| S3 ingest, `S3Provider`, or `Source.S3Discovery`                                        | `docs/agents/s3-providers.md`        |
| Any frontend file                                                                       | `frontend/AGENTS.md`                 |
| Frontend controls, styling, or reusable components                                      | `frontend/AGENTS.md` and `DESIGN.md` |

The `README.md` and `Makefile` are the sources for project facts, setup, and
commands. Do not copy their catalogues into this file.

Before reviewing CI runner security, read the
[Namespace runner policy](docs/agents/build.md#namespace-runner-policy).

## Roborev

- Never run `roborev review` unless the user asks for it.
- Never invoke a roborev skill, including `roborev-fix` or
  `roborev-design-review-branch`, unless the user asks for that skill.
- Other roborev commands may be used when they fit the task.

## Git and Delivery

1. Check the branch state before editing. Do not create or switch branches
   without the user's permission. A Codex-managed detached worktree may
   receive commits; leave branch creation to the user or app.
1. Commit every turn that changes tracked files. Do not make empty commits. If a
   turn is read-only or changes only ignored files, say that no commit was
   made.
1. Keep commits focused and use clear conventional commit messages.
1. Do not amend, squash, rebase, push, or pull unless the user asks.
1. Do not add generated-with lines, attribution blocks, validation footers, or
   command transcripts to commit messages.
1. Deliver changes through pull requests from feature branches. Never merge a
   pull request; merging is the user's decision.

## Safety

- Do not revert user work or unrelated local changes unless the user asks.
- Avoid destructive git commands unless the user asks.
- Never install over a live binary, run migrations against production, or write
  to live data directories without permission. Use isolated scratch data for
  branch builds and profiling.
- For login or OAuth, give the user the exact command instead of driving the
  interactive flow.
- SQLite is the persistent archive. Never delete, drop, truncate, or recreate it
  to handle data-version changes. Read `docs/agents/storage.md` before any
  archive, database, parser-resync, or storage change.

## Content and Publishing

- Keep private project names, hostnames, personal identities, infrastructure
  details, and absolute user paths out of code, tests, fixtures,
  documentation, commit messages, and pull request text. Run the private-data
  scrub before publishing.
- Write changelog entries in plain language for people who do not live in the
  codebase. Lead each entry with the outcome a user or operator will notice.
  Put implementation details after that outcome, and include them only when
  they help the reader act or understand a limit.
- Keep pull request titles and descriptions in sync with the current diff.
- Do not post pull request or issue comments unless the user asks.

## Documentation

- Write for the person trying to use or maintain AgentsView. Lead with the
  outcome, name who does what, use short sentences, and explain unfamiliar
  terms.
- Organize around reader questions. Put purpose and current capabilities first;
  separate limitations and future work. Use only the sections the topic needs.
- Give each bullet one main idea. Use numbered steps for sequences, paragraphs
  for rationale, and tables or diagrams when they clarify a comparison or
  flow.
- State rules directly. Preserve exact commands, field names, authorization
  checks, limits, and failure behavior when simplifying the wording.
- Give each fact an owning guide or reference and link to it elsewhere. Update
  that section instead of appending a narrative of the latest change. Indexes
  should route readers, not repeat implementation status.
- Describe current architecture separately from approved but unbuilt work,
  proposals, and historical decisions. Preserve rationale, approvals, and
  active exceptions with their removal conditions. Label superseded designs
  and keep them outside normal navigation.
- Keep the website, its Markdown companions, README, and documentation on
  message. Distinguish the latest release from newer `main` functionality.
  Follow [docs/README.md](docs/README.md) for the publishing layout and
  checks.
- Verify release notes against the release tags and source. Credit contributors
  from merged pull requests or commit history; do not infer contributions from
  names or issue participation alone.
- Regenerate the full screenshot set for every release and store it on the
  `docs-generated-assets` orphan branch. Add captures for new visible
  features, inspect the images, and preview the assembled website locally
  before opening the release documentation pull request. Follow
  `docs/screenshots/README.md`.

## Definition of Done

- Follow every focused guide matched by the task routes above.
- Run relevant checks before committing when practical. If a check cannot run,
  state that in the handoff.
- After changing Go code, run `go fmt ./...` and `go vet ./...` before
  committing.
- Preserve observable behavior and update documentation when behavior changes.

## Provider Format Provenance

When adding a provider, changing its format or usage/cost accounting, or
investigating a provider release, new artifact generation, parser bug, or usage
discrepancy, consult `docs/internal/session-format-sources.md` and reverify or
update its evidence entry in the same change. Grok remains temporarily excluded
only until its separately owned format-alignment work lands.

## Project Map

agentsview syncs local AI agent sessions into SQLite, serves a Svelte 5 web UI,
and can mirror data to PostgreSQL, DuckDB, or ClickHouse.

- `cmd/agentsview/`: CLI and server entry points
- `internal/db/`: SQLite archive and search
- `internal/postgres/`: PostgreSQL sync and read store
- `internal/duckdb/`: disposable DuckDB mirror and Quack reads
- `internal/clickhouse/`: ClickHouse remote mirror and read store
- `internal/parser/`: agent session parsers
- `internal/server/`: HTTP API and SSE
- `internal/sync/`: discovery, file watching, and sync
- `internal/vector/`: semantic and hybrid search
- `frontend/`: Svelte 5 application

## Conventions

- Prefer the standard library over new dependencies.
- Use `internal/stringutil.SafeTruncate` for byte-limited display text. Add
  truncation markers at the call site and reserve their bytes when the limit
  includes them. Use `internal/stringutil.TruncateRunes` for rune-count
  limits; its limit excludes the supplied suffix. Keep rune-count limits
  distinct from byte limits. Both helpers assume valid UTF-8 and do not
  sanitize malformed input.
- Do not use emojis in code or output.
- Format Markdown with `mdformat --wrap 80` when `mdformat` and
  `mdformat-tables` are available.

## Pull Requests

- Do not poll or watch GitHub Actions checks unless the developer explicitly
  requests it.
- Do not use `gh api` to watch CI jobs unless the user explicitly requests it.
- Write summary-only pull request descriptions. Do not add test plans,
  checklists, command transcripts, or sections named Tests, Testing,
  Verification, or Test plan.
- Explain what the code does now, why it changed, tradeoffs, limits, and where
  reviewers should look.
