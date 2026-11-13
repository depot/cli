# Assets in merge request descriptions

Repeat `--attach` to upload images and videos when creating a merge request:

```shell
depot mr create --title "Update settings" --body "New settings page" \
  --attach screenshot.png --attach walkthrough.mp4
```

The CLI appends Markdown media references in flag order. Use PNG, JPEG,
or WebP images, or MP4, MOV, or WebM videos. Each attachment is limited to 100 MiB,
with at most ten attachments per description.
`--image` remains an alias for `--attach`; both flags can be mixed. Progress goes
to stderr, so `--json` output remains machine-readable.

Uploads are private to their author until the merge request is saved, then
readable by repository readers signed into Depot. They remain available after
merging, closing, or deleting the source branch. Unattached uploads expire after
24 hours. Videos render with playback controls in Depot; codec support depends
on the browser, and a download link is available.

Each file is sent straight to storage through a short-lived upload URL, read
from disk as it is sent rather than loaded into memory. Transient failures retry
with the same upload identity, and an expired upload URL is replaced. If an
upload fails, the command exits without creating the merge request. Run the
command again to retry; a file that was already stored is not sent again.

If the branch already has an open MR, the CLI uploads the attachments and appends
them to that MR's description. Its title and existing description text are left
unchanged, and `--title` and `--body` are not applied.

Running the command again is safe. Uploading the same file for the same branch
returns the same asset, and an attachment already in the description is not
added again. If every file is already attached, the description is not updated.

An upload that expired, or that belongs to an earlier MR from the same branch
name, cannot be reused. The CLI then uploads a new copy once and continues.

Files are checked before any upload starts: each must be one of the supported
formats and within the size limit. A file that changes while it is uploading
is not stored or attached; run the command again.

The server must support asset uploads before this flag can be used.
