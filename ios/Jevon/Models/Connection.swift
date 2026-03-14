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

    /// Text being streamed from the current Jevon response.
    private var streamingText: String = ""

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
    }

    func send(_ text: String) {
        // Add locally immediately for responsiveness.
        messages.append(ChatMessage(role: .user, text: text))

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
            hasConnected = true

        case .history(let entries):
            messages = entries.map { entry in
                ChatMessage(
                    role: entry.role == "user" ? .user : .jevon,
                    text: entry.text,
                    timestamp: entry.timestamp
                )
            }

        case .text(let content):
            streamingText += content
            if serverState != .thinking {
                serverState = .thinking
            }
            updateOrAppendStreamingMessage()

        case .status(let newState):
            serverState = newState
            if newState == .idle {
                flushStreaming()
            }

        case .userMessage(let text, let timestamp):
            // Only add if we didn't already add it locally.
            if messages.last?.role != .user || messages.last?.text != text {
                messages.append(ChatMessage(role: .user, text: text, timestamp: timestamp))
            }

        case .view(let root, let slot):
            if slot == "sheet" {
                sheetView = root
            } else {
                mainView = root
            }

        case .dismiss(let slot):
            if slot == "sheet" {
                sheetView = nil
            }

        case .unknown:
            break
        }
    }

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
