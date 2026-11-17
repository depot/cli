# Usage analysis

`depot usage` returns organization-scoped JSON without prompting for authentication.
Use an organization API token or read-only organization token in `DEPOT_TOKEN` or `--token`.
The token determines the organization. User tokens from `depot login` are not supported;
`depot org switch` does not change the scope of an organization token.

```sh
depot usage series --product depot-ci --start 2026-10-01T00:00:00Z --end 2026-10-08T00:00:00Z
depot usage breakdown --product depot-ci --group-by workflow --repo owner/repo --page-size 100
```

Each breakdown call returns one page. Continue with `--page-token`, retaining all filters and
time bounds. Counts are protobuf JSON strings; do not coerce large counts to floating point.
Use `series --project PROJECT_ID` for a project's daily builds and layer cache.

Supported product/group combinations:

- `container-builds/project` (`--project`)
- `github-actions/repository`, `github-actions/runner`, `github-actions/job` (`--repo`; job also accepts `--workflow-path`)
- `depot-ci/repository`, `depot-ci/workflow` (`--repo`; workflow also accepts `--workflow-key`)
- `test-results/repository` (`--repo`)

Filters are repeatable. Use `--workflow-path ''` for unattributed GitHub Actions jobs.
Additional reads: `usage report`, `usage agents`, `usage current`. See the
[public guide](https://depot.dev/docs/api/usage-analysis) for their coverage and limitations.

Requires the API deployment containing `UsageAnalyticsService`; old servers return
`Unimplemented` and the command exits without printing partial JSON.

The contract is owned by [depot/api](https://github.com/depot/api/blob/main/proto-public/depot/core/v1/usage_analytics.proto).
The CLI imports its published Go and Connect clients from the two
`buf.build/gen/go/depot/api` modules pinned in `go.mod`.
