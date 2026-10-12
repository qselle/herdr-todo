Herdr Todo now runs as a standalone Go binary. Python is no longer required.

- Keeps inline capture, live search, the unified editor, mouse controls and undo.
- Keeps optional Herdr space attachments and click-to-open navigation.
- Opens existing v0.1.0 databases directly, preserving tasks, notes, timestamps,
  space links and activity. No export/import or data migration is needed.
- Includes macOS and Linux binaries for arm64 and amd64, with SHA256 checksums.
- Installation downloads and verifies the matching binary automatically.

Install or upgrade from a Herdr pane:

```sh
herdr plugin install qselle/herdr-todo --ref v0.2.0
```

The existing `herdr-todo.open` shortcut and task database location are unchanged.
