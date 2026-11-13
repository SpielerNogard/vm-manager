## Documentation

Treat docs as living code that decays. When a change touches behaviour, a command, a config key, a provider or the install path, update the docs that describe it (`README.md`, `docs/*.md`, example configs, doc comments) in the same change.

If you come across a section that no longer matches reality (wrong command, renamed file, removed flag, stale platform or architecture description), say so explicitly and fix it in the same change. Never leave incorrect docs behind because they are "outside the ticket scope".

## Testing on real machines

Machine access for tests on real hardware (Linux test VMs, a second Mac) lives outside the repo, so it never ends up in git history but every worktree sees it:

@~/.claude/includes/vm-manager-machines.md
