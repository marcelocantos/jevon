// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import Foundation
import os

private let logger = Logger(subsystem: "com.marcelocantos.jevon", category: "SyncPeer")

/// Result of handling an incoming message from the server.
struct HandleResult {
    /// Response data to send back over WebSocket (serialized PeerMessages).
    let response: Data?
    /// Whether any data changes were applied to the local replica.
    let hasChanges: Bool
    /// Updated subscription query results (if any subscriptions fired).
    let subscriptions: [QueryResult]
}

/// A subscription query result decoded from sqlpipe's binary format.
struct QueryResult {
    let id: UInt64
    let columns: [String]
    let rows: [[SQLValue]]
}

/// A SQLite value decoded from sqlpipe's wire format.
enum SQLValue {
    case null
    case integer(Int64)
    case real(Double)
    case text(String)
    case blob(Data)

    /// Convert to a dictionary-friendly Any value.
    var anyValue: Any {
        switch self {
        case .null: return NSNull()
        case .integer(let v): return v
        case .real(let v): return v
        case .text(let v): return v
        case .blob(let v): return v
        }
    }
}

/// Swift wrapper around sqlpipe's Peer C API for bidirectional SQLite sync.
///
/// The peer owns a local SQLite database and synchronises it with a remote
/// peer (jevond) over WebSocket using sqlpipe's changeset protocol.
@MainActor
final class SyncPeer {
    // nonisolated(unsafe) so deinit can access them for cleanup.
    // All other access is @MainActor-isolated.
    private nonisolated(unsafe) var db: OpaquePointer?
    private nonisolated(unsafe) var peer: OpaquePointer?

    /// Open a local SQLite database and create a sqlpipe Peer.
    ///
    /// - Parameters:
    ///   - dbPath: Path to the SQLite database file.
    ///   - ownedTables: Tables owned by this (client) peer.
    init(dbPath: String, ownedTables: [String]) throws {
        let rc = sqlite3_open_v2(
            dbPath,
            &db,
            SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE | SQLITE_OPEN_NOMUTEX,
            nil
        )
        guard rc == SQLITE_OK, let db else {
            let msg = db.flatMap { String(cString: sqlite3_errmsg($0)) } ?? "unknown error"
            sqlite3_close(db)
            self.db = nil
            throw SyncPeerError.openFailed(msg)
        }

        // Enable WAL mode for better concurrent read/write performance.
        sqlite3_exec(db, "PRAGMA journal_mode=WAL", nil, nil, nil)

        var cfg = sqlpipe_peer_config()

        // Set up owned tables as C string array.
        let cStrings = ownedTables.map { strdup($0) }
        defer { cStrings.forEach { free($0) } }

        var cStringPtrs = cStrings.map { UnsafePointer($0) }
        try cStringPtrs.withUnsafeMutableBufferPointer { buf in
            cfg.owned_tables = buf.baseAddress
            cfg.owned_table_count = ownedTables.count

            // Schema mismatch callback — apply remote schema to local DB.
            // The remote_schema_sql contains semicolon-separated CREATE TABLE
            // statements from the server. We execute them to create any missing
            // tables, then return true to retry the handshake.
            cfg.on_schema_mismatch = { ctx, remoteSV, localSV, remoteSchemaSQL in
                guard let remoteSchemaSQL, let db = ctx?.assumingMemoryBound(to: OpaquePointer?.self).pointee else {
                    return 0
                }
                let sql = String(cString: remoteSchemaSQL)
                logger.info("Schema mismatch — applying remote schema (\(sql.count) chars)")
                for stmt in sql.components(separatedBy: ";") {
                    let trimmed = stmt.trimmingCharacters(in: .whitespacesAndNewlines)
                    guard !trimmed.isEmpty else { continue }
                    if sqlite3_exec(db, trimmed, nil, nil, nil) != SQLITE_OK {
                        let err = String(cString: sqlite3_errmsg(db))
                        logger.error("Schema apply failed: \(err) — stmt: \(trimmed.prefix(100))")
                    }
                }
                return 1  // retry
            }
            cfg.schema_mismatch_ctx = withUnsafeMutablePointer(to: &self.db) { UnsafeMutableRawPointer($0) }

            // Set up logging callback.
            cfg.on_log = { ctx, level, message in
                guard let message else { return }
                let msg = String(cString: message)
                switch level {
                case 0: logger.debug("sqlpipe: \(msg)")
                case 1: logger.info("sqlpipe: \(msg)")
                case 2: logger.warning("sqlpipe: \(msg)")
                default: logger.error("sqlpipe: \(msg)")
                }
            }

            var peerPtr: OpaquePointer?
            let err = sqlpipe_peer_new(db, cfg, &peerPtr)
            if err.code != 0 {
                let msg = err.msg.flatMap { String(cString: $0) } ?? "unknown error"
                sqlpipe_free_error(err)
                throw SyncPeerError.peerCreateFailed(msg)
            }
            self.peer = peerPtr
        }

        logger.info("SyncPeer created: \(dbPath), owned=\(ownedTables)")
    }

    deinit {
        // Inline cleanup — deinit is nonisolated so we can't call close().
        if let peer {
            sqlpipe_peer_free(peer)
        }
        if let db {
            sqlite3_close(db)
        }
    }

    /// Start the peer handshake. Returns serialized messages to send to the server.
    func start() -> Data? {
        guard let peer else { return nil }
        var buf = sqlpipe_buf()
        let err = sqlpipe_peer_start(peer, &buf)
        defer { sqlpipe_free_buf(buf) }
        if err.code != 0 {
            logError(err, context: "start")
            return nil
        }
        return stripCountPrefix(buf)
    }

    /// Handle an incoming binary message from the server.
    ///
    /// Returns response data to send back, whether changes occurred, and
    /// any updated subscription results.
    func handleMessage(_ data: Data) -> HandleResult {
        guard let peer else {
            return HandleResult(response: nil, hasChanges: false, subscriptions: [])
        }

        // The frame may contain multiple concatenated PeerMessages.
        // Each is [4B LE length][payload...]. Split and handle one at a time.
        var allResponse = Data()
        var anyChanges = false
        var allSubscriptions: [QueryResult] = []

        data.withUnsafeBytes { rawBuf in
            let bytes = rawBuf.bindMemory(to: UInt8.self)
            var offset = 0

            while offset + 4 <= bytes.count {
                let msgLen = Int(bytes[offset]) |
                             (Int(bytes[offset+1]) << 8) |
                             (Int(bytes[offset+2]) << 16) |
                             (Int(bytes[offset+3]) << 24)
                let total = 4 + msgLen
                guard offset + total <= bytes.count else { break }

                var outBuf = sqlpipe_buf()
                let ptr = bytes.baseAddress!.advanced(by: offset)
                let err = sqlpipe_peer_handle_message(peer, ptr, total, &outBuf)

                if err.code != 0 {
                    logError(err, context: "handleMessage")
                } else {
                    let decoded = decodePeerHandleResult(outBuf)
                    if let resp = decoded.messages {
                        allResponse.append(resp)
                    }
                    if decoded.changeCount > 0 {
                        anyChanges = true
                    }
                    allSubscriptions.append(contentsOf: decoded.subscriptions)
                }
                sqlpipe_free_buf(outBuf)
                offset += total
            }
        }

        return HandleResult(
            response: allResponse.isEmpty ? nil : allResponse,
            hasChanges: anyChanges,
            subscriptions: allSubscriptions
        )
    }

    /// Flush local changes and return serialized messages to send to the server.
    func flush() -> Data? {
        guard let peer else { return nil }
        var buf = sqlpipe_buf()
        let err = sqlpipe_peer_flush(peer, &buf)
        defer { sqlpipe_free_buf(buf) }
        if err.code != 0 {
            logError(err, context: "flush")
            return nil
        }
        return stripCountPrefix(buf)
    }

    /// Subscribe to a SQL query. Returns the current result set.
    /// The subscription fires on subsequent `handleMessage` calls when
    /// the result set changes.
    func subscribe(_ sql: String) -> QueryResult? {
        guard let peer else { return nil }
        var buf = sqlpipe_buf()
        let err = sqlpipe_peer_subscribe(peer, sql, &buf)
        defer { sqlpipe_free_buf(buf) }
        if err.code != 0 {
            logError(err, context: "subscribe(\(sql))")
            return nil
        }
        guard buf.data != nil, buf.len > 0 else { return nil }
        let bytes = UnsafeBufferPointer(start: buf.data, count: buf.len)
        var offset = 0
        return decodeQueryResult(bytes, offset: &offset)
    }

    /// Unsubscribe from a previously created subscription.
    func unsubscribe(_ id: UInt64) {
        guard let peer else { return }
        let err = sqlpipe_peer_unsubscribe(peer, id)
        if err.code != 0 {
            logError(err, context: "unsubscribe(\(id))")
        }
    }

    /// Execute a SQL statement on the local database (for client-owned tables).
    func execute(_ sql: String) throws {
        guard let db else { throw SyncPeerError.closed }
        var errMsg: UnsafeMutablePointer<CChar>?
        let rc = sqlite3_exec(db, sql, nil, nil, &errMsg)
        if rc != SQLITE_OK {
            let msg = errMsg.flatMap { String(cString: $0) } ?? "unknown error"
            sqlite3_free(errMsg)
            throw SyncPeerError.execFailed(msg)
        }
    }

    /// One-shot query on the local database.
    func query(_ sql: String) -> [[String: Any]]? {
        guard let db else { return nil }
        var stmt: OpaquePointer?
        guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else {
            logger.error("query prepare failed: \(String(cString: sqlite3_errmsg(db)))")
            return nil
        }
        defer { sqlite3_finalize(stmt) }

        var results: [[String: Any]] = []
        let colCount = sqlite3_column_count(stmt)

        while sqlite3_step(stmt) == SQLITE_ROW {
            var row: [String: Any] = [:]
            for i in 0..<colCount {
                let name = String(cString: sqlite3_column_name(stmt, i))
                switch sqlite3_column_type(stmt, i) {
                case SQLITE_INTEGER:
                    row[name] = sqlite3_column_int64(stmt, i)
                case SQLITE_FLOAT:
                    row[name] = sqlite3_column_double(stmt, i)
                case SQLITE_TEXT:
                    row[name] = String(cString: sqlite3_column_text(stmt, i))
                case SQLITE_BLOB:
                    let len = sqlite3_column_bytes(stmt, i)
                    if let ptr = sqlite3_column_blob(stmt, i) {
                        row[name] = Data(bytes: ptr, count: Int(len))
                    } else {
                        row[name] = NSNull()
                    }
                default:
                    row[name] = NSNull()
                }
            }
            results.append(row)
        }
        return results
    }

    /// Current peer state: 0=Init, 1=Negotiating, 2=Diffing, 3=Applying, 4=Live.
    var peerState: UInt8 {
        guard let peer else { return 0 }
        return sqlpipe_peer_state(peer)
    }

    /// Whether the peer has completed handshake and is live.
    var isLive: Bool { peerState == 4 }

    /// Reset the peer (e.g. on reconnect).
    func reset() {
        guard let peer else { return }
        sqlpipe_peer_reset(peer)
    }

    /// Close the peer and database.
    func close() {
        if let peer {
            sqlpipe_peer_free(peer)
            self.peer = nil
        }
        if let db {
            sqlite3_close(db)
            self.db = nil
        }
    }

    // MARK: - Binary decoding

    private struct DecodedPeerHandleResult {
        let messages: Data?
        let changeCount: Int
        let subscriptions: [QueryResult]
    }

    /// Decode a PeerHandleResult from the C API's binary format.
    ///
    /// Format: `[u32 msg_count][pmsgs...][u32 change_count][changes...][u32 sub_count][subs...]`
    private func decodePeerHandleResult(_ buf: sqlpipe_buf) -> DecodedPeerHandleResult {
        guard let data = buf.data, buf.len > 0 else {
            return DecodedPeerHandleResult(messages: nil, changeCount: 0, subscriptions: [])
        }
        let bytes = UnsafeBufferPointer(start: data, count: buf.len)
        var offset = 0

        // Messages — re-encode as [u32 count][pmsgs...] for sending over WebSocket.
        let msgCount = readU32(bytes, offset: &offset)
        let msgStart = offset
        // Skip over the serialized peer messages (each has a 4B length prefix).
        for _ in 0..<msgCount {
            guard offset + 4 <= bytes.count else { break }
            let msgLen = readU32(bytes, offset: &offset)
            offset += Int(msgLen)
        }
        let msgEnd = offset

        var responseData: Data?
        if msgCount > 0 {
            // Send bare concatenated PeerMessages (no count prefix).
            responseData = Data(bytes: bytes.baseAddress!.advanced(by: msgStart),
                                count: msgEnd - msgStart)
        }

        // Changes — just read the count, skip the encoded change events.
        let changeCount = readU32(bytes, offset: &offset)
        for _ in 0..<changeCount {
            skipChangeEvent(bytes, offset: &offset)
        }

        // Subscriptions.
        let subCount = readU32(bytes, offset: &offset)
        var subs: [QueryResult] = []
        for _ in 0..<subCount {
            if let qr = decodeQueryResult(bytes, offset: &offset) {
                subs.append(qr)
            }
        }

        return DecodedPeerHandleResult(
            messages: responseData,
            changeCount: Int(changeCount),
            subscriptions: subs
        )
    }

    private func decodeQueryResult(
        _ bytes: UnsafeBufferPointer<UInt8>,
        offset: inout Int
    ) -> QueryResult? {
        guard offset + 12 <= bytes.count else { return nil }

        let id = readU64(bytes, offset: &offset)
        let colCount = readU32(bytes, offset: &offset)

        var columns: [String] = []
        for _ in 0..<colCount {
            columns.append(readString(bytes, offset: &offset))
        }

        let rowCount = readU32(bytes, offset: &offset)
        var rows: [[SQLValue]] = []
        for _ in 0..<rowCount {
            var row: [SQLValue] = []
            for _ in 0..<colCount {
                row.append(readValue(bytes, offset: &offset))
            }
            rows.append(row)
        }

        return QueryResult(id: id, columns: columns, rows: rows)
    }

    private func readValue(
        _ bytes: UnsafeBufferPointer<UInt8>,
        offset: inout Int
    ) -> SQLValue {
        guard offset < bytes.count else { return .null }
        let tag = bytes[offset]
        offset += 1
        switch tag {
        case 0x00:
            return .null
        case 0x01:
            let v = readI64(bytes, offset: &offset)
            return .integer(v)
        case 0x02:
            let bits = readU64(bytes, offset: &offset)
            let v = Double(bitPattern: bits)
            return .real(v)
        case 0x03:
            return .text(readString(bytes, offset: &offset))
        case 0x04:
            let len = readU32(bytes, offset: &offset)
            guard offset + Int(len) <= bytes.count else { return .null }
            let data = Data(bytes: bytes.baseAddress!.advanced(by: offset), count: Int(len))
            offset += Int(len)
            return .blob(data)
        default:
            return .null
        }
    }

    /// Skip over an encoded ChangeEvent without fully decoding it.
    private func skipChangeEvent(
        _ bytes: UnsafeBufferPointer<UInt8>,
        offset: inout Int
    ) {
        // table (string)
        _ = readString(bytes, offset: &offset)
        // op (u8)
        offset += 1
        // pk_flags
        let pkCount = readU32(bytes, offset: &offset)
        offset += Int(pkCount) // each flag is 1 byte
        // old_values
        let oldCount = readU32(bytes, offset: &offset)
        for _ in 0..<oldCount { _ = readValue(bytes, offset: &offset) }
        // new_values
        let newCount = readU32(bytes, offset: &offset)
        for _ in 0..<newCount { _ = readValue(bytes, offset: &offset) }
    }

    // MARK: - Primitive readers (little-endian)

    private func readU32(
        _ bytes: UnsafeBufferPointer<UInt8>,
        offset: inout Int
    ) -> UInt32 {
        guard offset + 4 <= bytes.count else { return 0 }
        let v = UInt32(bytes[offset])
            | (UInt32(bytes[offset + 1]) << 8)
            | (UInt32(bytes[offset + 2]) << 16)
            | (UInt32(bytes[offset + 3]) << 24)
        offset += 4
        return v
    }

    private func readU64(
        _ bytes: UnsafeBufferPointer<UInt8>,
        offset: inout Int
    ) -> UInt64 {
        guard offset + 8 <= bytes.count else { return 0 }
        var v: UInt64 = 0
        for i in 0..<8 {
            v |= UInt64(bytes[offset + i]) << (i * 8)
        }
        offset += 8
        return v
    }

    private func readI64(
        _ bytes: UnsafeBufferPointer<UInt8>,
        offset: inout Int
    ) -> Int64 {
        Int64(bitPattern: readU64(bytes, offset: &offset))
    }

    private func readString(
        _ bytes: UnsafeBufferPointer<UInt8>,
        offset: inout Int
    ) -> String {
        let len = readU32(bytes, offset: &offset)
        guard offset + Int(len) <= bytes.count else { return "" }
        let str = String(
            bytes: UnsafeBufferPointer(
                start: bytes.baseAddress!.advanced(by: offset),
                count: Int(len)
            ),
            encoding: .utf8
        ) ?? ""
        offset += Int(len)
        return str
    }

    // MARK: - Helpers

    private func bufToData(_ buf: sqlpipe_buf) -> Data? {
        guard let data = buf.data, buf.len > 0 else { return nil }
        return Data(bytes: data, count: buf.len)
    }

    /// Strip the [u32 count] prefix from a C API buffer that returns
    /// [u32 count][pmsg1][pmsg2]..., returning just the concatenated messages.
    private func stripCountPrefix(_ buf: sqlpipe_buf) -> Data? {
        guard let data = buf.data, buf.len > 4 else { return nil }
        return Data(bytes: data.advanced(by: 4), count: buf.len - 4)
    }

    private func logError(_ err: sqlpipe_error, context: String) {
        let msg = err.msg.flatMap { String(cString: $0) } ?? "code \(err.code)"
        logger.error("SyncPeer.\(context) failed: \(msg)")
        sqlpipe_free_error(err)
    }
}

// MARK: - Errors

enum SyncPeerError: LocalizedError {
    case openFailed(String)
    case peerCreateFailed(String)
    case execFailed(String)
    case closed

    var errorDescription: String? {
        switch self {
        case .openFailed(let msg): return "Failed to open database: \(msg)"
        case .peerCreateFailed(let msg): return "Failed to create peer: \(msg)"
        case .execFailed(let msg): return "SQL exec failed: \(msg)"
        case .closed: return "SyncPeer is closed"
        }
    }
}
