# Maintenance

## Documentation

Treat docs as code that decays. When you touch a module, also look at the docs
that describe it — the repository's own doc locations and any docstring on what
you're editing.

If you spot a section that no longer matches reality — wrong command, renamed
file, removed flag, stale architecture description, broken docstring
`Examples` block — **flag it explicitly and fix it in the same change**. Don't
silently leave incorrect docs behind because "it's outside the ticket scope".

## Code

The same applies to code, with a narrower scope. When a line you are changing
anyway violates one of the conventions this repository imports, fix it and say
that you did — a banner comment or an `assert` in production code, for example,
or a missing type hint on a test parameter.

Structural changes are always reported, never performed — splitting a monolithic
test file into one file per function, renaming a module, moving code between
modules — as is anything else larger than the lines you are already touching.
Name it, and agree the scope before changing it. Surgical changes remain the
default; this is a bounded exception to it, not a licence to refactor adjacent
code.
