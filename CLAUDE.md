# cairn — agent guidance

## Task tracking — ioc

cairn's tasks live in the ioc tracker (the `tasks` MCP server, product
`cairn`), not in `htt`. File follow-ups, bugs and deferred work there with
`task_create`, record progress with `note_add`, and read the backlog with
`task_list` / `task_show`. The `cairn` roadmap in `htt` is retired: read-only
history, do not write to it.
