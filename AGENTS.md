# AGENTS.md

## Commands, flags, and help text

Before adding or changing a `depot` command, flag, help text (`Use`, `Short`, `Long`, `Example`), or command output,
read [docs/cli-style-guide.md](docs/cli-style-guide.md) in full, from start to end, and follow it. Don't search or skim
it for individual rules.

Product and feature names in help text and output follow `PRODUCT_NAMES.md` in the depot/app repository. Read it in
full from a depot/app checkout, or with
`gh api repos/depot/app/contents/PRODUCT_NAMES.md -H 'Accept: application/vnd.github.raw'`. If you can't read it, say
so before naming products. Don't mention unreleased products or features in help text or output.
