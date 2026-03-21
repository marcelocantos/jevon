// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import Foundation
import Observation
import os
import UIKit

private let logger = Logger(subsystem: "com.marcelocantos.jevon", category: "Connection")

/// Manages the WebSocket connection to jevond.
@Observable
@MainActor
final class Connection {
    enum State: Sendable {
        case disconnected
        case connecting
        case connected(version: String)
        case error(String)
    }

    private(set) var state: State = .disconnected
    private(set) var messages: [ChatMessage] = []
    private(set) var serverState: ServerMessage.ServerState = .idle
    /// True once a successful connection has been established at least once.
    private(set) var hasConnected: Bool = false

    /// Server-driven UI: main screen view tree.
    private(set) var mainView: ViewNode?
    /// Server-driven UI: modal sheet view tree.
    private(set) var sheetView: ViewNode?

    /// In-app notification alert.
    private(set) var notificationTitle: String?
    private(set) var notificationBody: String?

    func dismissNotification() {
        notificationTitle = nil
        notificationBody = nil
    }

    /// Text being streamed from the current Jevon response.
    private var streamingText: String = ""

    // MARK: - Client-side Lua rendering

    /// Lua runtime for client-side view rendering. Created when scripts arrive.
    private var luaRuntime: LuaRuntime?
    /// Cached session list from the server.
    private var sessions: [ServerMessage.SessionEntry] = []
    /// Currently active sheet (empty = none, "sessions" = session list).
    private var activeSheet: String = ""
    /// Server version string, extracted from init message.
    private var serverVersion: String = ""

    // MARK: - sqlpipe sync

    /// Bidirectional SQLite sync peer. Created on first serverInit.
    private var syncPeer: SyncPeer?
    /// Subscription IDs for auto-render queries.
    private var transcriptSubID: UInt64?
    private var stateSubID: UInt64?
    /// Latest subscription results — the source of truth for Lua state
    /// when syncPeer is active. Populated from subscription callbacks,
    /// never from direct queries.
    private var syncMessages: [[String: Any]] = []

    private var webSocket: URLSessionWebSocketTask?
    private var receiveTask: Task<Void, Never>?
    private var reconnectTask: Task<Void, Never>?

    private(set) var serverURL: URL?
    private var reconnectDelay: TimeInterval = 1.0
    private static let maxReconnectDelay: TimeInterval = 8.0

    func connect(to host: String, port: Int) {
        disconnect()
        guard let url = URL(string: "ws://\(host):\(port)/ws/remote") else {
            state = .error("Invalid URL")
            return
        }
        serverURL = url
        saveLastServer(host: host, port: port)
        doConnect(url: url)
    }

    func disconnect() {
        reconnectTask?.cancel()
        reconnectTask = nil
        receiveTask?.cancel()
        receiveTask = nil
        webSocket?.cancel(with: .goingAway, reason: nil)
        webSocket = nil
        state = .disconnected
        hasConnected = false
        reconnectDelay = 1.0
        luaRuntime = nil
        sessions = []
        activeSheet = ""
        syncPeer?.close()
        syncPeer = nil
        transcriptSubID = nil
        stateSubID = nil
        syncMessages = []
    }

    func send(_ text: String) {
        // Add locally immediately for responsiveness.
        messages.append(ChatMessage(role: .user, text: text))
        renderViews()

        guard let webSocket else { return }

        let msg = ClientMessage.message(text)
        guard let data = try? JSONEncoder().encode(msg) else { return }
        let string = String(data: data, encoding: .utf8) ?? ""

        webSocket.send(.string(string)) { [weak self] error in
            if let error {
                Task { @MainActor [weak self] in
                    self?.state = .error(error.localizedDescription)
                }
            }
        }
    }

    /// Send an action back to the server (from server-driven UI interactions).
    func sendAction(_ action: String, value: String = "") {
        // Handle some actions locally when Lua scripts are loaded.
        if luaRuntime != nil {
            switch action {
            case "send_message" where !value.isEmpty:
                // Add message locally for immediate feedback.
                messages.append(ChatMessage(role: .user, text: value))
                renderViews()
                // Always use JSON path for send_message — the server's
                // HandleUserMessage writes to sync_transcript, which syncs
                // back via sqlpipe. The requests table path doesn't trigger
                // the transcript write.
                sendToServer(action: action, value: value)
                return

            case "show_sessions":
                sendToServer(action: action, value: value)
                return

            case "dismiss_sheet":
                activeSheet = ""
                renderViews()
                return

            default:
                break
            }
        }

        sendToServer(action: action, value: value)
    }

    /// Base HTTP URL derived from the WebSocket connection URL.
    var httpBaseURL: URL? {
        guard let serverURL else { return nil }
        var components = URLComponents(url: serverURL, resolvingAgainstBaseURL: false)
        components?.scheme = "http"
        components?.path = ""
        return components?.url
    }

    // MARK: - Control channel

    /// Pending control response continuations, keyed by action name.
    private var controlContinuations: [String: CheckedContinuation<ControlResponse, Never>] = [:]

    /// Send a control-channel message and await the response.
    /// Control messages bypass the Lua layer entirely.
    func sendControl(action: String, value: String = "") async -> Result<[String: Any], Error> {
        guard webSocket != nil else {
            return .failure(ControlError.notConnected)
        }

        let response = await withCheckedContinuation { (continuation: CheckedContinuation<ControlResponse, Never>) in
            controlContinuations[action] = continuation

            let msg: [String: String] = [
                "type": "control",
                "action": action,
                "value": value,
            ]
            guard let data = try? JSONEncoder().encode(msg),
                  let string = String(data: data, encoding: .utf8) else {
                controlContinuations.removeValue(forKey: action)
                continuation.resume(returning: .error("Failed to encode control message"))
                return
            }

            webSocket?.send(.string(string)) { [weak self] error in
                if let error {
                    Task { @MainActor in
                        if let cont = self?.controlContinuations.removeValue(forKey: action) {
                            cont.resume(returning: .error(error.localizedDescription))
                        }
                    }
                }
            }

            // Timeout after 10 seconds.
            Task {
                try? await Task.sleep(for: .seconds(10))
                await MainActor.run {
                    if let cont = self.controlContinuations.removeValue(forKey: action) {
                        cont.resume(returning: .error("Control request timed out"))
                    }
                }
            }
        }

        switch response {
        case .success(let jsonData):
            if let json = try? JSONSerialization.jsonObject(with: jsonData) as? [String: Any] {
                return .success(json)
            }
            return .failure(ControlError.serverError("Invalid response format"))
        case .error(let msg):
            return .failure(ControlError.serverError(msg))
        }
    }

    /// Handle an incoming control message — either a response to a pending
    /// request or a server-pushed action (like exec_lua).
    func handleControlResponse(_ data: [String: Any]) {
        guard let action = data["action"] as? String else { return }

        // Server-pushed control actions (no pending request).
        switch action {
        case "exec_lua":
            if let code = data["code"] as? String {
                let result = execLua(code)
                logger.info("exec_lua result: \(result ?? "nil")")
            }
            return
        case "screenshot":
            captureAndSendScreenshot()
            return
        default:
            break
        }

        // Response to a pending request.
        guard let continuation = controlContinuations.removeValue(forKey: action) else {
            return
        }
        if let jsonData = try? JSONSerialization.data(withJSONObject: data) {
            continuation.resume(returning: .success(jsonData))
        } else {
            continuation.resume(returning: .error("Failed to re-serialize control response"))
        }
    }

    /// Sendable wrapper for control responses to cross isolation boundaries.
    /// Stores the raw JSON Data rather than [String: Any] to satisfy Sendable.
    private enum ControlResponse: Sendable {
        case success(_ jsonData: Data)
        case error(_ message: String)
    }

    enum ControlError: LocalizedError {
        case notConnected
        case timeout
        case serverError(String)

        var errorDescription: String? {
            switch self {
            case .notConnected: "Not connected to server"
            case .timeout: "Control request timed out"
            case .serverError(let msg): msg
            }
        }
    }

    // MARK: - Persistence

    var lastServer: (host: String, port: Int)? {
        let defaults = UserDefaults.standard
        guard let host = defaults.string(forKey: "lastHost") else { return nil }
        let port = defaults.integer(forKey: "lastPort")
        return port > 0 ? (host, port) : nil
    }

    private func saveLastServer(host: String, port: Int) {
        let defaults = UserDefaults.standard
        defaults.set(host, forKey: "lastHost")
        defaults.set(port, forKey: "lastPort")
    }

    // MARK: - Internal

    private func sendToServer(action: String, value: String) {
        guard let webSocket else { return }

        let msg = ActionMessage(type: "action", action: action, value: value.isEmpty ? nil : value)
        guard let data = try? JSONEncoder().encode(msg),
              let string = String(data: data, encoding: .utf8) else { return }

        webSocket.send(.string(string)) { [weak self] error in
            if let error {
                Task { @MainActor [weak self] in
                    self?.state = .error(error.localizedDescription)
                }
            }
        }
    }

    private func doConnect(url: URL) {
        state = .connecting

        let session = URLSession(configuration: .default)
        let task = session.webSocketTask(with: url)
        webSocket = task
        task.resume()

        receiveTask = Task { [weak self] in
            await self?.receiveLoop()
        }
    }

    private func receiveLoop() async {
        guard let webSocket else { return }

        while !Task.isCancelled {
            let message: URLSessionWebSocketTask.Message
            do {
                message = try await webSocket.receive()
            } catch {
                if !Task.isCancelled {
                    logger.error("WebSocket receive failed: \(error.localizedDescription)")
                    state = .error("Disconnected")
                    flushStreaming()
                    scheduleReconnect()
                }
                return
            }

            switch message {
            case .data(let d):
                // Binary frame — sqlpipe sync protocol.
                handleBinaryMessage(d)
                reconnectDelay = 1.0
                continue

            case .string(let text):
                // JSON frame — check for control response first.
                let data = Data(text.utf8)
                if let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
                   json["type"] as? String == "control" {
                    handleControlResponse(json)
                    reconnectDelay = 1.0
                    continue
                }

                // Application protocol.
                do {
                    let serverMsg = try ServerMessage(from: data)
                    handleMessage(serverMsg)
                    reconnectDelay = 1.0
                } catch {
                    logger.warning("Failed to parse server message: \(error.localizedDescription) — raw: \(String(data: data.prefix(200), encoding: .utf8) ?? "?")")
                }

            @unknown default:
                continue
            }
        }
    }

    private func handleMessage(_ msg: ServerMessage) {
        switch msg {
        case .serverInit(let version):
            state = .connected(version: version)
            serverVersion = version
            hasConnected = true
            initSyncPeer()
            renderViews()

        case .history(let entries):
            messages = entries.map { entry in
                ChatMessage(
                    role: entry.role == "user" ? .user : .jevon,
                    text: entry.text,
                    timestamp: entry.timestamp
                )
            }
            renderViews()

        case .text(let content):
            streamingText += content
            if serverState != .thinking {
                serverState = .thinking
            }
            updateOrAppendStreamingMessage()
            renderViews()

        case .status(let newState):
            serverState = newState
            if newState == .idle {
                flushStreaming()
            }
            renderViews()

        case .userMessage(let text, let timestamp):
            // Only add if we didn't already add it locally.
            if messages.last?.role != .user || messages.last?.text != text {
                messages.append(ChatMessage(role: .user, text: text, timestamp: timestamp))
            }
            renderViews()

        case .scripts(let source):
            loadScripts(source)

        case .sessions(let entries):
            sessions = entries
            activeSheet = "sessions"
            renderViews()

        case .view(let root, let slot):
            // Fallback: accept server-rendered views if no Lua scripts loaded.
            if luaRuntime == nil {
                if slot == "sheet" {
                    sheetView = root
                } else {
                    mainView = root
                }
            }

        case .dismiss(let slot):
            if luaRuntime == nil {
                if slot == "sheet" {
                    sheetView = nil
                }
            }

        case .notification(let title, let body):
            notificationTitle = title
            notificationBody = body

        case .unknown:
            break
        }
    }

    // MARK: - Client-side Lua rendering

    private func loadScripts(_ source: String) {
        let runtime = LuaRuntime()
        runtime.onAction = { [weak self] action, value in
            self?.handleLuaAction(action, value: value)
        }
        if runtime.loadScript(source) {
            luaRuntime = runtime
            logger.info("Lua scripts loaded for client-side rendering")
            renderViews()
        } else {
            logger.error("Failed to load Lua scripts — falling back to server rendering")
        }
    }

    /// Handle actions triggered by Lua client-side functions.
    private func handleLuaAction(_ action: String, value: String) {
        switch action {
        case "disconnect":
            disconnect()
        default:
            sendAction(action, value: value)
        }
    }

    /// Execute Lua code pushed via the control channel. Returns result string.
    func execLua(_ code: String) -> String? {
        return luaRuntime?.eval(code)
    }

    /// Capture the current screen and send it back via the control channel.
    private func captureAndSendScreenshot() {
        guard let scene = UIApplication.shared.connectedScenes.first as? UIWindowScene,
              let window = scene.windows.first else {
            logger.error("screenshot: no window")
            return
        }

        let renderer = UIGraphicsImageRenderer(bounds: window.bounds)
        let image = renderer.image { ctx in
            window.drawHierarchy(in: window.bounds, afterScreenUpdates: false)
        }

        guard let pngData = image.pngData() else {
            logger.error("screenshot: PNG encoding failed")
            return
        }

        let base64 = pngData.base64EncodedString()

        guard let webSocket else { return }
        let msg: [String: String] = [
            "type": "control",
            "action": "screenshot_result",
            "value": base64,
        ]
        guard let jsonData = try? JSONEncoder().encode(msg),
              let string = String(data: jsonData, encoding: .utf8) else { return }

        webSocket.send(.string(string)) { error in
            if let error {
                logger.error("screenshot send failed: \(error.localizedDescription)")
            }
        }
    }

    /// Re-render views using local Lua scripts and current state.
    private func renderViews() {
        guard let luaRuntime else { return }

        let state = buildLuaState()

        // Determine main screen.
        let isConnected: Bool
        switch self.state {
        case .connected: isConnected = true
        default: isConnected = false
        }
        let screenName = isConnected ? "chat_screen" : "connect_screen"

        mainView = luaRuntime.callScreen(screenName, state: state)?.withPathIDs()

        // Render sheet if active.
        if !activeSheet.isEmpty {
            let sheetScreen = activeSheet + "_screen"
            sheetView = luaRuntime.callScreen(sheetScreen, state: state)?.withPathIDs(prefix: "sheet/")
        } else {
            sheetView = nil
        }
    }

    /// Build the state dictionary that Lua screen functions expect.
    private func buildLuaState() -> [String: Any] {
        let isConnected: Bool
        switch state {
        case .connected: isConnected = true
        default: isConnected = false
        }

        // When syncPeer has delivered subscription data, use it.
        // syncMessages is populated reactively from subscription callbacks
        // — no direct queries. Falls through to in-memory state if
        // subscriptions haven't fired yet (sync still in progress).
        if syncPeer != nil, !syncMessages.isEmpty || messages.isEmpty {
            return [
                "connected": isConnected,
                "version": serverVersion,
                "status": serverState == .thinking ? "thinking" : "idle",
                "messages": syncMessages,
                "streaming_text": streamingText,
                "sessions": sessions.map { s in
                    ["id": s.id, "name": s.name, "status": s.status,
                     "workdir": s.workdir, "active": s.active] as [String: Any]
                },
            ]
        }

        // Fallback: build from in-memory state.
        let formatter = ISO8601DateFormatter()
        let msgs: [[String: Any]] = messages.map { msg in
            [
                "role": msg.role == .user ? "user" : "jevon",
                "text": msg.text,
                "timestamp": formatter.string(from: msg.timestamp),
            ]
        }

        let sessEntries: [[String: Any]] = sessions.map { s in
            [
                "id": s.id,
                "name": s.name,
                "status": s.status,
                "workdir": s.workdir,
                "active": s.active,
            ]
        }

        return [
            "connected": isConnected,
            "version": serverVersion,
            "status": serverState == .thinking ? "thinking" : "idle",
            "messages": msgs,
            "streaming_text": streamingText,
            "sessions": sessEntries,
        ]
    }

    // MARK: - Streaming helpers

    private func updateOrAppendStreamingMessage() {
        if let last = messages.last, last.role == .jevon, last.isStreaming {
            messages[messages.count - 1] = ChatMessage(
                role: .jevon,
                text: streamingText,
                timestamp: last.timestamp,
                isStreaming: true
            )
        } else {
            messages.append(ChatMessage(
                role: .jevon,
                text: streamingText,
                timestamp: Date(),
                isStreaming: true
            ))
        }
    }

    private func flushStreaming() {
        guard !streamingText.isEmpty else { return }
        if let last = messages.last, last.role == .jevon, last.isStreaming {
            messages[messages.count - 1] = ChatMessage(
                role: .jevon,
                text: streamingText,
                timestamp: last.timestamp,
                isStreaming: false
            )
        }
        streamingText = ""
    }

    // MARK: - sqlpipe sync

    /// Initialize the SyncPeer and send the handshake to the server.
    private func initSyncPeer() {
        guard syncPeer == nil else { return }

        let docsDir = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first!
        let dbPath = docsDir.appendingPathComponent("jevon-sync.db").path

        do {
            let peer = try SyncPeer(dbPath: dbPath, ownedTables: ["requests"])

            // Create client-owned table. Server-owned tables are created
            // automatically via the on_schema_mismatch callback during handshake.
            try peer.execute("""
                CREATE TABLE IF NOT EXISTS requests (
                    id         INTEGER PRIMARY KEY AUTOINCREMENT,
                    type       TEXT NOT NULL,
                    payload    TEXT NOT NULL DEFAULT '',
                    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                )
                """)

            syncPeer = peer

            // Start the handshake — send initial data to server.
            if let handshake = peer.start() {
                sendBinary(handshake)
            }

            // Set up query subscriptions for auto-render.
            setupSubscriptions()

            logger.info("SyncPeer initialized")
        } catch {
            logger.error("Failed to init SyncPeer: \(error.localizedDescription)")
        }
    }

    /// Subscribe to key queries so we re-render when data changes.
    /// Results arrive via HandleResult.subscriptions after sync enters
    /// Live state — no immediate evaluation.
    private func setupSubscriptions() {
        guard let syncPeer else { return }

        transcriptSubID = syncPeer.subscribe(
            "SELECT role, content AS text, timestamp FROM sync_transcript ORDER BY rowid"
        )

        stateSubID = syncPeer.subscribe(
            "SELECT status, streaming_text, version FROM server_state WHERE id = 1"
        )
    }

    /// Handle a binary WebSocket frame (sqlpipe sync protocol).
    private func handleBinaryMessage(_ data: Data) {
        guard let syncPeer else {
            logger.warning("Received binary message but syncPeer is nil")
            return
        }

        let result = syncPeer.handleMessage(data)

        // Send response data back to server.
        if let response = result.response {
            sendBinary(response)
        }

        // Process subscription results.
        var changed = false
        for sub in result.subscriptions {
            if sub.id == transcriptSubID {
                syncMessages = queryResultToDicts(sub)
                changed = true
            } else if sub.id == stateSubID {
                changed = true
            }
        }

        // Re-render if data changed or subscriptions fired.
        if result.hasChanges || changed {
            renderViews()
        }
    }

    /// Insert a request into the client-owned requests table and flush.
    private func sendRequest(type: String, payload: String) {
        guard let syncPeer else {
            // Fall back to JSON.
            sendToServer(action: type, value: payload)
            return
        }

        do {
            // Use single quotes for SQL string values; escape any embedded quotes.
            let escapedType = type.replacingOccurrences(of: "'", with: "''")
            let escapedPayload = payload.replacingOccurrences(of: "'", with: "''")
            try syncPeer.execute(
                "INSERT INTO requests (type, payload) VALUES ('\(escapedType)', '\(escapedPayload)')"
            )

            if let flushData = syncPeer.flush() {
                sendBinary(flushData)
            }
        } catch {
            logger.error("sendRequest failed: \(error.localizedDescription)")
            // Fall back to JSON.
            sendToServer(action: type, value: payload)
        }
    }

    /// Convert a QueryResult into the [[String: Any]] format Lua expects.
    private func queryResultToDicts(_ qr: QueryResult) -> [[String: Any]] {
        qr.rows.map { row in
            var dict: [String: Any] = [:]
            for (i, col) in qr.columns.enumerated() where i < row.count {
                dict[col] = row[i].anyValue
            }
            return dict
        }
    }

    /// Send a binary WebSocket frame.
    private func sendBinary(_ data: Data) {
        guard let webSocket else { return }
        webSocket.send(.data(data)) { [weak self] error in
            if let error {
                Task { @MainActor [weak self] in
                    logger.error("Binary send failed: \(error.localizedDescription)")
                    self?.state = .error(error.localizedDescription)
                }
            }
        }
    }

    private func scheduleReconnect() {
        guard let url = serverURL else { return }
        let delay = reconnectDelay
        reconnectDelay = min(reconnectDelay * 2, Self.maxReconnectDelay)

        reconnectTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled else { return }
            self?.doConnect(url: url)
        }
    }
}
