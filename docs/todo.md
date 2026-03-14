# TODO

- ~~Do Jevon minions survive jevond restart?~~ Done.
- ~~Can jevon-ctl be an internal tool (e.g. MCP server) instead of a separate binary?~~ Done — replaced with in-process MCP server.
- ~~Rename project from "dais" to "jevon" (Jevons paradox reference)~~ Done.
- Cache Lua scripts locally on the device so the connect screen renders from Lua even before connecting to jevond. Once caching works, remove the old purpose-built Swift views (ConnectView, ChatView, SessionListView, QRScannerView, SessionService, SessionSummary).
