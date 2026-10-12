`v0.1.0.sqlite3` was created by the unmodified Python store shipped in v0.1.0.
`v0.1.0.json` is that store's export of the same database. All entries are
synthetic: three tasks, a note containing a newline, a completed task, a task
in progress, a space attachment, and events, at a fixed UTC timestamp.

The Go compatibility test copies the database into a temporary directory and
compares its entire export before exercising edits and continued task IDs.
Neither Python nor Herdr is required to run the tests.
