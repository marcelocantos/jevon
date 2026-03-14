// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Messages received from jevond over WebSocket.
enum ServerMessage: Sendable {
    case serverInit(version: String)
    case history(entries: [HistoryEntry])
    case text(content: String)
    case status(state: ServerState)
    case userMessage(text: String, timestamp: Date)
    case scripts(source: String)
    case sessions(entries: [SessionEntry])
    case view(root: ViewNode, slot: String?)
    case dismiss(slot: String)
    case notification(title: String, body: String)
    case unknown(type: String)

    struct SessionEntry: Codable, Sendable {
        let id: String
        let name: String
        let status: String
        let workdir: String
        let active: Bool
    }

    struct HistoryEntry: Codable, Sendable, Identifiable {
        let role: String
        let text: String
        let timestamp: Date

        var id: Date { timestamp }
    }

    enum ServerState: String, Codable, Sendable {
        case thinking
        case idle
    }
}

extension ServerMessage {
    init(from data: Data) throws {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601

        let envelope = try decoder.decode(Envelope.self, from: data)
        switch envelope.type {
        case "init":
            let msg = try decoder.decode(InitMessage.self, from: data)
            self = .serverInit(version: msg.version)
        case "history":
            let msg = try decoder.decode(HistoryMessage.self, from: data)
            self = .history(entries: msg.entries)
        case "text":
            let msg = try decoder.decode(TextMessage.self, from: data)
            self = .text(content: msg.content)
        case "status":
            let msg = try decoder.decode(StatusMessage.self, from: data)
            self = .status(state: msg.state)
        case "user_message":
            let msg = try decoder.decode(UserMessageEcho.self, from: data)
            self = .userMessage(text: msg.text, timestamp: msg.timestamp)
        case "scripts":
            let msg = try decoder.decode(ScriptsMessage.self, from: data)
            self = .scripts(source: msg.source)
        case "sessions":
            let msg = try decoder.decode(SessionsMessage.self, from: data)
            self = .sessions(entries: msg.sessions)
        case "view":
            let msg = try decoder.decode(ViewMessage.self, from: data)
            self = .view(root: msg.root, slot: msg.slot)
        case "dismiss":
            let msg = try decoder.decode(DismissMessage.self, from: data)
            self = .dismiss(slot: msg.slot)
        case "notification":
            let msg = try decoder.decode(NotificationMessage.self, from: data)
            self = .notification(title: msg.title, body: msg.body)
        default:
            self = .unknown(type: envelope.type)
        }
    }

    private struct Envelope: Codable {
        let type: String
    }

    private struct InitMessage: Codable {
        let version: String
    }

    private struct HistoryMessage: Codable {
        let entries: [HistoryEntry]
    }

    private struct TextMessage: Codable {
        let content: String
    }

    private struct StatusMessage: Codable {
        let state: ServerState
    }

    private struct UserMessageEcho: Codable {
        let text: String
        let timestamp: Date
    }

    private struct ScriptsMessage: Codable {
        let source: String
    }

    private struct SessionsMessage: Codable {
        let sessions: [SessionEntry]
    }

    private struct NotificationMessage: Codable {
        let title: String
        let body: String
    }
}

/// Message sent from the client to jevond.
struct ClientMessage: Codable, Sendable {
    let type: String
    let text: String

    static func message(_ text: String) -> ClientMessage {
        ClientMessage(type: "message", text: text)
    }
}
