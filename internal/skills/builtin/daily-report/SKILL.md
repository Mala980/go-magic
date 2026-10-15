---
name: daily-report
description: "Use when the user asks for a daily report, standup update, weekly summary, or progress recap, or when they want completed work, in-progress items and blockers collected from their task list into a written report."
version: 2.0.0
author: go-magic
tags: [report, daily, summary, productivity]
tools: [todo, session_search]
---

# Daily Report Skill

Turn the user's task list and recent conversations into a structured report — with real data, not plausible-sounding filler.

## When to Use

Load this skill when:
- The user asks for a daily report, standup update, or weekly summary
- Summarizing what was completed, what is in progress, and what is blocked
- Preparing a progress recap for a manager or a team channel

## Data Sources (read these first)

Do not write the report from memory. Pull the facts:

1. **Task list** — call the `todo` tool with `action: "list"` to get the user's items, each with `id`, `title`, `status` (`pending` / `in_progress` / `completed`) and `priority` (`high` / `medium` / `low`).
   - Completed items → "Accomplished"
   - `in_progress` → "In Progress"
   - `pending` with `high` priority → "Next" / "Blockers" candidates
2. **Recent conversations** — use `session_search` to recover what was actually worked on when the task list is thin (e.g. a long debugging session that never became a todo).
3. **The user's own message** — if they list things in the request, treat that as authoritative and merge it with the above.

If a section has no source data, write "no data" and say which source was empty. **Never invent deliverables, percentages, ticket numbers, or hours.** A short honest report beats a fabricated complete one.

## Report Templates

### Daily Standup
```
## Daily Standup - [date]

**Completed**:
- <from todo items with status=completed>
- ...

**In Progress**:
- <from todo items with status=in_progress>

**Blockers**:
- None
- or <what is waiting on what, and on whom>
```

### Daily Progress Report
```
## Daily Progress Report - [date]

### Accomplished
1. **<project/task>**: <what actually changed, with file/PR/commit reference if known>

### In Progress
- <task> (<status, or "no tracked estimate">)

### Blockers
- <blocker, or "None">

### Next
- [ ] <high-priority pending items, in priority order>

### Metrics
- Tasks completed: <count from todo tool>
- Tasks in progress: <count>
- (omit hours/meetings unless the user gave the numbers)
```

### Weekly Summary
```
## Weekly Summary - week of [date]

### High Priority
1. ...

### Accomplished
| Task | Status | Notes |
|------|--------|-------|
| ...  | Done   | ...   |

### Next Week
- [ ] ...

### Key Metrics
- Completed: <n>   In progress: <n>   Pending: <n>
```

## Writing Rules

- **Specific over vague**: name the artifact that changed, not "worked on feature X"
- **Progress must be sourced**: a percentage needs a stated basis; otherwise say "in progress"
- **Blockers are concrete**: "waiting on the API contract from <team>" not "blocked"
- **Concise**: bullet points; the reader scans, they do not read
- **Consistent dates**: use one date format throughout, and state the timezone when it matters

## Metrics That Are Safe to Report

- Counts by status (completed / in progress / pending) — from the `todo` tool
- Counts by priority — from the `todo` tool
- Items completed in the requested window, if the todo items carry timestamps

Anything else (hours worked, velocity points, meetings) must come from the user; do not estimate it.

## Worked Example

```
## Daily Progress Report - 2024-01-15

### Accomplished
1. **Auth refactor**: replaced session cookies with signed tokens (PR #123)
2. **Payment flow**: fixed the double-charge on retry (commit 4f2a1c)

### In Progress
- Search indexing: schema written, backfill not started

### Blockers
- Waiting on the staging DB credentials from the platform team

### Next
- [ ] Finish the search index backfill (high)

### Metrics
- Tasks completed: 2   In progress: 1   Pending: 3
```

## Integration

- `todo` (action `list`) — the primary source for completed / in-progress / pending items and their priorities
- `session_search` — recover context for work that never made it into the task list
