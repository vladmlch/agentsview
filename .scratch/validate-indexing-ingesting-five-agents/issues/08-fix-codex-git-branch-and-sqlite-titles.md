# Fix Codex git_branch omission and state_5.sqlite title discovery

Type: task
Status: resolved
Blocked by: 

## Question

How should AgentsView update `codex.go` and `codex_metadata.go` to:
1. Assign `sess.GitBranch = b.gitBranch` in `codexSessionBuilder` so that git branch context extracted from `session_meta` is preserved in the database?
2. Read session titles from modern Codex's `.codex/state_5.sqlite` (`threads` table, 2,043 titles) in addition to legacy `session_index.jsonl` (826 lines), populating titles for 1,420 untitled sessions?
3. Gracefully stream-decode multiline pretty-printed JSON rollouts that fail the single-line JSONL reader?

## Answer

1. **Git Branch Preservation**: `codexSessionBuilder` now records `b.gitBranch = branch` in `handleSessionMeta` and populates `GitBranch: b.gitBranch` on the final `ParsedSession`. The cursor checkpointing logic in `codex_cursor.go` also tracks, clones (`strings.Clone`), and sizes `gitBranch` in memory estimation.
2. **SQLite Thread Titles**: `codex_metadata.go` now scans for `.codex/state_*.sqlite` and `.codex/state_*.db`, reading the `threads` table to populate session titles on top of legacy `session_index.jsonl`. `WatchPlan` monitors `state_*.sqlite` / `state_*.db` (including WAL siblings), and `SourcesForChangedPath` invalidates and rescans metadata accordingly.
3. **Multiline JSON Rollouts**: `parseCodexSessionSnapshotStreaming` now catches line-reader failures and retries via a streaming multiline JSON decoder with `sink.Reset()`, and `readCodexSessionMetaHeader` scans the entire JSON array/stream for `session_meta`.
