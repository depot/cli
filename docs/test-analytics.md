# Test analytics

Use `depot tests analytics` to find flaky or slow tests across Depot CI and GitHub Actions runs.

```sh
depot tests analytics --repo acme/api --ranking flaky --output json
depot tests analytics --repo acme/api --ranking slowest --output json
depot tests analytics --ci --ref refs/heads/main --start-date 2026-09-01 --end-date 2026-09-07
```

`--ranking flaky` is the default. It requires both passing and failing/errored executions at the same repository,
ref, and commit, and ranks by the number of affected ref/commit groups. `--ranking slowest` ranks by approximate p95
of positive test durations. Identical test names in different repositories and execution sources stay separate.

Dates are inclusive UTC calendar dates. The default is seven days including today; the maximum is 90 days.
Use `--ci` or `--gha` to select an execution source. `--repo` and `--ref` match stored source values exactly.
Use `--suite`, `--class`, and `--file` for exact representative test-identity labels, and `--test` for a case-insensitive
name substring. `--limit` defaults to 20 and accepts up to 100. `hasMore` in JSON means more tests match;
increase the limit or narrow the filters.

When piped, output defaults to JSON and authentication never prompts. Use an existing login or API token and
`--org` where needed. Organization tokens and user tokens with organization membership are supported;
project and workload tokens cannot read organization-wide analytics. `--output table` and `--output json`
explicitly select a format. JSON preserves 64-bit counts as decimal strings.

Each entry includes a recent failure sample where available, including an owner ID, message and stack trace.
For more context, pass `latestFailure.ownerId` and its source to the existing command:

```sh
depot tests <owner-id> --ci --status failed --status errored --output json
```

The sample is the latest failure in the selected window, and may be from a different revision than the revisions
that made the test qualify as flaky. Text fields are limited to 8 KiB; use the per-owner command to inspect further.
Store failures return an error rather than an empty result. Recent uploads may take time to appear.
