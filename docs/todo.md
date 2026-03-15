# TODO

- ~~Do Jevon minions survive jevond restart?~~ Done.
- ~~Can jevon-ctl be an internal tool (e.g. MCP server) instead of a separate binary?~~ Done — replaced with in-process MCP server.
- ~~Rename project from "dais" to "jevon" (Jevons paradox reference)~~ Done.
- Cache Lua scripts locally on the device so the connect screen renders from Lua even before connecting to jevond. Once caching works, remove the old purpose-built Swift views (ConnectView, ChatView, SessionListView, QRScannerView, SessionService, SessionSummary).
- Audit SwiftUI modifier surface area and expose a moderate but fairly complete set as Lua-controllable props. Currently several behavioral modifiers are hardcoded in the Swift renderer (e.g. keyboard avoidance, scroll dismiss, autocorrection). These should be optional props on the relevant primitives so Lua scripts have full control. Areas to cover: keyboard behavior (ignore/avoid/dismiss), scroll behavior (anchoring, paging, indicators), text input (autocorrect, autocapitalization, keyboard type, secure entry), accessibility (labels, hints, traits), animation (transitions, implicit animations), gestures (long press, drag), safe area handling, and focus management.
