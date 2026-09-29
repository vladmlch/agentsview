# Fix Codex git_branch omission and state_5.sqlite title discovery

Type: task
Status: open
Blocked by: 

## Question

How should AgentsView update `codex.go` and `codex_metadata.go` to:
1. Assign `sess.GitBranch = b.gitBranch` in `codexSessionBuilder` so that git branch context extracted from `session_meta` is preserved in the database?
2. Read session titles from modern Codex's `.codex/state_5.sqlite` (`threads` table, 2,043 titles) in addition to legacy `session_index.jsonl` (826 lines), populating titles for 1,420 untitled sessions?
3. Gracefully stream-decode multiline pretty-printed JSON rollouts that fail the single-line JSONL reader?
