// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import Foundation
import Observation
import os

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
            case "show_sessions":
                // Ask the server for fresh session data, then show the sheet.
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

            let data: Data
            switch message {
            case .string(let text):
                data = Data(text.utf8)
            case .data(let d):
                data = d
            @unknown default:
                continue
            }

            do {
                let serverMsg = try ServerMessage(from: data)
                handleMessage(serverMsg)
                reconnectDelay = 1.0
            } catch {
                logger.warning("Failed to parse server message: \(error.localizedDescription) — raw: \(String(data: data.prefix(200), encoding: .utf8) ?? "?")")
                // Don't disconnect on parse errors — just skip the message.
            }
        }
    }

    private func handleMessage(_ msg: ServerMessage) {
        switch msg {
        case .serverInit(let version):
            state = .connected(version: version)
            serverVersion = version
            hasConnected = true
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
        if runtime.loadScript(source) {
            luaRuntime = runtime
            logger.info("Lua scripts loaded for client-side rendering")
            renderViews()
        } else {
            logger.error("Failed to load Lua scripts — falling back to server rendering")
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

        mainView = luaRuntime.callScreen(screenName, state: state)

        // Render sheet if active.
        if !activeSheet.isEmpty {
            let sheetScreen = activeSheet + "_screen"
            sheetView = luaRuntime.callScreen(sheetScreen, state: state)
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
