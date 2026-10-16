# Cancel container builds

Use `depot build cancel` to request cancellation of container builds. Supply an
organization API token through `DEPOT_TOKEN` or `--token`. Create an organization
token in **Organization Settings > API Tokens**. Project tokens, reader tokens,
legacy `depot login` tokens, and CI OIDC credentials cannot perform this operation.
The token determines the organization; no organization switch is required.

```sh
# Cancel one or more builds. Repeated IDs are processed once.
depot build cancel <build-id> [<build-id>...]

# Preview running builds in a project without changing anything.
depot build cancel --project <project-id> --all-running --dry-run

# Review the selected IDs and confirm interactively.
depot build cancel --project <project-id> --all-running

# Cancel selected running builds from a script.
depot build cancel --project <project-id> --all-running --yes --output json
```

`--all-running` requires an explicit `--project`; it does not infer a project from
the current directory or `DEPOT_PROJECT_ID`. It cannot be combined with build IDs.
All pages are read before any cancellation requests are sent. Only builds reported
as running are selected, and the selected IDs are fixed before confirmation. This
is not an atomic snapshot: builds may finish during selection, and builds started
after selection are excluded. A listing failure cancels nothing.

Bulk cancellation requires confirmation, or `--yes` when input or output is not a
terminal. `--dry-run` skips confirmation and makes no cancellation requests.
Explicit build IDs do not require confirmation. Requests run with at most four
in flight and a 30-second timeout per API call.

Results appear in selection order:

- `OUTCOME_CANCELLATION_REQUESTED`: the API accepted the request. Execution may
  still be running; use the build detail page or API to observe its final outcome.
- `OUTCOME_ALREADY_TERMINAL`: the build already finished. Nothing was changed.
- An error: the request failed, timed out, or returned an unrecognized outcome.
  Other selected builds are still attempted. An unknown outcome never confirms
  cancellation. A timeout may occur after the server accepted a request; retrying
  the same build ID is safe.

Some build executors do not support cancellation and return `failed_precondition`.
An older server without the cancellation API returns `unimplemented`. The CLI
reports these per build and does not fall back to the build completion API.

The command exits nonzero if any selected build has an error. With `--output json`,
stdout contains one JSON object even when some builds fail; the summary error goes
to stderr:

```json
{
  "dry_run": false,
  "results": [
    {"build_id": "build-a", "outcome": "OUTCOME_CANCELLATION_REQUESTED"},
    {"build_id": "build-b", "outcome": "OUTCOME_ALREADY_TERMINAL"},
    {"build_id": "build-c", "error": {"code": "not_found", "message": "not_found: Build not found"}}
  ]
}
```

A dry run sets `dry_run` to `true` and uses `WOULD_REQUEST_CANCELLATION` for each
selected ID. No running builds produces an empty `results` array and succeeds.
Validation, authentication resolution, and listing failures happen before results
exist and report an error on stderr.

`depot build .` continues to start a build. To build a directory named `cancel`, use
`depot build ./cancel`. `depot ci cancel` remains the command for Depot CI runs.
