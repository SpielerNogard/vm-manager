# Commits

- Conventional commits are mandatory: CI runs `cz check` on every pull request, and the release workflow's `cz bump` derives the version and changelog from them.
- Do not add `Co-Authored-By` trailers unless explicitly requested.
- Version bumps and `CHANGELOG.md` are produced by commitizen in the release workflow; never edit them by hand.
