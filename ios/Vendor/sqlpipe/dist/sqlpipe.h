// Copyright 2026 The sqlpipe Authors
// SPDX-License-Identifier: Apache-2.0
#pragma once

#define SQLPIPE_VERSION       "0.12.0"
#define SQLPIPE_VERSION_MAJOR 0
#define SQLPIPE_VERSION_MINOR 12
#define SQLPIPE_VERSION_PATCH 0

#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <set>
#include <span>
#include <sqlite3.h>
#include <stdexcept>
#include <string>
#include <variant>
#include <vector>

// ── types.h ─────────────────────────────────────────────────────
namespace sqlpipe {

/// Monotonically increasing sequence number for changesets.
using Seq = std::int64_t;

/// Schema version tracked by PRAGMA schema_version.
using SchemaVersion = std::int32_t;

/// A byte buffer holding a raw SQLite changeset blob.
using Changeset = std::vector<std::uint8_t>;

/// Column value. Uses std::variant to represent SQLite types.
using Value = std::variant<
    std::monostate,            // NULL
    std::int64_t,              // INTEGER
    double,                    // REAL
    std::string,               // TEXT
    std::vector<std::uint8_t>  // BLOB
>;

/// The type of row operation.
enum class OpType : std::uint8_t {
    Insert = 1,
    Update = 2,
    Delete = 3,
};

/// A single row-level change extracted from a changeset.
struct ChangeEvent {
    std::string        table;
    OpType             op;
    std::vector<bool>  pk_flags;    // true for PK columns
    std::vector<Value> old_values;  // populated for UPDATE, DELETE
    std::vector<Value> new_values;  // populated for INSERT, UPDATE
};

/// Conflict resolution action returned by ConflictCallback.
enum class ConflictAction : std::uint8_t {
    Omit,      ///< Skip this change; the conflicting row is left as-is.
    Replace,   ///< Overwrite the conflicting row with the incoming change.
    Abort,     ///< Abort the entire changeset application.
};

/// Conflict type reported during changeset application.
enum class ConflictType : std::uint8_t {
    Data,        ///< A directly conflicting change (different values for same row).
    NotFound,    ///< The row to update/delete was not found on the replica.
    Conflict,    ///< A row with the same PK already exists (on INSERT).
    Constraint,  ///< A constraint (UNIQUE, NOT NULL, CHECK) would be violated.
    ForeignKey,  ///< A foreign key constraint would be violated.
};

/// Callback for conflict resolution on the replica side.
using ConflictCallback = std::function<ConflictAction(
    ConflictType type, const ChangeEvent& event)>;

/// Default bucket size for the diff protocol (rows per bucket).
inline constexpr std::int64_t kDefaultBucketSize = 1024;

/// Maximum serialized message size accepted by deserialize() (64 MB).
inline constexpr std::size_t kMaxMessageSize = 64 * 1024 * 1024;

/// Maximum number of elements in a deserialized array (10 M).
inline constexpr std::uint32_t kMaxArrayCount = 10'000'000;

/// Opaque handle for a query subscription.
using SubscriptionId = std::uint64_t;

/// Full result set of a subscribed query.
struct QueryResult {
    SubscriptionId                  id;
    std::vector<std::string>        columns;  ///< Column names.
    std::vector<std::vector<Value>> rows;     ///< Result rows.
};

// ── Diff sync progress ──────────────────────────────────────────

/// Phase of the diff sync protocol for progress reporting.
enum class DiffPhase : std::uint8_t {
    ComputingBuckets,   ///< Computing bucket hashes for tables.
    ComparingBuckets,   ///< Comparing bucket hashes (master only).
    ComputingRowHashes, ///< Computing per-row hashes for mismatched buckets.
    BuildingPatchset,   ///< Building INSERT patchset (master only).
    ApplyingPatchset,   ///< Applying patchset + deletes (replica only).
};

/// Progress information emitted during diff sync.
struct DiffProgress {
    DiffPhase    phase;
    std::string  table;           ///< Current table (empty if N/A).
    std::int64_t items_done = 0;  ///< Buckets/rows/tables processed so far.
    std::int64_t items_total = 0; ///< Total items (0 if unknown).
};

/// Callback for diff sync progress reporting.
using ProgressCallback = std::function<void(const DiffProgress&)>;

// ── Logging ────────────────────────────────────────────────────────

/// Log severity level.
enum class LogLevel : uint8_t { Debug = 0, Info, Warn, Error };

/// Callback for library log output. If not set, logs are silently discarded.
using LogCallback = std::function<void(LogLevel level, std::string_view message)>;


/// Called when a schema mismatch is detected during handshake.
/// Receives (remote_fingerprint, local_fingerprint, remote_schema_sql).
/// remote_schema_sql contains the remote side's sorted CREATE TABLE statements
/// (semicolon-separated). On the master side, this is empty because the replica
/// only sends a fingerprint in HelloMsg, not its full schema.
/// The callback may ALTER the local database to resolve the mismatch.
/// Return true to recompute fingerprints and retry; false to proceed
/// with the default behaviour (ErrorMsg on master, Error state on replica).
using SchemaMismatchCallback = std::function<bool(
    SchemaVersion remote_sv, SchemaVersion local_sv,
    const std::string& remote_schema_sql)>;

/// Execute a one-shot SQL query and return the result set.
/// The returned QueryResult has id = 0 (not a subscription).
/// Throws Error on SQL errors.
QueryResult query(sqlite3* db, const std::string& sql);

} // namespace sqlpipe

// ── query_watch.h ───────────────────────────────────────────────
namespace sqlpipe {

/// Standalone query subscription engine for a single sqlite3* handle.
///
/// Monitors registered SQL queries and detects when their results change.
/// Result-change detection uses FNV-1a hashing — queries are only reported
/// as changed when the actual result set differs, not on every call to
/// notify().
///
/// Usage:
///   QueryWatch w(db);
///   auto result = w.subscribe("SELECT * FROM items WHERE active = 1");
///   // ... perform writes ...
///   auto changed = w.notify({"items"});  // re-evaluates affected queries
///
/// Does NOT own the sqlite3* handle.
class QueryWatch {
public:
    explicit QueryWatch(sqlite3* db);
    ~QueryWatch();

    QueryWatch(const QueryWatch&) = delete;
    QueryWatch& operator=(const QueryWatch&) = delete;
    QueryWatch(QueryWatch&&) noexcept;
    QueryWatch& operator=(QueryWatch&&) noexcept;

    /// Register a query subscription. Returns the subscription ID.
    /// Results arrive via notify() — subscribe does not evaluate the query.
    SubscriptionId subscribe(const std::string& sql);

    /// Remove a subscription.
    void unsubscribe(SubscriptionId id);

    /// Re-evaluate subscriptions that depend on any of the given tables.
    /// Returns results only for subscriptions whose output actually changed.
    std::vector<QueryResult> notify(const std::set<std::string>& affected_tables);

    /// Returns true if there are no active subscriptions.
    bool empty() const;

private:
    struct Impl;
    std::unique_ptr<Impl> impl_;
};

} // namespace sqlpipe

// ── error.h ─────────────────────────────────────────────────────
namespace sqlpipe {

/// Error codes returned by sqlpipe operations.
enum class ErrorCode : int {
    Ok = 0,
    SqliteError,        ///< An underlying SQLite call failed.
    ProtocolError,      ///< Malformed or unexpected message.
    SchemaMismatch,     ///< Master and replica schemas differ.
    InvalidState,       ///< Operation not valid in the current state.
    OwnershipRejected,  ///< Peer ownership request was rejected by the server.
    WithoutRowidTable,  ///< Table uses WITHOUT ROWID (not supported).
};

/// Exception thrown by sqlpipe operations.
class Error : public std::runtime_error {
public:
    Error(ErrorCode code, const std::string& msg)
        : std::runtime_error(msg), code_(code) {}

    ErrorCode code() const noexcept { return code_; }

private:
    ErrorCode code_;
};

} // namespace sqlpipe

// ── protocol.h ──────────────────────────────────────────────────
namespace sqlpipe {

inline constexpr std::uint32_t kProtocolVersion = 5;

// ── Message types ───────────────────────────────────────────────────

/// Sent by both sides during handshake to exchange schema state.
struct HelloMsg {
    std::uint32_t protocol_version;  ///< Must match kProtocolVersion.
    SchemaVersion schema_version;    ///< Sender's schema fingerprint.
    std::set<std::string> owned_tables;  ///< Tables the sender wants to own (Peer mode).
};

/// A single changeset (one flush() worth of changes). Used in live streaming.
struct ChangesetMsg {
    Seq       seq;   ///< Sequence number assigned by the master.
    Changeset data;  ///< Raw SQLite changeset blob.
};

/// Acknowledgement sent by the replica after applying a changeset.
struct AckMsg {
    Seq seq;  ///< The sequence number that was applied.
};

/// Protocol-level error. Receiving side should transition to Error state.
struct ErrorMsg {
    ErrorCode     code{};                      ///< Machine-readable error category.
    std::string   detail;                      ///< Human-readable description.
    SchemaVersion remote_schema_version = 0;   ///< Remote fingerprint (SchemaMismatch only).
    std::string   remote_schema_sql;           ///< Remote CREATE TABLE SQL (SchemaMismatch only).

    ErrorMsg() = default;
    ErrorMsg(ErrorCode c, std::string d,
             SchemaVersion rsv = 0, std::string rsql = {})
        : code(c), detail(std::move(d)),
          remote_schema_version(rsv), remote_schema_sql(std::move(rsql)) {}
};

// ── Diff protocol messages ──────────────────────────────────────────

/// One bucket's hash in BucketHashesMsg.
struct BucketHashEntry {
    std::string   table;
    std::int64_t  bucket_lo;     ///< Inclusive rowid lower bound.
    std::int64_t  bucket_hi;     ///< Inclusive rowid upper bound.
    std::uint64_t hash;          ///< XOR of fnv1a(rowid||row_hash) per row.
    std::int64_t  row_count;     ///< Number of rows in this bucket.
};

/// Sent by replica after receiving master's HelloMsg.
/// Contains per-table bucket hashes for the diff protocol.
struct BucketHashesMsg {
    std::vector<BucketHashEntry> buckets;
};

/// One bucket range the master needs row-level detail for.
struct NeedBucketRange {
    std::string  table;
    std::int64_t lo;
    std::int64_t hi;
};

/// Sent by master after comparing bucket hashes.
/// Lists the buckets that differ and need row-level detail.
struct NeedBucketsMsg {
    std::vector<NeedBucketRange> ranges;
};

/// A contiguous run of rowids with their hashes.
struct RowHashRun {
    std::int64_t               start_rowid;  ///< First rowid in the run.
    std::int64_t               count;        ///< Number of contiguous rowids.
    std::vector<std::uint64_t> hashes;       ///< One hash per rowid.
};

/// Row hashes for one bucket in RowHashesMsg.
struct RowHashesEntry {
    std::string              table;
    std::int64_t             lo;     ///< Bucket rowid lower bound.
    std::int64_t             hi;     ///< Bucket rowid upper bound.
    std::vector<RowHashRun>  runs;   ///< Run-length encoded row hashes.
};

/// Sent by replica with per-row hashes for requested buckets.
struct RowHashesMsg {
    std::vector<RowHashesEntry> entries;
};

/// Per-table list of rowids to delete.
struct TableDeletes {
    std::string               table;
    std::vector<std::int64_t> rowids;
};

/// Sent by master with the computed diff. Carries an INSERT patchset for
/// rows to add/update and per-table rowid lists for rows to delete.
struct DiffReadyMsg {
    Seq                        seq;       ///< Master's current seq.
    Changeset                  patchset;  ///< INSERT records for insert+update.
    std::vector<TableDeletes>  deletes;   ///< Rowids to delete per table.
};

using Message = std::variant<
    HelloMsg,
    ChangesetMsg,
    AckMsg,
    ErrorMsg,
    BucketHashesMsg,
    NeedBucketsMsg,
    RowHashesMsg,
    DiffReadyMsg
>;

// ── Wire format tags ────────────────────────────────────────────────

enum class MessageTag : std::uint8_t {
    Hello        = 0x01,
    Changeset    = 0x03,
    Ack          = 0x08,
    Error        = 0x09,
    BucketHashes = 0x0A,
    NeedBuckets  = 0x0B,
    RowHashes    = 0x0C,
    DiffReady    = 0x0D,
};

// ── Serialization ───────────────────────────────────────────────────

/// Serialize a Message to a length-prefixed byte buffer.
/// Format: [4-byte total_length LE][1-byte tag][payload...]
std::vector<std::uint8_t> serialize(const Message& msg);

/// Deserialize a byte buffer (including the 4-byte length prefix) into a Message.
Message deserialize(std::span<const std::uint8_t> buf);

/// Callback invoked automatically when a write transaction commits on a
/// Master's database. Receives the changeset messages ready to send.
/// When set, the Master hooks into SQLite's commit notification so that
/// callers never need to call flush() explicitly.
using FlushCallback = std::function<void(const std::vector<Message>&)>;

} // namespace sqlpipe

// ── master.h ────────────────────────────────────────────────────
namespace sqlpipe {

struct MasterConfig {
    /// If set, only track these tables. nullopt = track all.
    /// An empty set means track nothing.
    std::optional<std::set<std::string>> table_filter;

    /// Meta-table key for storing the sequence number.
    std::string seq_key = "seq";

    /// Rows per bucket for the diff protocol.
    std::int64_t bucket_size = kDefaultBucketSize;

    /// Diff sync progress callback. nullptr = no reporting.
    ProgressCallback on_progress = nullptr;

    /// Called when a replica's schema fingerprint doesn't match.
    /// The callback may ALTER the local DB to resolve the mismatch,
    /// then return true to recompute and retry the comparison.
    /// nullptr or returning false = send ErrorMsg (default behaviour).
    SchemaMismatchCallback on_schema_mismatch = nullptr;

    /// Log callback. nullptr = discard all log output.
    LogCallback on_log = nullptr;

    /// Automatic flush callback. When set, the Master hooks into SQLite's
    /// commit notification and delivers changeset messages automatically
    /// after each write transaction. No explicit flush() needed.
    /// nullptr = manual flush only (caller must call flush()).
    FlushCallback on_flush = nullptr;
};

/// The sending side of the replication protocol.
///
/// Does NOT own the sqlite3* handle. Caller must keep it open for
/// the Master's lifetime.
class Master {
public:
    explicit Master(sqlite3* db, MasterConfig config = {});
    ~Master();

    Master(const Master&) = delete;
    Master& operator=(const Master&) = delete;
    Master(Master&&) noexcept;
    Master& operator=(Master&&) noexcept;

    /// Execute SQL on the master's database. If on_flush is set, any
    /// committed changes are automatically delivered via the callback.
    void exec(const std::string& sql);

    /// Call after committing a write transaction. Extracts the changeset,
    /// assigns a sequence number, and returns the messages to send to
    /// connected replicas. Returns empty if nothing changed.
    std::vector<Message> flush();

    /// Process an incoming message from a replica.
    std::vector<Message> handle_message(const Message& msg);

    Seq current_seq() const;
    SchemaVersion schema_version() const;

private:
    struct Impl;
    std::unique_ptr<Impl> impl_;
};

} // namespace sqlpipe

// ── replica.h ───────────────────────────────────────────────────
namespace sqlpipe {

struct ReplicaConfig {
    /// Called when a conflict occurs during changeset application.
    /// Default (nullptr): ConflictAction::Abort.
    ConflictCallback on_conflict = nullptr;

    /// If set, only manage these tables during diff sync.
    /// nullopt = all tracked tables (default). Empty set = nothing.
    std::optional<std::set<std::string>> table_filter;

    /// Meta-table key for storing the sequence number.
    std::string seq_key = "seq";

    /// Rows per bucket for the diff protocol.
    std::int64_t bucket_size = kDefaultBucketSize;

    /// Diff sync progress callback. nullptr = no reporting.
    ProgressCallback on_progress = nullptr;

    /// Called when a SchemaMismatch error is received from the master.
    /// The callback may ALTER the local DB to match the master's schema,
    /// then return true to auto-reset to Init state (caller should retry
    /// the handshake). nullptr or returning false = transition to Error
    /// state (default behaviour).
    SchemaMismatchCallback on_schema_mismatch = nullptr;

    /// Log callback. nullptr = discard all log output.
    LogCallback on_log = nullptr;
};

/// Return type for Replica::handle_message.
struct HandleResult {
    std::vector<Message>      messages;       ///< Protocol responses to send back.
    std::vector<ChangeEvent>  changes;        ///< Row-level changes applied this call.
    std::vector<QueryResult>  subscriptions;  ///< Invalidated subscription results.
};

/// The receiving side of the replication protocol.
///
/// Does NOT own the sqlite3* handle. Caller must keep it open for
/// the Replica's lifetime.
class Replica {
public:
    explicit Replica(sqlite3* db, ReplicaConfig config = {});
    ~Replica();

    Replica(const Replica&) = delete;
    Replica& operator=(const Replica&) = delete;
    Replica(Replica&&) noexcept;
    Replica& operator=(Replica&&) noexcept;

    /// Generate the initial HelloMsg to send to the master.
    Message hello() const;

    /// Process an incoming message from the master.
    HandleResult handle_message(const Message& msg);

    /// Process multiple messages, deferring subscription evaluation until
    /// all are applied. More efficient than calling handle_message() in a
    /// loop when processing a burst of messages.
    HandleResult handle_messages(std::span<const Message> msgs);

    /// Subscribe to a SQL query. Returns the subscription ID.
    /// The initial result and subsequent updates arrive via
    /// HandleResult::subscriptions — subscribe does not evaluate the query.
    /// Before Live state, evaluation is deferred until sync completes.
    /// After Live, the subscription is evaluated on the next handle_message.
    SubscriptionId subscribe(const std::string& sql);

    /// Remove a subscription.
    void unsubscribe(SubscriptionId id);

    /// Reset to Init state for reconnection. Subscriptions are preserved;
    /// they will re-evaluate after the next handshake applies changes.
    void reset();

    Seq current_seq() const;
    SchemaVersion schema_version() const;

    /// Replica connection lifecycle state.
    enum class State : std::uint8_t {
        Init,        ///< Created but hello() not yet called.
        Handshake,   ///< hello() sent, awaiting master's response.
        DiffBuckets, ///< Sent bucket hashes, awaiting NeedBucketsMsg.
        DiffRows,    ///< Sent row hashes (or skipped), awaiting DiffReadyMsg.
        Live,        ///< Streaming; ready for real-time changesets.
        Error,       ///< A protocol or application error occurred.
    };

    State state() const;

private:
    struct Impl;
    std::unique_ptr<Impl> impl_;
};

} // namespace sqlpipe

// ── peer.h ──────────────────────────────────────────────────────
namespace sqlpipe {

/// Server-side callback to approve or reject a client's ownership request.
/// Receives the set of tables the client wants to own.
/// Returns true to approve, false to reject.
using ApproveOwnershipCallback = std::function<bool(
    const std::set<std::string>& requested_tables)>;

struct PeerConfig {
    /// Tables this peer wants to own (client-side: sent in hello).
    /// Ignored on the server side (computed as complement of client's).
    std::set<std::string> owned_tables;

    /// If set, only consider these tables for replication.
    /// nullopt = all user tables (default). owned_tables must be a subset.
    /// Complement (remote tables) is computed within this filtered set.
    std::optional<std::set<std::string>> table_filter;

    /// Server-side ownership validation callback.
    /// Non-null indicates this peer is the server.
    /// nullptr = auto-approve any request.
    ApproveOwnershipCallback approve_ownership = nullptr;

    /// Conflict callback for the internal Replica.
    ConflictCallback on_conflict = nullptr;

    /// Diff sync progress callback. nullptr = no reporting.
    /// Forwarded to both the internal Master and Replica.
    ProgressCallback on_progress = nullptr;

    /// Called when a schema mismatch is detected during handshake.
    /// Forwarded to both the internal Master and Replica.
    SchemaMismatchCallback on_schema_mismatch = nullptr;

    /// Log callback. nullptr = discard all log output.
    /// Forwarded to both the internal Master and Replica.
    LogCallback on_log = nullptr;
};

/// Identifies whether the sender was acting as master or replica.
enum class SenderRole : std::uint8_t {
    AsMaster  = 0,  ///< Sender is acting as master (replicating owned tables).
    AsReplica = 1,  ///< Sender is acting as replica (acks/hellos for other side).
};

/// A protocol message with a directional tag for peer-to-peer routing.
struct PeerMessage {
    SenderRole sender_role;
    Message    payload;
};

/// Return type for Peer::handle_message.
struct PeerHandleResult {
    std::vector<PeerMessage>  messages;       ///< Protocol responses to send back.
    std::vector<ChangeEvent>  changes;        ///< Row-level changes applied.
    std::vector<QueryResult>  subscriptions;  ///< Invalidated subscription results.
};

/// Bidirectional replication peer.
///
/// Wraps a Master (for owned tables) and a Replica (for the remote peer's
/// tables) behind a symmetric API.
///
/// Does NOT own the sqlite3* handle.
class Peer {
public:
    explicit Peer(sqlite3* db, PeerConfig config = {});
    ~Peer();

    Peer(const Peer&) = delete;
    Peer& operator=(const Peer&) = delete;
    Peer(Peer&&) noexcept;
    Peer& operator=(Peer&&) noexcept;

    /// Initiate the handshake (client only).
    /// Returns PeerMessages to send to the remote peer.
    std::vector<PeerMessage> start();

    /// Flush local changes on owned tables.
    /// Returns PeerMessages to send to the remote peer.
    std::vector<PeerMessage> flush();

    /// Process an incoming PeerMessage from the remote peer.
    PeerHandleResult handle_message(const PeerMessage& msg);

    /// Subscribe to a SQL query on the replica side. Returns the
    /// subscription ID. Results arrive via PeerHandleResult::subscriptions.
    SubscriptionId subscribe(const std::string& sql);

    /// Remove a subscription.
    void unsubscribe(SubscriptionId id);

    /// Reset to Init state for reconnection. Call start() again to
    /// re-handshake. Table ownership is preserved from the previous session.
    void reset();

    /// Peer lifecycle state.
    enum class State : std::uint8_t {
        Init,        ///< Created, not yet started.
        Negotiating, ///< Ownership negotiation in progress.
        Diffing,     ///< Diff sync in progress.
        Live,        ///< Both directions are live.
        Error,       ///< A protocol or application error occurred.
    };

    State state() const;

    /// The tables this peer owns (mastering).
    const std::set<std::string>& owned_tables() const;

    /// The tables the remote peer owns (replicating).
    const std::set<std::string>& remote_tables() const;

private:
    struct Impl;
    std::unique_ptr<Impl> impl_;
};

/// Serialize a PeerMessage to a length-prefixed byte buffer.
/// Format: [4B LE length][1B sender_role][1B tag][payload...]
std::vector<std::uint8_t> serialize(const PeerMessage& msg);

/// Deserialize a byte buffer into a PeerMessage.
PeerMessage deserialize_peer(std::span<const std::uint8_t> buf);

// ── Convenience utilities ───────────────────────────────────────

/// Drive the Master/Replica handshake protocol to completion when both
/// are in the same process. Exchanges messages until no more remain.
void sync_handshake(Master& master, Replica& replica);

/// Drive the Peer handshake protocol to completion when both peers are
/// in the same process. The client initiates; messages are exchanged
/// until both peers reach Live state or no more messages remain.
void sync_handshake(Peer& client, Peer& server);

} // namespace sqlpipe
