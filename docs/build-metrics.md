# Container-build resource metrics

`depot builds metrics` exports one page of completed builds with measurements for each build-machine association. `depot builds analytics` returns project and architecture summaries computed by the API.

## Authentication and time range

Set `DEPOT_TOKEN` to an organization API token with permission to read builds, or supply `--token`. The token determines the organization; these commands do not use `depot login`, project tokens, or `--org`. Select the project with `--project`, `DEPOT_PROJECT_ID`, or a local `depot.json`, in that order.

Both commands require `--start` and `--end` as RFC 3339 timestamps with at most millisecond precision. They select builds by completion time: start inclusive, end exclusive. The range must be positive, at most seven days, and end no later than now. Timestamps must be on or after January 1, 1970.

```shell
depot builds analytics --project <project-id> \
  --start 2026-11-01T00:00:00Z --end 2026-11-02T00:00:00Z --output table

depot builds metrics --project <project-id> \
  --start 2026-11-01T00:00:00Z --end 2026-11-02T00:00:00Z --output csv > builds.csv
```

The time range selects builds, not slices of their measurements. A build that finishes in the range can include observations from before `--start`.

## Output and pagination

Both commands accept `--output table`, `--output json`, or `--output csv` (`-o` for short). The default, `auto`, selects table output in a terminal and JSON when redirected. All data goes to stdout; pagination instructions go to stderr.

`metrics` returns newest builds first. `--page-size` defaults to 20 and accepts 1–50. Each invocation requests exactly one page. JSON includes `nextPageToken`; table and CSV print that token to stderr. Pass it as `--page-token` with the same organization token, project, start, and end to continue. Stop when no token is returned. Do not treat a single page as the full project history.

```shell
depot builds metrics --project <project-id> \
  --start 2026-11-01T00:00:00Z --end 2026-11-02T00:00:00Z \
  --page-size 50 --output json > page-1.json

depot builds metrics --project <project-id> \
  --start 2026-11-01T00:00:00Z --end 2026-11-02T00:00:00Z \
  --page-size 50 --page-token "$(jq -r '.nextPageToken' page-1.json)" \
  --output json > page-2.json
```

Only run the second command when the first page has a nonempty token. An empty token requests the first page again.

JSON preserves the API response, including machine association times and optional capacities. Metrics CSV has one row per build/machine, with build ID, creation and completion times, hardware, machine ID, architecture, association times, capacities, and statistics. A build without a recorded machine still receives a row with empty machine fields. Each CSV page includes its own header.

`analytics` has no pagination. It summarizes at most 1,000 completed builds and returns an error if the range exceeds server limits; narrow the range and retry. Analytics CSV contains one `scope=project` row and one `scope=architecture` row per architecture. Build counts apply to the project row; architecture rows leave those fields empty. The project row already includes the architecture rows, so do not add them together. The CLI never computes a project average from a page of builds.

## Units and weighting

| Measurement | Meaning |
| --- | --- |
| CPU/memory utilization | Fraction of the collector's observed machine capacity in JSON/CSV; percentage in tables. |
| Average CPU utilization | Observed-duration-weighted utilization, including user and kernel CPU. |
| Average memory utilization/bytes | Duration-weighted mean of each interval's highest sampled sum of build-owned executor memory. |
| Peak CPU/memory utilization | Highest observed interval value. Summary peaks are individual build-machine peaks, not simultaneous project totals. |
| Peak memory bytes | Highest sampled sum of build-owned executor memory; brief spikes between samples can be missed. |
| CPU time seconds | CPU time consumed by observed executor cgroups, summed across machines/builds in summaries. |
| CPU/memory observed seconds | Sum of intervals with observations for that metric. Concurrent builds and machines contribute separately; this is not elapsed wall time. |
| CPU/memory sample count | Number of measured collection intervals, summed across machines/builds in summaries. |
| CPU capacity / memory capacity bytes | Capacity observed by the collector, when available; memory is in bytes. |

Averages use observed duration rather than build count, sample count, or full build duration. Unobserved intervals do not contribute zeroes. For comparisons, inspect sample counts and observed seconds alongside utilization.

## Coverage and retention

These measurements cover build-owned executor work. They exclude other builds and host activity, including daemon overhead. Cache hits, idle gaps, already-shared or anonymous operations, and unsupported cgroup layouts are unmeasured. Later consumers do not receive copied measurements from shared work.

Measurements require a builder with the collector enabled and an API that supports these endpoints. Older builders have no observations. Historical host metrics cannot be converted into build-owned metrics, and these commands never fall back to host measurements.

Missing optional utilization, CPU time, memory bytes, and capacity values remain absent in JSON, blank in CSV, and `n/a` in tables. Zero sample counts and zero observed seconds describe missing coverage; a present utilization value of zero represents a real measurement. `buildsWithoutMachines` counts missing machine associations, not every build missing telemetry.

Availability follows the backend's metric retention. The seven-day request limit is a query bound, not a retention guarantee. Build history can remain after its measurements expire; those builds retain missing statistics rather than acquiring zero usage. The CLI does not store or extend retention.

Requests have a 30-second timeout and support cancellation. Errors produce a nonzero exit status. If server resource limits are exceeded, narrow the date range or reduce the metrics page size.
