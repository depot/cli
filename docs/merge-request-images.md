# Images in merge request descriptions

Repeat `--image` to upload screenshots when creating a merge request:

```shell
depot mr create --title "Update settings" --body "New settings page" \
  --image before.png --image after.png
```

The CLI appends Markdown images to the description in flag order. Use static
PNG, JPEG, or WebP files, each at most 10 MiB and 40 megapixels. A description
supports up to ten images. Progress goes to stderr, so `--json` output remains
machine-readable.

Uploads are private to their author until the merge request is saved, then
readable by repository readers signed into Depot. They remain available after
the merge request is merged or closed, including after deleting its source
branch. Unattached uploads expire after 24 hours.

Transient upload failures are retried with the same upload identity. If an
upload fails, the command exits without creating the merge request. Run the
command again to retry. If the branch already has an open merge request, the
CLI returns that request without uploading additional images.

The server must support image uploads before this flag can be used.
