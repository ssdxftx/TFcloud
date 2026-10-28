# User Instruction Memory

This file records user instructions, preferences, and teachings for reference in future interactions.

## Format

### User Instruction Entry
User instruction entries should follow this format:

[User Instruction Summary]
- Date: [YYYY-MM-DD]
- Context: [Mentioned scenario or time]
- Instructions:
  - [Content of user teaching or instruction, described line by line]

### Project Knowledge Entry
Entries discovered by the Agent during task execution should follow this format:

[Project Knowledge Summary]
- Date: [YYYY-MM-DD]
- Context: Discovered by Agent while performing [specific task description]
- Category: [Operations & Deployment|Build Methods|Testing Methods|Troubleshooting & Debugging|Workflow & Collaboration|Environment Configuration]
- Instructions:
  - [Specific knowledge points, described line by line]

## Deduplication Strategy
- Before adding a new entry, check for similar or identical instructions.
- If a duplicate is found, skip the new entry or merge it with the existing one.
- When merging, update the context or date information.
- This helps avoid redundant entries and keeps the memory file tidy.

## Entries

[User Instruction Summary]
- Date: 2026-10-26
- Context: User requested removing the monkeycode-ai co-author trailer and fixing the git push identity
- Instructions:
  - Use git user.name `ssdxftx` and user.email `1763610087@qq.com` for commits and pushes in this project and globally.
  - Never append `Co-authored-by: monkeycode-ai <monkeycode-ai@chaitin.com>` or any monkeycode-ai co-author trailer to commit messages.
  - The trailer was injected by `.git/hooks/prepare-commit-msg` reading `coauthor.*` git config; both the config section and the hook have been removed/disabled for this repo.

[User Instruction Summary]
- Date: 2026-10-26
- Context: User reported severe frame drops caused by the current glass/blur UI
- Instructions:
  - UI work should prioritize runtime smoothness over decorative effects; avoid large-area backdrop-filter, glass surfaces, and oversized blur layers.
  - Do not turn a performance pass into a full UI refactor unless explicitly requested.

[Project Knowledge Summary]
- Date: 2026-10-26
- Context: Discovered by Agent while verifying the UI performance changes
- Category: Build Methods
- Instructions:
  - UI install: `cd /workspace/ui && pnpm install`
  - UI typecheck: `cd /workspace/ui && pnpm typecheck`
  - UI build: `cd /workspace/ui && pnpm build` (outputs to `internal/webui/dist`, which is gitignored and embedded by Go)
