// Copyright 2026 The sqlpipe Authors
// SPDX-License-Identifier: Apache-2.0
#include "sqlpipe.h"
#include <sqlift.h>

#include <algorithm>
#include <cassert>
#include <cstring>
#include <map>
#include <format>
#include <lz4.h>
#include <unordered_map>
#include <unordered_set>

// ── Logging macro ───────────────────────────────────────────────
#define SQLPIPE_LOG(cb, level, ...) \
    do { if (cb) (cb)(level, std::format(__VA_ARGS__)); } while(0)

// ── sqlite_util.h ───────────────────────────────────────────────
namespace sqlpipe::detail {

/// RAII wrapper for sqlite3_stmt*.
class StmtGuard {
public:
    StmtGuard() = default;
    explicit StmtGuard(sqlite3_stmt* s) : stmt_(s) {}
    ~StmtGuard() { if (stmt_) sqlite3_finalize(stmt_); }

    StmtGuard(const StmtGuard&) = delete;
    StmtGuard& operator=(const StmtGuard&) = delete;
    StmtGuard(StmtGuard&& o) noexcept : stmt_(o.stmt_) { o.stmt_ = nullptr; }
    StmtGuard& operator=(StmtGuard&& o) noexcept {
        if (this != &o) {
            if (stmt_) sqlite3_finalize(stmt_);
            stmt_ = o.stmt_;
            o.stmt_ = nullptr;
        }
        return *this;
    }

    sqlite3_stmt* get() const { return stmt_; }

    sqlite3_stmt* release() {
        auto* s = stmt_;
        stmt_ = nullptr;
        return s;
    }

private:
    sqlite3_stmt* stmt_ = nullptr;
};

/// Execute SQL or throw.
inline void exec(sqlite3* db, const char* sql) {
    char* err = nullptr;
    int rc = sqlite3_exec(db, sql, nullptr, nullptr, &err);
    if (rc != SQLITE_OK) {
        std::string msg = err ? err : "unknown error";
        sqlite3_free(err);
        throw Error(ErrorCode::SqliteError, msg);
    }
}

/// Prepare a statement or throw.
inline StmtGuard prepare(sqlite3* db, const char* sql) {
    sqlite3_stmt* stmt = nullptr;
    int rc = sqlite3_prepare_v2(db, sql, -1, &stmt, nullptr);
    if (rc != SQLITE_OK) {
        throw Error(ErrorCode::SqliteError, sqlite3_errmsg(db));
    }
    return StmtGuard(stmt);
}

/// Step a statement expecting SQLITE_DONE, or throw.
inline void step_done(sqlite3* db, sqlite3_stmt* stmt) {
    int rc = sqlite3_step(stmt);
    if (rc != SQLITE_DONE) {
        throw Error(ErrorCode::SqliteError, sqlite3_errmsg(db));
    }
}

} // namespace sqlpipe::detail

// ── session_guard.h ─────────────────────────────────────────────
namespace sqlpipe::detail {

/// RAII wrapper for sqlite3_session*.
class SessionGuard {
public:
    SessionGuard() = default;
    explicit SessionGuard(sqlite3_session* s) : session_(s) {}
    ~SessionGuard() { if (session_) sqlite3session_delete(session_); }

    SessionGuard(const SessionGuard&) = delete;
    SessionGuard& operator=(const SessionGuard&) = delete;
    SessionGuard(SessionGuard&& o) noexcept : session_(o.session_) { o.session_ = nullptr; }
    SessionGuard& operator=(SessionGuard&& o) noexcept {
        if (this != &o) {
            if (session_) sqlite3session_delete(session_);
            session_ = o.session_;
            o.session_ = nullptr;
        }
        return *this;
    }

    sqlite3_session* get() const { return session_; }

    sqlite3_session* release() {
        auto* s = session_;
        session_ = nullptr;
        return s;
    }

private:
    sqlite3_session* session_ = nullptr;
};

} // namespace sqlpipe::detail

// ── meta.h ──────────────────────────────────────────────────────
namespace sqlpipe::detail {

/// Create _sqlpipe_meta table if it doesn't exist.
void ensure_meta_table(sqlite3* db);

/// Read the current sequence number from _sqlpipe_meta.
Seq read_seq(sqlite3* db, const std::string& key = "seq");

/// Write the sequence number to _sqlpipe_meta.
void write_seq(sqlite3* db, Seq seq, const std::string& key = "seq");

/// Compute a fingerprint of the user table definitions (excludes internal tables).
/// Uses FNV-1a over the sorted CREATE TABLE SQL.
/// If filter is non-null, only include tables in the filter.
SchemaVersion compute_schema_fingerprint(
    sqlite3* db, const std::set<std::string>* filter = nullptr);

/// Get all user table names (excludes _sqlpipe_* and sqlite_* tables).
/// Only includes tables with explicit PRIMARY KEYs.
/// Rejects WITHOUT ROWID tables.
/// If filter is non-null, only include tables in the filter.
std::vector<std::string> get_tracked_tables(
    sqlite3* db, const std::set<std::string>* filter = nullptr,
    const LogCallback* on_log = nullptr);

/// Get the CREATE TABLE SQL for all tracked user tables.
/// If filter is non-null, only include tables in the filter.
std::string get_schema_sql(
    sqlite3* db, const std::set<std::string>* filter = nullptr);

/// Get the CREATE TABLE SQL for a single table.
std::string get_table_create_sql(sqlite3* db, const std::string& table);

/// Check if a table uses WITHOUT ROWID.
bool is_without_rowid(sqlite3* db, const std::string& table);

} // namespace sqlpipe::detail

// ── hash.h ──────────────────────────────────────────────────────
namespace sqlpipe::detail {

/// 64-bit FNV-1a hash of a row's column values (type-tagged).
std::uint64_t hash_row(sqlite3_stmt* stmt, int ncols);

/// Hash a (rowid, row_hash) pair for bucket accumulation.
std::uint64_t hash_bucket_entry(std::int64_t rowid, std::uint64_t row_hash);

struct RowHashInfo {
    std::int64_t  rowid;
    std::uint64_t hash;
};

/// Compute per-row hashes for rows in [lo, hi] rowid range.
std::vector<RowHashInfo> compute_row_hashes(
    sqlite3* db, const std::string& table,
    std::int64_t lo, std::int64_t hi);

struct BucketInfo {
    std::int64_t  lo, hi;
    std::uint64_t hash;
    std::int64_t  count;
};

/// Compute bucket hashes for a single table.
std::vector<BucketInfo> compute_table_buckets(
    sqlite3* db, const std::string& table, std::int64_t bucket_size);

/// Compute bucket hashes for all tracked tables, respecting filter.
std::vector<BucketHashEntry> compute_all_buckets(
    sqlite3* db, const std::set<std::string>* filter,
    std::int64_t bucket_size);

/// Build an INSERT patchset for specific rowids in a table.
Changeset build_insert_patchset(
    sqlite3* db, const std::string& table,
    const std::vector<std::int64_t>& rowids);

/// Combine multiple patchsets into one via sqlite3changegroup.
Changeset combine_patchsets(const std::vector<Changeset>& parts);

} // namespace sqlpipe::detail

// ── changeset_iter.h ────────────────────────────────────────────
namespace sqlpipe::detail {

/// Convert a sqlite3_value* to our Value variant.
Value to_value(sqlite3_value* val);

/// Convert an SQLite op code (SQLITE_INSERT/UPDATE/DELETE) to OpType.
OpType sqlite_op_to_optype(int sqlite_op);

/// Collect all row-level changes from a changeset into a vector.
std::vector<ChangeEvent> collect_events(const Changeset& data);

/// Build a ChangeEvent from a changeset iterator at its current position.
ChangeEvent extract_event(sqlite3_changeset_iter* iter);

} // namespace sqlpipe::detail

// ── protocol.cpp ────────────────────────────────────────────────
namespace sqlpipe {

namespace {

// ── Little-endian encoding helpers ──────────────────────────────────

void put_u8(std::vector<std::uint8_t>& buf, std::uint8_t v) {
    buf.push_back(v);
}

void put_u32(std::vector<std::uint8_t>& buf, std::uint32_t v) {
    buf.push_back(static_cast<std::uint8_t>(v));
    buf.push_back(static_cast<std::uint8_t>(v >> 8));
    buf.push_back(static_cast<std::uint8_t>(v >> 16));
    buf.push_back(static_cast<std::uint8_t>(v >> 24));
}

void put_i32(std::vector<std::uint8_t>& buf, std::int32_t v) {
    put_u32(buf, static_cast<std::uint32_t>(v));
}

void put_i64(std::vector<std::uint8_t>& buf, std::int64_t v) {
    auto u = static_cast<std::uint64_t>(v);
    for (int i = 0; i < 8; ++i) {
        buf.push_back(static_cast<std::uint8_t>(u >> (i * 8)));
    }
}

void put_u64(std::vector<std::uint8_t>& buf, std::uint64_t v) {
    for (int i = 0; i < 8; ++i) {
        buf.push_back(static_cast<std::uint8_t>(v >> (i * 8)));
    }
}

void put_bytes(std::vector<std::uint8_t>& buf,
               const void* data, std::uint32_t len) {
    put_u32(buf, len);
    auto* p = static_cast<const std::uint8_t*>(data);
    buf.insert(buf.end(), p, p + len);
}

void put_string(std::vector<std::uint8_t>& buf, const std::string& s) {
    put_bytes(buf, s.data(), static_cast<std::uint32_t>(s.size()));
}

void put_changeset(std::vector<std::uint8_t>& buf, const Changeset& cs) {
    constexpr std::size_t kCompressionThreshold = 64;
    if (cs.size() < kCompressionThreshold) {
        // Uncompressed: [u32 total] [0x00] [raw_data]
        put_u32(buf, static_cast<std::uint32_t>(cs.size() + 1));
        put_u8(buf, 0x00);
        buf.insert(buf.end(), cs.begin(), cs.end());
    } else {
        int src_size = static_cast<int>(cs.size());
        int max_dst = LZ4_compressBound(src_size);
        std::vector<std::uint8_t> tmp(max_dst);
        int compressed_size = LZ4_compress_default(
            reinterpret_cast<const char*>(cs.data()),
            reinterpret_cast<char*>(tmp.data()),
            src_size, max_dst);
        if (compressed_size > 0 &&
            compressed_size < static_cast<int>(cs.size())) {
            // LZ4: [u32 total] [0x01] [u32 original_len] [compressed_data]
            put_u32(buf, static_cast<std::uint32_t>(1 + 4 + compressed_size));
            put_u8(buf, 0x01);
            put_u32(buf, static_cast<std::uint32_t>(cs.size()));
            buf.insert(buf.end(), tmp.begin(),
                       tmp.begin() + compressed_size);
        } else {
            // Fallback: uncompressed
            put_u32(buf, static_cast<std::uint32_t>(cs.size() + 1));
            put_u8(buf, 0x00);
            buf.insert(buf.end(), cs.begin(), cs.end());
        }
    }
}

// ── Reader for deserialization ──────────────────────────────────────

class Reader {
public:
    Reader(std::span<const std::uint8_t> buf)
        : data_(buf.data()), size_(buf.size()), pos_(0) {}

    std::uint8_t read_u8() {
        check(1);
        return data_[pos_++];
    }

    std::uint32_t read_u32() {
        check(4);
        std::uint32_t v = 0;
        for (int i = 0; i < 4; ++i)
            v |= static_cast<std::uint32_t>(data_[pos_++]) << (i * 8);
        return v;
    }

    std::int32_t read_i32() {
        return static_cast<std::int32_t>(read_u32());
    }

    std::int64_t read_i64() {
        check(8);
        std::uint64_t v = 0;
        for (int i = 0; i < 8; ++i)
            v |= static_cast<std::uint64_t>(data_[pos_++]) << (i * 8);
        return static_cast<std::int64_t>(v);
    }

    std::uint64_t read_u64() {
        check(8);
        std::uint64_t v = 0;
        for (int i = 0; i < 8; ++i)
            v |= static_cast<std::uint64_t>(data_[pos_++]) << (i * 8);
        return v;
    }

    std::string read_string() {
        auto len = read_u32();
        if (len > kMaxMessageSize) {
            throw Error(ErrorCode::ProtocolError,
                        "string length exceeds limit");
        }
        check(len);
        std::string s(reinterpret_cast<const char*>(data_ + pos_), len);
        pos_ += len;
        return s;
    }

    Changeset read_changeset() {
        auto len = read_u32();
        if (len == 0) return {};
        check(len);
        auto type = read_u8();
        auto payload_len = len - 1;
        if (type == 0x00) {
            // Uncompressed
            Changeset cs(data_ + pos_, data_ + pos_ + payload_len);
            pos_ += payload_len;
            return cs;
        } else if (type == 0x01) {
            // LZ4
            auto original_len = read_u32();
            auto compressed_len = payload_len - 4;
            check(compressed_len);
            Changeset cs(original_len);
            int result = LZ4_decompress_safe(
                reinterpret_cast<const char*>(data_ + pos_),
                reinterpret_cast<char*>(cs.data()),
                static_cast<int>(compressed_len),
                static_cast<int>(original_len));
            if (result < 0) {
                throw Error(ErrorCode::ProtocolError,
                            "LZ4 decompression failed");
            }
            pos_ += compressed_len;
            return cs;
        } else {
            throw Error(ErrorCode::ProtocolError,
                        "unknown changeset compression type");
        }
    }

    bool at_end() const { return pos_ >= size_; }

private:
    void check(std::size_t n) {
        if (pos_ + n > size_) {
            throw Error(ErrorCode::ProtocolError, "unexpected end of message");
        }
    }

    const std::uint8_t* data_;
    std::size_t size_;
    std::size_t pos_;
};

} // namespace

// ── serialize ───────────────────────────────────────────────────────

std::vector<std::uint8_t> serialize(const Message& msg) {
    std::vector<std::uint8_t> buf;
    // Reserve space for the 4-byte length prefix.
    buf.resize(4);

    std::visit([&](const auto& m) {
        using T = std::decay_t<decltype(m)>;

        if constexpr (std::is_same_v<T, HelloMsg>) {
            put_u8(buf, static_cast<std::uint8_t>(MessageTag::Hello));
            put_u32(buf, m.protocol_version);
            put_i32(buf, m.schema_version);
            if (!m.owned_tables.empty()) {
                put_u32(buf, static_cast<std::uint32_t>(m.owned_tables.size()));
                for (const auto& t : m.owned_tables) {
                    put_string(buf, t);
                }
            }
        }
        else if constexpr (std::is_same_v<T, ChangesetMsg>) {
            put_u8(buf, static_cast<std::uint8_t>(MessageTag::Changeset));
            put_i64(buf, m.seq);
            put_changeset(buf, m.data);
        }
        else if constexpr (std::is_same_v<T, AckMsg>) {
            put_u8(buf, static_cast<std::uint8_t>(MessageTag::Ack));
            put_i64(buf, m.seq);
        }
        else if constexpr (std::is_same_v<T, ErrorMsg>) {
            put_u8(buf, static_cast<std::uint8_t>(MessageTag::Error));
            put_i32(buf, static_cast<std::int32_t>(m.code));
            put_string(buf, m.detail);
            put_i32(buf, m.remote_schema_version);
            put_string(buf, m.remote_schema_sql);
        }
        else if constexpr (std::is_same_v<T, BucketHashesMsg>) {
            put_u8(buf, static_cast<std::uint8_t>(MessageTag::BucketHashes));
            put_u32(buf, static_cast<std::uint32_t>(m.buckets.size()));
            for (const auto& b : m.buckets) {
                put_string(buf, b.table);
                put_i64(buf, b.bucket_lo);
                put_i64(buf, b.bucket_hi);
                put_u64(buf, b.hash);
                put_i64(buf, b.row_count);
            }
        }
        else if constexpr (std::is_same_v<T, NeedBucketsMsg>) {
            put_u8(buf, static_cast<std::uint8_t>(MessageTag::NeedBuckets));
            put_u32(buf, static_cast<std::uint32_t>(m.ranges.size()));
            for (const auto& r : m.ranges) {
                put_string(buf, r.table);
                put_i64(buf, r.lo);
                put_i64(buf, r.hi);
            }
        }
        else if constexpr (std::is_same_v<T, RowHashesMsg>) {
            put_u8(buf, static_cast<std::uint8_t>(MessageTag::RowHashes));
            put_u32(buf, static_cast<std::uint32_t>(m.entries.size()));
            for (const auto& e : m.entries) {
                put_string(buf, e.table);
                put_i64(buf, e.lo);
                put_i64(buf, e.hi);
                put_u32(buf, static_cast<std::uint32_t>(e.runs.size()));
                for (const auto& run : e.runs) {
                    put_i64(buf, run.start_rowid);
                    put_i64(buf, run.count);
                    for (std::int64_t i = 0; i < run.count; ++i) {
                        put_u64(buf, run.hashes[static_cast<std::size_t>(i)]);
                    }
                }
            }
        }
        else if constexpr (std::is_same_v<T, DiffReadyMsg>) {
            put_u8(buf, static_cast<std::uint8_t>(MessageTag::DiffReady));
            put_i64(buf, m.seq);
            put_changeset(buf, m.patchset);
            put_u32(buf, static_cast<std::uint32_t>(m.deletes.size()));
            for (const auto& td : m.deletes) {
                put_string(buf, td.table);
                put_u32(buf, static_cast<std::uint32_t>(td.rowids.size()));
                for (auto rid : td.rowids) {
                    put_i64(buf, rid);
                }
            }
        }
    }, msg);

    // Patch the length prefix: total_length = tag + payload (excludes the 4 prefix bytes).
    std::uint32_t total = static_cast<std::uint32_t>(buf.size() - 4);
    buf[0] = static_cast<std::uint8_t>(total);
    buf[1] = static_cast<std::uint8_t>(total >> 8);
    buf[2] = static_cast<std::uint8_t>(total >> 16);
    buf[3] = static_cast<std::uint8_t>(total >> 24);

    return buf;
}

// ── deserialize ─────────────────────────────────────────────────────

Message deserialize(std::span<const std::uint8_t> buf) {
    if (buf.size() < 5) {
        throw Error(ErrorCode::ProtocolError, "message too short");
    }
    if (buf.size() > kMaxMessageSize + 4) {
        throw Error(ErrorCode::ProtocolError,
                    "message exceeds maximum size (" +
                    std::to_string(buf.size()) + " bytes)");
    }

    Reader r(buf);
    auto total_len = r.read_u32();
    (void)total_len;  // already have the full buffer

    auto tag = static_cast<MessageTag>(r.read_u8());

    auto check_count = [](std::uint32_t n) {
        if (n > kMaxArrayCount) {
            throw Error(ErrorCode::ProtocolError,
                        "array count exceeds limit (" +
                        std::to_string(n) + ")");
        }
    };

    switch (tag) {
    case MessageTag::Hello: {
        HelloMsg m;
        m.protocol_version = r.read_u32();
        m.schema_version = r.read_i32();
        if (!r.at_end()) {
            auto count = r.read_u32();
            check_count(count);
            for (std::uint32_t i = 0; i < count; ++i) {
                m.owned_tables.insert(r.read_string());
            }
        }
        return m;
    }
    case MessageTag::Changeset: {
        ChangesetMsg m;
        m.seq = r.read_i64();
        m.data = r.read_changeset();
        return m;
    }
    case MessageTag::Ack: {
        AckMsg m;
        m.seq = r.read_i64();
        return m;
    }
    case MessageTag::Error: {
        ErrorMsg m;
        m.code = static_cast<ErrorCode>(r.read_i32());
        m.detail = r.read_string();
        m.remote_schema_version = r.read_i32();
        m.remote_schema_sql = r.read_string();
        return m;
    }
    case MessageTag::BucketHashes: {
        BucketHashesMsg m;
        auto count = r.read_u32();
        check_count(count);
        m.buckets.resize(count);
        for (std::uint32_t i = 0; i < count; ++i) {
            m.buckets[i].table = r.read_string();
            m.buckets[i].bucket_lo = r.read_i64();
            m.buckets[i].bucket_hi = r.read_i64();
            m.buckets[i].hash = r.read_u64();
            m.buckets[i].row_count = r.read_i64();
        }
        return m;
    }
    case MessageTag::NeedBuckets: {
        NeedBucketsMsg m;
        auto count = r.read_u32();
        check_count(count);
        m.ranges.resize(count);
        for (std::uint32_t i = 0; i < count; ++i) {
            m.ranges[i].table = r.read_string();
            m.ranges[i].lo = r.read_i64();
            m.ranges[i].hi = r.read_i64();
        }
        return m;
    }
    case MessageTag::RowHashes: {
        RowHashesMsg m;
        auto entry_count = r.read_u32();
        check_count(entry_count);
        m.entries.resize(entry_count);
        for (std::uint32_t i = 0; i < entry_count; ++i) {
            m.entries[i].table = r.read_string();
            m.entries[i].lo = r.read_i64();
            m.entries[i].hi = r.read_i64();
            auto run_count = r.read_u32();
            check_count(run_count);
            m.entries[i].runs.resize(run_count);
            for (std::uint32_t j = 0; j < run_count; ++j) {
                m.entries[i].runs[j].start_rowid = r.read_i64();
                m.entries[i].runs[j].count = r.read_i64();
                m.entries[i].runs[j].hashes.resize(
                    static_cast<std::size_t>(m.entries[i].runs[j].count));
                for (std::int64_t k = 0; k < m.entries[i].runs[j].count; ++k) {
                    m.entries[i].runs[j].hashes[static_cast<std::size_t>(k)] =
                        r.read_u64();
                }
            }
        }
        return m;
    }
    case MessageTag::DiffReady: {
        DiffReadyMsg m;
        m.seq = r.read_i64();
        m.patchset = r.read_changeset();
        auto del_count = r.read_u32();
        check_count(del_count);
        m.deletes.resize(del_count);
        for (std::uint32_t i = 0; i < del_count; ++i) {
            m.deletes[i].table = r.read_string();
            auto rid_count = r.read_u32();
            check_count(rid_count);
            m.deletes[i].rowids.resize(rid_count);
            for (std::uint32_t j = 0; j < rid_count; ++j) {
                m.deletes[i].rowids[j] = r.read_i64();
            }
        }
        return m;
    }
    default:
        throw Error(ErrorCode::ProtocolError,
                    "unknown message tag: " +
                    std::to_string(static_cast<int>(tag)));
    }
}

} // namespace sqlpipe

// ── meta.cpp ────────────────────────────────────────────────────

namespace sqlpipe::detail {

void ensure_meta_table(sqlite3* db) {
    exec(db,
        "CREATE TABLE IF NOT EXISTS _sqlpipe_meta ("
        "  key   TEXT PRIMARY KEY,"
        "  value TEXT NOT NULL"
        ")");

    // Seed seq=0 if not present.
    exec(db,
        "INSERT OR IGNORE INTO _sqlpipe_meta (key, value) "
        "VALUES ('seq', '0')");
}

Seq read_seq(sqlite3* db, const std::string& key) {
    auto stmt = prepare(db,
        "SELECT value FROM _sqlpipe_meta WHERE key=?");
    sqlite3_bind_text(stmt.get(), 1, key.c_str(),
                      static_cast<int>(key.size()), SQLITE_TRANSIENT);
    int rc = sqlite3_step(stmt.get());
    if (rc == SQLITE_ROW) {
        return sqlite3_column_int64(stmt.get(), 0);
    }
    return 0;
}

void write_seq(sqlite3* db, Seq seq, const std::string& key) {
    auto stmt = prepare(db,
        "INSERT OR REPLACE INTO _sqlpipe_meta (key, value) VALUES (?, ?)");
    sqlite3_bind_text(stmt.get(), 1, key.c_str(),
                      static_cast<int>(key.size()), SQLITE_TRANSIENT);
    sqlite3_bind_int64(stmt.get(), 2, seq);
    step_done(db, stmt.get());
}

SchemaVersion compute_schema_fingerprint(
        sqlite3* db, const std::set<std::string>* filter) {
    auto sql = get_schema_sql(db, filter);

    int err_type;
    char* err_msg = nullptr;

    char* json = sqlift_parse(sql.c_str(), &err_type, &err_msg);
    if (!json) {
        std::string msg = err_msg ? err_msg : "unknown error";
        sqlift_free(err_msg);
        throw Error(ErrorCode::SqliteError,
                    "sqlift_parse failed: " + msg);
    }

    char* hex = sqlift_schema_hash(json, &err_type, &err_msg);
    sqlift_free(json);
    if (!hex) {
        std::string msg = err_msg ? err_msg : "unknown error";
        sqlift_free(err_msg);
        throw Error(ErrorCode::SqliteError,
                    "sqlift_schema_hash failed: " + msg);
    }

    // FNV-1a 32-bit of the structural hash.
    std::uint32_t hash = 2166136261u;
    for (const char* p = hex; *p; ++p) {
        hash ^= static_cast<std::uint8_t>(*p);
        hash *= 16777619u;
    }
    sqlift_free(hex);
    return static_cast<SchemaVersion>(hash);
}

bool is_without_rowid(sqlite3* db, const std::string& table) {
    // A WITHOUT ROWID table will fail when you try to select rowid.
    std::string sql = "SELECT rowid FROM \"" + table + "\" LIMIT 0";
    sqlite3_stmt* raw = nullptr;
    int rc = sqlite3_prepare_v2(db, sql.c_str(), -1, &raw, nullptr);
    if (raw) sqlite3_finalize(raw);
    return rc != SQLITE_OK;
}

std::vector<std::string> get_tracked_tables(
        sqlite3* db, const std::set<std::string>* filter,
        const LogCallback* on_log) {
    auto stmt = prepare(db,
        "SELECT name FROM sqlite_master "
        "WHERE type='table' "
        "  AND name NOT LIKE '_sqlpipe_%' "
        "  AND name NOT LIKE 'sqlite_%' "
        "ORDER BY name");

    std::vector<std::string> tables;
    while (sqlite3_step(stmt.get()) == SQLITE_ROW) {
        std::string name(reinterpret_cast<const char*>(
            sqlite3_column_text(stmt.get(), 0)));

        // Skip tables not in the filter (if provided).
        if (filter && filter->find(name) == filter->end()) {
            continue;
        }

        // Check if table has an explicit PK.
        std::string pragma = "PRAGMA table_info('" + name + "')";
        auto pk_stmt = prepare(db, pragma.c_str());
        bool has_pk = false;
        while (sqlite3_step(pk_stmt.get()) == SQLITE_ROW) {
            if (sqlite3_column_int(pk_stmt.get(), 5) > 0) {  // pk column
                has_pk = true;
                break;
            }
        }

        if (!has_pk) {
            SQLPIPE_LOG(on_log ? *on_log : LogCallback{}, LogLevel::Warn,
                       "table '{}' has no explicit PRIMARY KEY, skipping", name);
            continue;
        }

        // Reject WITHOUT ROWID tables.
        if (is_without_rowid(db, name)) {
            throw Error(ErrorCode::WithoutRowidTable,
                        "table '" + name + "' uses WITHOUT ROWID (not supported)");
        }

        tables.push_back(std::move(name));
    }

    return tables;
}

std::string get_schema_sql(
        sqlite3* db, const std::set<std::string>* filter) {
    auto tables = get_tracked_tables(db, filter);
    std::string sql;
    for (const auto& t : tables) {
        if (!sql.empty()) sql += ";\n";
        sql += get_table_create_sql(db, t);
    }
    if (!sql.empty()) sql += ";";
    return sql;
}

std::string get_table_create_sql(sqlite3* db, const std::string& table) {
    auto stmt = prepare(db,
        "SELECT sql FROM sqlite_master WHERE type='table' AND name=?");
    sqlite3_bind_text(stmt.get(), 1, table.c_str(),
                      static_cast<int>(table.size()), SQLITE_TRANSIENT);
    int rc = sqlite3_step(stmt.get());
    if (rc == SQLITE_ROW) {
        return reinterpret_cast<const char*>(sqlite3_column_text(stmt.get(), 0));
    }
    throw Error(ErrorCode::SqliteError,
                "table '" + table + "' not found in sqlite_master");
}

} // namespace sqlpipe::detail

// ── hash.cpp ────────────────────────────────────────────────────

namespace sqlpipe::detail {

namespace {
constexpr std::uint64_t kFnv64Offset = 14695981039346656037ULL;
constexpr std::uint64_t kFnv64Prime  = 1099511628211ULL;

inline void fnv64_byte(std::uint64_t& hash, std::uint8_t b) {
    hash ^= b;
    hash *= kFnv64Prime;
}

inline void fnv64_bytes(std::uint64_t& hash,
                        const void* data, std::size_t len) {
    auto* p = static_cast<const std::uint8_t*>(data);
    for (std::size_t i = 0; i < len; ++i) {
        fnv64_byte(hash, p[i]);
    }
}
} // namespace

/// Feed a Value into a running FNV-1a hash (type-tagged, same encoding as hash_row).
inline void hash_value(std::uint64_t& hash, const Value& v) {
    std::visit([&](const auto& val) {
        using T = std::decay_t<decltype(val)>;
        if constexpr (std::is_same_v<T, std::monostate>) {
            fnv64_byte(hash, 0x00);
        } else if constexpr (std::is_same_v<T, std::int64_t>) {
            fnv64_byte(hash, 0x01);
            auto u = static_cast<std::uint64_t>(val);
            std::uint8_t bytes[8];
            for (int j = 0; j < 8; ++j)
                bytes[j] = static_cast<std::uint8_t>(u >> (j * 8));
            fnv64_bytes(hash, bytes, 8);
        } else if constexpr (std::is_same_v<T, double>) {
            fnv64_byte(hash, 0x02);
            std::uint8_t bytes[8];
            std::memcpy(bytes, &val, 8);
            fnv64_bytes(hash, bytes, 8);
        } else if constexpr (std::is_same_v<T, std::string>) {
            fnv64_byte(hash, 0x03);
            std::uint32_t ulen = static_cast<std::uint32_t>(val.size());
            std::uint8_t lenbytes[4];
            for (int j = 0; j < 4; ++j)
                lenbytes[j] = static_cast<std::uint8_t>(ulen >> (j * 8));
            fnv64_bytes(hash, lenbytes, 4);
            fnv64_bytes(hash, val.data(), val.size());
        } else if constexpr (std::is_same_v<T, std::vector<std::uint8_t>>) {
            fnv64_byte(hash, 0x04);
            std::uint32_t ulen = static_cast<std::uint32_t>(val.size());
            std::uint8_t lenbytes[4];
            for (int j = 0; j < 4; ++j)
                lenbytes[j] = static_cast<std::uint8_t>(ulen >> (j * 8));
            fnv64_bytes(hash, lenbytes, 4);
            fnv64_bytes(hash, val.data(), val.size());
        }
    }, v);
}

std::uint64_t hash_row(sqlite3_stmt* stmt, int ncols) {
    std::uint64_t hash = kFnv64Offset;

    for (int i = 0; i < ncols; ++i) {
        int type = sqlite3_column_type(stmt, i);
        switch (type) {
        case SQLITE_NULL:
            fnv64_byte(hash, 0x00);
            break;
        case SQLITE_INTEGER: {
            fnv64_byte(hash, 0x01);
            auto val = sqlite3_column_int64(stmt, i);
            auto u = static_cast<std::uint64_t>(val);
            std::uint8_t bytes[8];
            for (int j = 0; j < 8; ++j)
                bytes[j] = static_cast<std::uint8_t>(u >> (j * 8));
            fnv64_bytes(hash, bytes, 8);
            break;
        }
        case SQLITE_FLOAT: {
            fnv64_byte(hash, 0x02);
            double val = sqlite3_column_double(stmt, i);
            std::uint8_t bytes[8];
            std::memcpy(bytes, &val, 8);
            fnv64_bytes(hash, bytes, 8);
            break;
        }
        case SQLITE_TEXT: {
            fnv64_byte(hash, 0x03);
            int len = sqlite3_column_bytes(stmt, i);
            std::uint32_t ulen = static_cast<std::uint32_t>(len);
            std::uint8_t lenbytes[4];
            for (int j = 0; j < 4; ++j)
                lenbytes[j] = static_cast<std::uint8_t>(ulen >> (j * 8));
            fnv64_bytes(hash, lenbytes, 4);
            fnv64_bytes(hash, sqlite3_column_text(stmt, i),
                        static_cast<std::size_t>(len));
            break;
        }
        case SQLITE_BLOB: {
            fnv64_byte(hash, 0x04);
            int len = sqlite3_column_bytes(stmt, i);
            std::uint32_t ulen = static_cast<std::uint32_t>(len);
            std::uint8_t lenbytes[4];
            for (int j = 0; j < 4; ++j)
                lenbytes[j] = static_cast<std::uint8_t>(ulen >> (j * 8));
            fnv64_bytes(hash, lenbytes, 4);
            fnv64_bytes(hash, sqlite3_column_blob(stmt, i),
                        static_cast<std::size_t>(len));
            break;
        }
        default:
            fnv64_byte(hash, 0x00);
            break;
        }
    }
    return hash;
}

std::uint64_t hash_bucket_entry(std::int64_t rowid, std::uint64_t row_hash) {
    std::uint64_t hash = kFnv64Offset;
    auto u = static_cast<std::uint64_t>(rowid);
    std::uint8_t bytes[8];
    for (int i = 0; i < 8; ++i)
        bytes[i] = static_cast<std::uint8_t>(u >> (i * 8));
    fnv64_bytes(hash, bytes, 8);
    for (int i = 0; i < 8; ++i)
        bytes[i] = static_cast<std::uint8_t>(row_hash >> (i * 8));
    fnv64_bytes(hash, bytes, 8);
    return hash;
}

std::vector<RowHashInfo> compute_row_hashes(
        sqlite3* db, const std::string& table,
        std::int64_t lo, std::int64_t hi) {
    std::string sql = "SELECT rowid, * FROM \"" + table +
                      "\" WHERE rowid >= ? AND rowid <= ? ORDER BY rowid";
    auto stmt = prepare(db, sql.c_str());
    sqlite3_bind_int64(stmt.get(), 1, lo);
    sqlite3_bind_int64(stmt.get(), 2, hi);

    int ncols = sqlite3_column_count(stmt.get());

    std::vector<RowHashInfo> result;
    while (sqlite3_step(stmt.get()) == SQLITE_ROW) {
        std::int64_t rowid = sqlite3_column_int64(stmt.get(), 0);
        // Hash columns 1..ncols-1 (skip the rowid column at position 0).
        std::uint64_t h = kFnv64Offset;
        for (int c = 1; c < ncols; ++c) {
            int type = sqlite3_column_type(stmt.get(), c);
            switch (type) {
            case SQLITE_NULL:
                fnv64_byte(h, 0x00);
                break;
            case SQLITE_INTEGER: {
                fnv64_byte(h, 0x01);
                auto val = sqlite3_column_int64(stmt.get(), c);
                auto u = static_cast<std::uint64_t>(val);
                std::uint8_t bytes[8];
                for (int j = 0; j < 8; ++j)
                    bytes[j] = static_cast<std::uint8_t>(u >> (j * 8));
                fnv64_bytes(h, bytes, 8);
                break;
            }
            case SQLITE_FLOAT: {
                fnv64_byte(h, 0x02);
                double val = sqlite3_column_double(stmt.get(), c);
                std::uint8_t bytes[8];
                std::memcpy(bytes, &val, 8);
                fnv64_bytes(h, bytes, 8);
                break;
            }
            case SQLITE_TEXT: {
                fnv64_byte(h, 0x03);
                int len = sqlite3_column_bytes(stmt.get(), c);
                std::uint32_t ulen = static_cast<std::uint32_t>(len);
                std::uint8_t lenbytes[4];
                for (int j = 0; j < 4; ++j)
                    lenbytes[j] = static_cast<std::uint8_t>(ulen >> (j * 8));
                fnv64_bytes(h, lenbytes, 4);
                fnv64_bytes(h, sqlite3_column_text(stmt.get(), c),
                            static_cast<std::size_t>(len));
                break;
            }
            case SQLITE_BLOB: {
                fnv64_byte(h, 0x04);
                int len = sqlite3_column_bytes(stmt.get(), c);
                std::uint32_t ulen = static_cast<std::uint32_t>(len);
                std::uint8_t lenbytes[4];
                for (int j = 0; j < 4; ++j)
                    lenbytes[j] = static_cast<std::uint8_t>(ulen >> (j * 8));
                fnv64_bytes(h, lenbytes, 4);
                fnv64_bytes(h, sqlite3_column_blob(stmt.get(), c),
                            static_cast<std::size_t>(len));
                break;
            }
            default:
                fnv64_byte(h, 0x00);
                break;
            }
        }
        result.push_back({rowid, h});
    }
    return result;
}

std::vector<BucketInfo> compute_table_buckets(
        sqlite3* db, const std::string& table, std::int64_t bucket_size) {
    // Get min and max rowid.
    std::string minmax_sql = "SELECT MIN(rowid), MAX(rowid) FROM \"" + table + "\"";
    auto stmt = prepare(db, minmax_sql.c_str());
    if (sqlite3_step(stmt.get()) != SQLITE_ROW ||
        sqlite3_column_type(stmt.get(), 0) == SQLITE_NULL) {
        return {};  // empty table
    }
    std::int64_t min_rid = sqlite3_column_int64(stmt.get(), 0);
    std::int64_t max_rid = sqlite3_column_int64(stmt.get(), 1);

    std::int64_t lo_bucket = min_rid / bucket_size;
    if (min_rid < 0) lo_bucket = (min_rid - bucket_size + 1) / bucket_size;
    std::int64_t hi_bucket = max_rid / bucket_size;
    if (max_rid < 0) hi_bucket = (max_rid - bucket_size + 1) / bucket_size;

    std::vector<BucketInfo> buckets;
    for (std::int64_t k = lo_bucket; k <= hi_bucket; ++k) {
        std::int64_t blo = k * bucket_size;
        std::int64_t bhi = blo + bucket_size - 1;

        auto rows = compute_row_hashes(db, table, blo, bhi);
        if (rows.empty()) continue;

        std::uint64_t bucket_hash = 0;
        for (const auto& row : rows) {
            bucket_hash ^= hash_bucket_entry(row.rowid, row.hash);
        }
        buckets.push_back({blo, bhi, bucket_hash,
                           static_cast<std::int64_t>(rows.size())});
    }
    return buckets;
}

std::vector<BucketHashEntry> compute_all_buckets(
        sqlite3* db, const std::set<std::string>* filter,
        std::int64_t bucket_size) {
    auto tables = get_tracked_tables(db, filter);
    std::vector<BucketHashEntry> result;
    for (const auto& table : tables) {
        auto buckets = compute_table_buckets(db, table, bucket_size);
        for (auto& b : buckets) {
            result.push_back(BucketHashEntry{
                table, b.lo, b.hi, b.hash, b.count});
        }
    }
    return result;
}

Changeset build_insert_patchset(
        sqlite3* db, const std::string& table,
        const std::vector<std::int64_t>& rowids) {
    if (rowids.empty()) return {};

    auto create_sql = get_table_create_sql(db, table);

    exec(db, "ATTACH ':memory:' AS _sqlpipe_stage");

    // Create table in _sqlpipe_stage.
    std::string prefixed = create_sql;
    auto pos = prefixed.find("CREATE TABLE ");
    if (pos != std::string::npos) {
        prefixed.insert(pos + 13, "_sqlpipe_stage.");
    }
    exec(db, prefixed.c_str());

    // Create session on _sqlpipe_stage.
    sqlite3_session* raw = nullptr;
    int rc = sqlite3session_create(db, "_sqlpipe_stage", &raw);
    if (rc != SQLITE_OK) {
        exec(db, "DETACH _sqlpipe_stage");
        throw Error(ErrorCode::SqliteError,
                    std::string("session_create: ") + sqlite3_errmsg(db));
    }
    SessionGuard session(raw);

    rc = sqlite3session_attach(raw, table.c_str());
    if (rc != SQLITE_OK) {
        exec(db, "DETACH _sqlpipe_stage");
        throw Error(ErrorCode::SqliteError,
                    std::string("session_attach: ") + sqlite3_errmsg(db));
    }

    // Insert target rows from main into _sqlpipe_stage.
    // Build the IN clause or use individual inserts.
    for (auto rid : rowids) {
        std::string ins_sql =
            "INSERT INTO _sqlpipe_stage.\"" + table +
            "\" SELECT * FROM main.\"" + table + "\" WHERE rowid = ?";
        auto ins_stmt = prepare(db, ins_sql.c_str());
        sqlite3_bind_int64(ins_stmt.get(), 1, rid);
        step_done(db, ins_stmt.get());
    }

    // Extract patchset.
    int n = 0;
    void* p = nullptr;
    rc = sqlite3session_patchset(raw, &n, &p);
    if (rc != SQLITE_OK) {
        exec(db, "DETACH _sqlpipe_stage");
        throw Error(ErrorCode::SqliteError,
                    std::string("session_patchset: ") + sqlite3_errmsg(db));
    }

    Changeset cs;
    if (n > 0 && p) {
        cs.assign(static_cast<std::uint8_t*>(p),
                  static_cast<std::uint8_t*>(p) + n);
    }
    sqlite3_free(p);

    // Cleanup.
    session = SessionGuard{};  // delete before detach
    exec(db, ("DROP TABLE _sqlpipe_stage.\"" + table + "\"").c_str());
    exec(db, "DETACH _sqlpipe_stage");

    return cs;
}

Changeset combine_patchsets(const std::vector<Changeset>& parts) {
    if (parts.empty()) return {};
    if (parts.size() == 1) return parts[0];

    sqlite3_changegroup* grp = nullptr;
    int rc = sqlite3changegroup_new(&grp);
    if (rc != SQLITE_OK) {
        throw Error(ErrorCode::SqliteError, "sqlite3changegroup_new failed");
    }

    for (const auto& cs : parts) {
        if (cs.empty()) continue;
        rc = sqlite3changegroup_add(grp,
            static_cast<int>(cs.size()),
            const_cast<void*>(static_cast<const void*>(cs.data())));
        if (rc != SQLITE_OK) {
            sqlite3changegroup_delete(grp);
            throw Error(ErrorCode::SqliteError, "sqlite3changegroup_add failed");
        }
    }

    int n = 0;
    void* p = nullptr;
    rc = sqlite3changegroup_output(grp, &n, &p);
    sqlite3changegroup_delete(grp);

    if (rc != SQLITE_OK) {
        sqlite3_free(p);
        throw Error(ErrorCode::SqliteError, "sqlite3changegroup_output failed");
    }

    Changeset result;
    if (n > 0 && p) {
        result.assign(static_cast<std::uint8_t*>(p),
                      static_cast<std::uint8_t*>(p) + n);
    }
    sqlite3_free(p);
    return result;
}

} // namespace sqlpipe::detail

// ── changeset_iter.cpp ──────────────────────────────────────────

namespace sqlpipe::detail {

Value to_value(sqlite3_value* val) {
    if (!val) return std::monostate{};

    switch (sqlite3_value_type(val)) {
    case SQLITE_NULL:
        return std::monostate{};
    case SQLITE_INTEGER:
        return sqlite3_value_int64(val);
    case SQLITE_FLOAT:
        return sqlite3_value_double(val);
    case SQLITE_TEXT: {
        auto* text = reinterpret_cast<const char*>(sqlite3_value_text(val));
        int len = sqlite3_value_bytes(val);
        return std::string(text, static_cast<std::size_t>(len));
    }
    case SQLITE_BLOB: {
        auto* data = static_cast<const std::uint8_t*>(sqlite3_value_blob(val));
        int len = sqlite3_value_bytes(val);
        return std::vector<std::uint8_t>(data, data + len);
    }
    default:
        return std::monostate{};
    }
}

OpType sqlite_op_to_optype(int sqlite_op) {
    switch (sqlite_op) {
    case SQLITE_INSERT: return OpType::Insert;
    case SQLITE_UPDATE: return OpType::Update;
    case SQLITE_DELETE: return OpType::Delete;
    default:
        throw Error(ErrorCode::ProtocolError,
                    "unknown sqlite operation: " + std::to_string(sqlite_op));
    }
}

ChangeEvent extract_event(sqlite3_changeset_iter* iter) {
    const char* table = nullptr;
    int ncol = 0, op = 0, indirect = 0;
    sqlite3changeset_op(iter, &table, &ncol, &op, &indirect);

    unsigned char* pk_raw = nullptr;
    int pk_ncol = 0;
    sqlite3changeset_pk(iter, &pk_raw, &pk_ncol);

    ChangeEvent event;
    event.table = table ? table : "";
    event.op = sqlite_op_to_optype(op);

    event.pk_flags.resize(static_cast<std::size_t>(ncol));
    for (int i = 0; i < ncol; ++i) {
        event.pk_flags[static_cast<std::size_t>(i)] = (pk_raw[i] != 0);
    }

    if (op == SQLITE_UPDATE || op == SQLITE_DELETE) {
        event.old_values.resize(static_cast<std::size_t>(ncol));
        for (int i = 0; i < ncol; ++i) {
            sqlite3_value* val = nullptr;
            sqlite3changeset_old(iter, i, &val);
            event.old_values[static_cast<std::size_t>(i)] = to_value(val);
        }
    }

    if (op == SQLITE_INSERT || op == SQLITE_UPDATE) {
        event.new_values.resize(static_cast<std::size_t>(ncol));
        for (int i = 0; i < ncol; ++i) {
            sqlite3_value* val = nullptr;
            sqlite3changeset_new(iter, i, &val);
            event.new_values[static_cast<std::size_t>(i)] = to_value(val);
        }
    }

    return event;
}

std::vector<ChangeEvent> collect_events(const Changeset& data) {
    std::vector<ChangeEvent> events;
    if (data.empty()) return events;

    sqlite3_changeset_iter* iter = nullptr;
    int rc = sqlite3changeset_start(
        &iter,
        static_cast<int>(data.size()),
        const_cast<void*>(static_cast<const void*>(data.data())));
    if (rc != SQLITE_OK) {
        throw Error(ErrorCode::SqliteError, "sqlite3changeset_start failed");
    }

    while (sqlite3changeset_next(iter) == SQLITE_ROW) {
        events.push_back(extract_event(iter));
    }

    sqlite3changeset_finalize(iter);
    return events;
}

} // namespace sqlpipe::detail

// ── master.cpp ──────────────────────────────────────────────────
namespace sqlpipe {

struct Master::Impl {
    sqlite3*                 db;
    MasterConfig             config;
    detail::SessionGuard     session;
    std::vector<std::string> tracked_tables;
    Seq                      seq = 0;
    SchemaVersion            cached_sv = 0;

    // Diff handshake state.
    enum class HSState : std::uint8_t {
        Idle,
        WaitBucketHashes,
        WaitRowHashes,
        Live,
    };
    HSState hs_state = HSState::Idle;

    // Stored between rounds: the ranges we asked for row hashes.
    std::vector<NeedBucketRange> pending_ranges;

    const std::set<std::string>* filter() const {
        return config.table_filter ? &*config.table_filter : nullptr;
    }

    void report(DiffPhase phase, const std::string& table,
                std::int64_t done, std::int64_t total) {
        if (config.on_progress) {
            config.on_progress(DiffProgress{phase, table, done, total});
        }
    }

    void init() {
        detail::ensure_meta_table(db);
        seq = detail::read_seq(db, config.seq_key);
        cached_sv = detail::compute_schema_fingerprint(db, filter());
        scan_tables();
        recreate_session();
        SQLPIPE_LOG(config.on_log, LogLevel::Info, "master initialized at seq={}", seq);
    }

    void scan_tables() {
        tracked_tables = detail::get_tracked_tables(db, filter(),
            config.on_log ? &config.on_log : nullptr);
        SQLPIPE_LOG(config.on_log, LogLevel::Info, "tracking {} tables", tracked_tables.size());
    }

    void recreate_session() {
        session = detail::SessionGuard{};  // delete old

        sqlite3_session* raw = nullptr;
        int rc = sqlite3session_create(db, "main", &raw);
        if (rc != SQLITE_OK) {
            throw Error(ErrorCode::SqliteError,
                        std::string("sqlite3session_create: ") + sqlite3_errmsg(db));
        }
        session = detail::SessionGuard(raw);

        for (const auto& t : tracked_tables) {
            rc = sqlite3session_attach(raw, t.c_str());
            if (rc != SQLITE_OK) {
                throw Error(ErrorCode::SqliteError,
                            std::string("sqlite3session_attach: ") + sqlite3_errmsg(db));
            }
        }
    }

    Changeset extract_changeset() {
        int n = 0;
        void* p = nullptr;
        int rc = sqlite3session_changeset(session.get(), &n, &p);
        if (rc != SQLITE_OK) {
            throw Error(ErrorCode::SqliteError,
                        std::string("sqlite3session_changeset: ") + sqlite3_errmsg(db));
        }

        Changeset cs;
        if (n > 0 && p) {
            cs.assign(static_cast<std::uint8_t*>(p),
                      static_cast<std::uint8_t*>(p) + n);
        }
        sqlite3_free(p);
        return cs;
    }

    std::vector<Message> handle_hello(const HelloMsg& hello) {
        if (hello.protocol_version != kProtocolVersion) {
            return {ErrorMsg{ErrorCode::ProtocolError,
                "unsupported protocol version: " +
                std::to_string(hello.protocol_version)}};
        }

        auto my_sv = detail::compute_schema_fingerprint(db, filter());

        // Schema mismatch → invoke callback or error.
        if (hello.schema_version != my_sv) {
            if (config.on_schema_mismatch &&
                config.on_schema_mismatch(hello.schema_version, my_sv, "")) {
                // Callback may have modified the schema. Recompute.
                cached_sv = detail::compute_schema_fingerprint(db, filter());
                scan_tables();
                recreate_session();
                my_sv = cached_sv;
            }
            if (hello.schema_version != my_sv) {
                SQLPIPE_LOG(config.on_log, LogLevel::Info,
                            "schema mismatch (replica={}, master={})",
                            hello.schema_version, my_sv);
                return {ErrorMsg{ErrorCode::SchemaMismatch,
                    "schema mismatch: replica=" +
                    std::to_string(hello.schema_version) +
                    " master=" + std::to_string(my_sv),
                    my_sv,
                    detail::get_schema_sql(db, filter())}};
            }
        }

        hs_state = HSState::WaitBucketHashes;
        SQLPIPE_LOG(config.on_log, LogLevel::Info, "hello ok, waiting for bucket hashes");
        return {HelloMsg{kProtocolVersion, my_sv, {}}};
    }

    std::vector<Message> handle_bucket_hashes(const BucketHashesMsg& msg) {
        if (hs_state != HSState::WaitBucketHashes) {
            return {ErrorMsg{ErrorCode::InvalidState,
                "unexpected BucketHashesMsg"}};
        }

        // Compute our own bucket hashes.
        report(DiffPhase::ComputingBuckets, {},
               0, static_cast<std::int64_t>(tracked_tables.size()));
        auto my_buckets = detail::compute_all_buckets(
            db, filter(), config.bucket_size);
        report(DiffPhase::ComputingBuckets, {},
               static_cast<std::int64_t>(tracked_tables.size()),
               static_cast<std::int64_t>(tracked_tables.size()));

        // Build lookup: (table, lo) → hash for our buckets.
        struct BucketKey {
            std::string table;
            std::int64_t lo;
            bool operator==(const BucketKey& o) const {
                return table == o.table && lo == o.lo;
            }
        };
        struct BucketKeyHash {
            std::size_t operator()(const BucketKey& k) const {
                return std::hash<std::string>{}(k.table) ^
                       (std::hash<std::int64_t>{}(k.lo) << 1);
            }
        };
        std::unordered_map<BucketKey, std::uint64_t, BucketKeyHash>
            my_bucket_map;
        std::unordered_set<BucketKey, BucketKeyHash> my_bucket_keys;
        for (const auto& b : my_buckets) {
            BucketKey key{b.table, b.bucket_lo};
            my_bucket_map[key] = b.hash;
            my_bucket_keys.insert(key);
        }

        // Build lookup for replica's buckets.
        std::unordered_map<BucketKey, std::uint64_t, BucketKeyHash>
            their_bucket_map;
        std::unordered_set<BucketKey, BucketKeyHash> their_bucket_keys;
        for (const auto& b : msg.buckets) {
            BucketKey key{b.table, b.bucket_lo};
            their_bucket_map[key] = b.hash;
            their_bucket_keys.insert(key);
        }

        // Find mismatched buckets.
        NeedBucketsMsg need;

        // Buckets on master or replica (union of both key sets).
        std::unordered_set<BucketKey, BucketKeyHash> all_keys;
        all_keys.insert(my_bucket_keys.begin(), my_bucket_keys.end());
        all_keys.insert(their_bucket_keys.begin(), their_bucket_keys.end());

        for (const auto& key : all_keys) {
            auto my_it = my_bucket_map.find(key);
            auto their_it = their_bucket_map.find(key);

            bool differs = false;
            if (my_it == my_bucket_map.end() ||
                their_it == their_bucket_map.end()) {
                differs = true;  // one side has it, the other doesn't
            } else if (my_it->second != their_it->second) {
                differs = true;  // both have it, hashes differ
            }

            if (differs) {
                // Find the hi bound from whichever side has it.
                std::int64_t hi = key.lo + config.bucket_size - 1;
                need.ranges.push_back(
                    NeedBucketRange{key.table, key.lo, hi});
            }
        }

        report(DiffPhase::ComparingBuckets, {},
               static_cast<std::int64_t>(need.ranges.size()),
               static_cast<std::int64_t>(all_keys.size()));

        // Sort ranges for deterministic order.
        std::sort(need.ranges.begin(), need.ranges.end(),
            [](const NeedBucketRange& a, const NeedBucketRange& b) {
                if (a.table != b.table) return a.table < b.table;
                return a.lo < b.lo;
            });

        if (need.ranges.empty()) {
            // All buckets match. Skip row-hash exchange.
            hs_state = HSState::Live;
            SQLPIPE_LOG(config.on_log, LogLevel::Info, "all buckets match, entering live at seq={}", seq);
            return {NeedBucketsMsg{},
                    DiffReadyMsg{seq, {}, {}}};
        }

        pending_ranges = need.ranges;
        hs_state = HSState::WaitRowHashes;
        SQLPIPE_LOG(config.on_log, LogLevel::Info, "{} mismatched bucket ranges", need.ranges.size());
        return {std::move(need)};
    }

    std::vector<Message> handle_row_hashes(const RowHashesMsg& msg) {
        if (hs_state != HSState::WaitRowHashes) {
            return {ErrorMsg{ErrorCode::InvalidState,
                "unexpected RowHashesMsg"}};
        }

        // Build map of replica's rows: table → (rowid → hash).
        std::map<std::string,
                 std::map<std::int64_t, std::uint64_t>> replica_rows;
        for (const auto& entry : msg.entries) {
            auto& tbl_map = replica_rows[entry.table];
            for (const auto& run : entry.runs) {
                for (std::int64_t i = 0; i < run.count; ++i) {
                    tbl_map[run.start_rowid + i] =
                        run.hashes[static_cast<std::size_t>(i)];
                }
            }
        }

        // Compute diff per table and build patchset + deletes.
        std::vector<Changeset> patchsets;
        std::vector<TableDeletes> deletes;

        // Group pending_ranges by table.
        std::map<std::string, std::vector<std::pair<std::int64_t, std::int64_t>>>
            table_ranges;
        for (const auto& r : pending_ranges) {
            table_ranges[r.table].push_back({r.lo, r.hi});
        }

        std::int64_t tables_done = 0;
        auto tables_total = static_cast<std::int64_t>(table_ranges.size());

        for (const auto& [table, ranges] : table_ranges) {
            report(DiffPhase::ComputingRowHashes, table,
                   tables_done, tables_total);

            std::vector<std::int64_t> insert_rowids;
            std::vector<std::int64_t> update_rowids;
            std::vector<std::int64_t> delete_rowids;

            auto replica_it = replica_rows.find(table);

            for (const auto& [lo, hi] : ranges) {
                // Compute master's row hashes for this range.
                auto master_rows = detail::compute_row_hashes(db, table, lo, hi);

                // Build replica's row hash map for this range.
                std::map<std::int64_t, std::uint64_t> rep_range;
                if (replica_it != replica_rows.end()) {
                    auto& tbl_map = replica_it->second;
                    for (auto it = tbl_map.lower_bound(lo);
                         it != tbl_map.end() && it->first <= hi; ++it) {
                        rep_range[it->first] = it->second;
                    }
                }

                // Build master's row hash map for this range.
                std::map<std::int64_t, std::uint64_t> mas_range;
                for (const auto& row : master_rows) {
                    mas_range[row.rowid] = row.hash;
                }

                // Diff.
                for (const auto& [rid, mhash] : mas_range) {
                    auto rep_it = rep_range.find(rid);
                    if (rep_it == rep_range.end()) {
                        insert_rowids.push_back(rid);
                    } else if (rep_it->second != mhash) {
                        update_rowids.push_back(rid);
                    }
                }
                for (const auto& [rid, _] : rep_range) {
                    if (mas_range.find(rid) == mas_range.end()) {
                        delete_rowids.push_back(rid);
                    }
                }
            }

            // Build INSERT patchset for insert + update rowids.
            std::vector<std::int64_t> upsert_rowids;
            upsert_rowids.reserve(insert_rowids.size() + update_rowids.size());
            upsert_rowids.insert(upsert_rowids.end(),
                                 insert_rowids.begin(), insert_rowids.end());
            upsert_rowids.insert(upsert_rowids.end(),
                                 update_rowids.begin(), update_rowids.end());

            if (!upsert_rowids.empty()) {
                report(DiffPhase::BuildingPatchset, table,
                       static_cast<std::int64_t>(upsert_rowids.size()), 0);
                auto ps = detail::build_insert_patchset(db, table, upsert_rowids);
                if (!ps.empty()) {
                    patchsets.push_back(std::move(ps));
                }
            }

            if (!delete_rowids.empty()) {
                std::sort(delete_rowids.begin(), delete_rowids.end());
                deletes.push_back(TableDeletes{table, std::move(delete_rowids)});
            }

            ++tables_done;
        }

        // Combine all per-table patchsets.
        Changeset combined = detail::combine_patchsets(patchsets);

        hs_state = HSState::Live;
        pending_ranges.clear();
        SQLPIPE_LOG(config.on_log, LogLevel::Info, "diff computed, entering live at seq={}", seq);

        return {DiffReadyMsg{seq, std::move(combined), std::move(deletes)}};
    }
};

// ── Public API ──────────────────────────────────────────────────────

Master::Master(sqlite3* db, MasterConfig config)
    : impl_(std::make_unique<Impl>()) {
    impl_->db = db;
    impl_->config = config;
    impl_->init();
}

Master::~Master() = default;
Master::Master(Master&&) noexcept = default;
Master& Master::operator=(Master&&) noexcept = default;

std::vector<Message> Master::flush() {
    // If DDL ran since last flush, the tracked table set may have changed.
    auto sv = detail::compute_schema_fingerprint(impl_->db, impl_->filter());
    if (sv != impl_->cached_sv) {
        impl_->cached_sv = sv;
        impl_->scan_tables();
        impl_->recreate_session();
    }

    auto cs = impl_->extract_changeset();
    if (cs.empty()) return {};

    impl_->recreate_session();

    impl_->seq++;
    detail::write_seq(impl_->db, impl_->seq, impl_->config.seq_key);

    SQLPIPE_LOG(impl_->config.on_log, LogLevel::Debug, "flushed changeset seq={} ({} bytes)", impl_->seq, cs.size());

    return {ChangesetMsg{impl_->seq, std::move(cs)}};
}

std::vector<Message> Master::handle_message(const Message& msg) {
    return std::visit([&](const auto& m) -> std::vector<Message> {
        using T = std::decay_t<decltype(m)>;

        if constexpr (std::is_same_v<T, HelloMsg>) {
            return impl_->handle_hello(m);
        }
        else if constexpr (std::is_same_v<T, BucketHashesMsg>) {
            return impl_->handle_bucket_hashes(m);
        }
        else if constexpr (std::is_same_v<T, RowHashesMsg>) {
            return impl_->handle_row_hashes(m);
        }
        else if constexpr (std::is_same_v<T, AckMsg>) {
            SQLPIPE_LOG(impl_->config.on_log, LogLevel::Debug, "replica acked seq={}", m.seq);
            return {};
        }
        else {
            return {ErrorMsg{ErrorCode::InvalidState,
                "unexpected message from replica"}};
        }
    }, msg);
}

Seq Master::current_seq() const { return impl_->seq; }

SchemaVersion Master::schema_version() const {
    return detail::compute_schema_fingerprint(impl_->db, impl_->filter());
}

} // namespace sqlpipe

// ── query_watch.cpp ─────────────────────────────────────────────

namespace sqlpipe {

struct QueryWatch::Impl {
    sqlite3* db;

    struct Subscription {
        SubscriptionId               id;
        std::string                  sql;
        std::set<std::string>        tables;
        detail::StmtGuard            stmt;     // cached prepared statement
        std::vector<std::string>     columns;  // cached column names
        std::uint64_t                result_hash = 0;  // hash of last delivered result
    };
    std::map<SubscriptionId, Subscription> subscriptions;
    // Reverse index: table name → subscription IDs that depend on it.
    std::unordered_map<std::string, std::set<SubscriptionId>> table_subs;
    SubscriptionId next_sub_id = 1;

    std::set<std::string> discover_tables(const std::string& sql) {
        std::set<std::string> tables;
        sqlite3_set_authorizer(db, [](void* ctx, int action,
                const char* a1, const char*, const char*, const char*) -> int {
            if (action == SQLITE_READ && a1) {
                std::string name(a1);
                if (name.compare(0, 9, "_sqlpipe_") != 0 &&
                    name.compare(0, 7, "sqlite_") != 0) {
                    static_cast<std::set<std::string>*>(ctx)->insert(name);
                }
            }
            return SQLITE_OK;
        }, &tables);

        sqlite3_stmt* stmt = nullptr;
        int rc = sqlite3_prepare_v2(db, sql.c_str(), -1, &stmt, nullptr);
        if (stmt) sqlite3_finalize(stmt);
        sqlite3_set_authorizer(db, nullptr, nullptr);

        if (rc != SQLITE_OK) {
            throw Error(ErrorCode::SqliteError,
                std::string("subscribe prepare: ") + sqlite3_errmsg(db));
        }
        return tables;
    }

    // Evaluate a subscription query. Returns the result and its hash.
    std::pair<QueryResult, std::uint64_t> evaluate_query(Subscription& sub) {
        QueryResult result;
        result.id = sub.id;
        result.columns = sub.columns;

        std::uint64_t h = detail::kFnv64Offset;
        sqlite3_reset(sub.stmt.get());
        int ncols = static_cast<int>(sub.columns.size());
        while (sqlite3_step(sub.stmt.get()) == SQLITE_ROW) {
            detail::fnv64_byte(h, 0xFF);  // row separator
            std::vector<Value> row;
            row.reserve(static_cast<std::size_t>(ncols));
            for (int i = 0; i < ncols; ++i) {
                row.push_back(detail::to_value(
                    sqlite3_column_value(sub.stmt.get(), i)));
                detail::hash_value(h, row.back());
            }
            result.rows.push_back(std::move(row));
        }
        return {std::move(result), h};
    }

    std::vector<QueryResult> evaluate_invalidated(
            const std::set<std::string>& affected) {
        // Collect unique subscription IDs via reverse index.
        std::set<SubscriptionId> ids;
        for (const auto& table : affected) {
            auto it = table_subs.find(table);
            if (it != table_subs.end()) {
                ids.insert(it->second.begin(), it->second.end());
            }
        }

        std::vector<QueryResult> results;
        for (auto id : ids) {
            auto it = subscriptions.find(id);
            if (it != subscriptions.end()) {
                auto [result, hash] = evaluate_query(it->second);
                if (hash != it->second.result_hash) {
                    it->second.result_hash = hash;
                    results.push_back(std::move(result));
                }
            }
        }
        return results;
    }
};

QueryWatch::QueryWatch(sqlite3* db)
    : impl_(std::make_unique<Impl>()) {
    impl_->db = db;
}

QueryWatch::~QueryWatch() = default;
QueryWatch::QueryWatch(QueryWatch&&) noexcept = default;
QueryWatch& QueryWatch::operator=(QueryWatch&&) noexcept = default;

QueryResult QueryWatch::subscribe(const std::string& sql) {
    auto tables = impl_->discover_tables(sql);
    auto id = impl_->next_sub_id++;

    // Prepare statement once and cache column names.
    auto stmt = detail::prepare(impl_->db, sql.c_str());
    int ncols = sqlite3_column_count(stmt.get());
    std::vector<std::string> columns;
    columns.reserve(static_cast<std::size_t>(ncols));
    for (int i = 0; i < ncols; ++i) {
        const char* name = sqlite3_column_name(stmt.get(), i);
        columns.push_back(name ? name : "");
    }

    // Build reverse index entries.
    for (const auto& t : tables) {
        impl_->table_subs[t].insert(id);
    }

    impl_->subscriptions[id] = {id, sql, std::move(tables),
                                std::move(stmt), std::move(columns), 0};
    auto [result, hash] = impl_->evaluate_query(impl_->subscriptions[id]);
    impl_->subscriptions[id].result_hash = hash;
    return result;
}

void QueryWatch::unsubscribe(SubscriptionId id) {
    auto it = impl_->subscriptions.find(id);
    if (it != impl_->subscriptions.end()) {
        // Remove reverse index entries.
        for (const auto& t : it->second.tables) {
            auto ts_it = impl_->table_subs.find(t);
            if (ts_it != impl_->table_subs.end()) {
                ts_it->second.erase(id);
                if (ts_it->second.empty()) {
                    impl_->table_subs.erase(ts_it);
                }
            }
        }
        impl_->subscriptions.erase(it);
    }
}

std::vector<QueryResult> QueryWatch::notify(
        const std::set<std::string>& affected_tables) {
    return impl_->evaluate_invalidated(affected_tables);
}

bool QueryWatch::empty() const {
    return impl_->subscriptions.empty();
}

} // namespace sqlpipe

// ── replica.cpp ─────────────────────────────────────────────────

namespace sqlpipe {

struct Replica::Impl {
    sqlite3*       db;
    ReplicaConfig  config;
    Seq            seq = 0;
    Replica::State state = Replica::State::Init;
    QueryWatch     watch;

    Impl(sqlite3* db_, ReplicaConfig cfg)
        : db(db_), config(std::move(cfg)), watch(db_) {}

    const std::set<std::string>* filter() const {
        return config.table_filter ? &*config.table_filter : nullptr;
    }

    void report(DiffPhase phase, const std::string& table,
                std::int64_t done, std::int64_t total) {
        if (config.on_progress) {
            config.on_progress(DiffProgress{phase, table, done, total});
        }
    }

    void init() {
        detail::ensure_meta_table(db);
        seq = detail::read_seq(db, config.seq_key);
        SQLPIPE_LOG(config.on_log, LogLevel::Info, "replica initialized at seq={}", seq);
    }

    std::vector<ChangeEvent> apply_changeset(const Changeset& data, Seq new_seq) {
        int rc = sqlite3changeset_apply(
            db,
            static_cast<int>(data.size()),
            const_cast<void*>(static_cast<const void*>(data.data())),
            nullptr,
            [](void* ctx, int conflict_type, sqlite3_changeset_iter* iter)
                -> int {
                auto* cfg = static_cast<ReplicaConfig*>(ctx);
                if (!cfg->on_conflict) {
                    return SQLITE_CHANGESET_ABORT;
                }

                auto event = detail::extract_event(iter);

                ConflictType ct;
                switch (conflict_type) {
                case SQLITE_CHANGESET_DATA:        ct = ConflictType::Data; break;
                case SQLITE_CHANGESET_NOTFOUND:    ct = ConflictType::NotFound; break;
                case SQLITE_CHANGESET_CONFLICT:    ct = ConflictType::Conflict; break;
                case SQLITE_CHANGESET_CONSTRAINT:  ct = ConflictType::Constraint; break;
                case SQLITE_CHANGESET_FOREIGN_KEY: ct = ConflictType::ForeignKey; break;
                default:                           ct = ConflictType::Data; break;
                }

                auto action = cfg->on_conflict(ct, event);
                switch (action) {
                case ConflictAction::Omit:    return SQLITE_CHANGESET_OMIT;
                case ConflictAction::Replace: return SQLITE_CHANGESET_REPLACE;
                case ConflictAction::Abort:   return SQLITE_CHANGESET_ABORT;
                default:                      return SQLITE_CHANGESET_ABORT;
                }
            },
            &config);

        if (rc != SQLITE_OK) {
            throw Error(ErrorCode::SqliteError,
                        std::string("changeset_apply: ") + sqlite3_errmsg(db));
        }

        auto events = detail::collect_events(data);

        seq = new_seq;
        detail::write_seq(db, seq, config.seq_key);
        return events;
    }

    HandleResult handle_hello_from_master(const HelloMsg& m) {
        if (state != Replica::State::Handshake) {
            state = Replica::State::Error;
            return {{ErrorMsg{ErrorCode::InvalidState,
                "received HelloMsg in unexpected state"}}, {}, {}};
        }
        if (m.protocol_version != kProtocolVersion) {
            state = Replica::State::Error;
            return {{ErrorMsg{ErrorCode::ProtocolError,
                "unsupported protocol version"}}, {}, {}};
        }

        // Compute bucket hashes and send to master.
        report(DiffPhase::ComputingBuckets, {}, 0, 0);
        auto buckets = detail::compute_all_buckets(
            db, filter(), config.bucket_size);
        report(DiffPhase::ComputingBuckets, {},
               static_cast<std::int64_t>(buckets.size()),
               static_cast<std::int64_t>(buckets.size()));
        state = Replica::State::DiffBuckets;
        SQLPIPE_LOG(config.on_log, LogLevel::Info, "sending {} bucket hashes", buckets.size());
        return {{BucketHashesMsg{std::move(buckets)}}, {}, {}};
    }

    HandleResult handle_need_buckets(const NeedBucketsMsg& m) {
        if (state != Replica::State::DiffBuckets) {
            state = Replica::State::Error;
            return {{ErrorMsg{ErrorCode::InvalidState,
                "received NeedBucketsMsg in unexpected state"}}, {}, {}};
        }

        state = Replica::State::DiffRows;

        if (m.ranges.empty()) {
            // All buckets match; waiting for DiffReadyMsg.
            SQLPIPE_LOG(config.on_log, LogLevel::Info, "all buckets match, waiting for DiffReady");
            return {};
        }

        // Compute row hashes for requested ranges.
        RowHashesMsg rh;
        std::int64_t ranges_done = 0;
        auto ranges_total = static_cast<std::int64_t>(m.ranges.size());
        for (const auto& range : m.ranges) {
            report(DiffPhase::ComputingRowHashes, range.table,
                   ranges_done, ranges_total);
            auto rows = detail::compute_row_hashes(
                db, range.table, range.lo, range.hi);

            RowHashesEntry entry;
            entry.table = range.table;
            entry.lo = range.lo;
            entry.hi = range.hi;

            // Run-length encode: group contiguous rowids.
            if (!rows.empty()) {
                RowHashRun current_run;
                current_run.start_rowid = rows[0].rowid;
                current_run.count = 1;
                current_run.hashes.push_back(rows[0].hash);

                for (std::size_t i = 1; i < rows.size(); ++i) {
                    if (rows[i].rowid ==
                        current_run.start_rowid + current_run.count) {
                        // Contiguous.
                        current_run.count++;
                        current_run.hashes.push_back(rows[i].hash);
                    } else {
                        // Gap — start a new run.
                        entry.runs.push_back(std::move(current_run));
                        current_run = RowHashRun{};
                        current_run.start_rowid = rows[i].rowid;
                        current_run.count = 1;
                        current_run.hashes.push_back(rows[i].hash);
                    }
                }
                entry.runs.push_back(std::move(current_run));
            }

            rh.entries.push_back(std::move(entry));
            ++ranges_done;
        }

        SQLPIPE_LOG(config.on_log, LogLevel::Info, "sending row hashes for {} ranges", m.ranges.size());
        return {{std::move(rh)}, {}, {}};
    }

    HandleResult handle_diff_ready(const DiffReadyMsg& m) {
        if (state != Replica::State::DiffRows &&
            state != Replica::State::DiffBuckets) {
            state = Replica::State::Error;
            return {{ErrorMsg{ErrorCode::InvalidState,
                "received DiffReadyMsg in unexpected state"}}, {}, {}};
        }

        std::vector<ChangeEvent> events;

        // Save and disable foreign keys.
        auto fk_stmt = detail::prepare(db, "PRAGMA foreign_keys");
        bool fk_was_on = false;
        if (sqlite3_step(fk_stmt.get()) == SQLITE_ROW) {
            fk_was_on = sqlite3_column_int(fk_stmt.get(), 0) != 0;
        }
        fk_stmt = detail::StmtGuard{};
        if (fk_was_on) {
            detail::exec(db, "PRAGMA foreign_keys = OFF");
        }

        detail::exec(db, "BEGIN");

        report(DiffPhase::ApplyingPatchset, {},
               0, static_cast<std::int64_t>(m.deletes.size()) + 1);

        // Apply INSERT patchset (handles both inserts and updates via REPLACE).
        if (!m.patchset.empty()) {
            int rc = sqlite3changeset_apply(
                db,
                static_cast<int>(m.patchset.size()),
                const_cast<void*>(
                    static_cast<const void*>(m.patchset.data())),
                nullptr,
                [](void*, int, sqlite3_changeset_iter*) -> int {
                    return SQLITE_CHANGESET_REPLACE;
                },
                nullptr);

            if (rc != SQLITE_OK) {
                detail::exec(db, "ROLLBACK");
                if (fk_was_on) detail::exec(db, "PRAGMA foreign_keys = ON");
                throw Error(ErrorCode::SqliteError,
                            std::string("diff patchset apply: ") +
                            sqlite3_errmsg(db));
            }

            events = detail::collect_events(m.patchset);
        }

        // Delete rows by rowid.
        for (const auto& td : m.deletes) {
            for (auto rid : td.rowids) {
                // Query the row before deleting to collect change events.
                std::string sel_sql =
                    "SELECT * FROM \"" + td.table + "\" WHERE rowid = ?";
                auto sel_stmt = detail::prepare(db, sel_sql.c_str());
                sqlite3_bind_int64(sel_stmt.get(), 1, rid);
                if (sqlite3_step(sel_stmt.get()) == SQLITE_ROW) {
                    ChangeEvent ev;
                    ev.table = td.table;
                    ev.op = OpType::Delete;
                    int ncol = sqlite3_column_count(sel_stmt.get());
                    ev.old_values.resize(static_cast<std::size_t>(ncol));
                    for (int c = 0; c < ncol; ++c) {
                        ev.old_values[static_cast<std::size_t>(c)] =
                            detail::to_value(
                                sqlite3_column_value(sel_stmt.get(), c));
                    }
                    events.push_back(std::move(ev));
                }
                sel_stmt = detail::StmtGuard{};

                std::string del_sql =
                    "DELETE FROM \"" + td.table + "\" WHERE rowid = ?";
                auto del_stmt = detail::prepare(db, del_sql.c_str());
                sqlite3_bind_int64(del_stmt.get(), 1, rid);
                detail::step_done(db, del_stmt.get());
            }
        }

        // Update seq.
        seq = m.seq;
        detail::write_seq(db, seq, config.seq_key);

        detail::exec(db, "COMMIT");

        if (fk_was_on) {
            detail::exec(db, "PRAGMA foreign_keys = ON");
        }

        state = Replica::State::Live;
        SQLPIPE_LOG(config.on_log, LogLevel::Info, "diff applied, entering live at seq={}", seq);

        return {{AckMsg{m.seq}}, std::move(events), {}};
    }
};

// ── Public API ──────────────────────────────────────────────────────

Replica::Replica(sqlite3* db, ReplicaConfig config)
    : impl_(std::make_unique<Impl>(db, std::move(config))) {
    impl_->init();
}

Replica::~Replica() = default;
Replica::Replica(Replica&&) noexcept = default;
Replica& Replica::operator=(Replica&&) noexcept = default;

Message Replica::hello() const {
    impl_->state = State::Handshake;
    const auto* f = impl_->config.table_filter
        ? &*impl_->config.table_filter : nullptr;
    return HelloMsg{kProtocolVersion,
                    detail::compute_schema_fingerprint(impl_->db, f), {}};
}

HandleResult Replica::handle_message(const Message& msg) {
    auto result = std::visit([&](const auto& m) -> HandleResult {
        using T = std::decay_t<decltype(m)>;

        if constexpr (std::is_same_v<T, HelloMsg>) {
            return impl_->handle_hello_from_master(m);
        }
        else if constexpr (std::is_same_v<T, NeedBucketsMsg>) {
            return impl_->handle_need_buckets(m);
        }
        else if constexpr (std::is_same_v<T, DiffReadyMsg>) {
            return impl_->handle_diff_ready(m);
        }
        else if constexpr (std::is_same_v<T, ChangesetMsg>) {
            if (impl_->state != State::Live) {
                impl_->state = State::Error;
                return {{ErrorMsg{ErrorCode::InvalidState,
                    "received ChangesetMsg in unexpected state"}}, {}, {}};
            }
            auto events = impl_->apply_changeset(m.data, m.seq);
            SQLPIPE_LOG(impl_->config.on_log, LogLevel::Debug, "applied changeset seq={}", m.seq);
            return {{AckMsg{m.seq}}, std::move(events), {}};
        }
        else if constexpr (std::is_same_v<T, ErrorMsg>) {
            if (m.code == ErrorCode::SchemaMismatch &&
                impl_->config.on_schema_mismatch) {
                auto my_sv = schema_version();
                if (impl_->config.on_schema_mismatch(
                        m.remote_schema_version, my_sv,
                        m.remote_schema_sql)) {
                    // Callback modified the local schema. Reset to Init
                    // so the caller can retry the handshake.
                    SQLPIPE_LOG(impl_->config.on_log, LogLevel::Info,
                                "schema mismatch resolved by callback, "
                                "resetting to Init");
                    impl_->state = State::Init;
                    impl_->seq = detail::read_seq(
                        impl_->db, impl_->config.seq_key);
                    return {};
                }
            }
            impl_->state = State::Error;
            SQLPIPE_LOG(impl_->config.on_log, LogLevel::Error, "received error from master: {}", m.detail);
            return {};
        }
        else {
            return {{ErrorMsg{ErrorCode::InvalidState,
                "unexpected message type from master"}}, {}, {}};
        }
    }, msg);

    // Evaluate invalidated subscriptions.
    if (!result.changes.empty() && !impl_->watch.empty()) {
        std::set<std::string> affected;
        for (const auto& ev : result.changes) {
            if (!ev.table.empty()) affected.insert(ev.table);
        }
        result.subscriptions = impl_->watch.notify(affected);
    }

    return result;
}

HandleResult Replica::handle_messages(std::span<const Message> msgs) {
    HandleResult combined;
    std::set<std::string> affected;

    for (const auto& msg : msgs) {
        auto result = std::visit([&](const auto& m) -> HandleResult {
            using T = std::decay_t<decltype(m)>;

            if constexpr (std::is_same_v<T, HelloMsg>) {
                return impl_->handle_hello_from_master(m);
            }
            else if constexpr (std::is_same_v<T, NeedBucketsMsg>) {
                return impl_->handle_need_buckets(m);
            }
            else if constexpr (std::is_same_v<T, DiffReadyMsg>) {
                return impl_->handle_diff_ready(m);
            }
            else if constexpr (std::is_same_v<T, ChangesetMsg>) {
                if (impl_->state != State::Live) {
                    impl_->state = State::Error;
                    return {{ErrorMsg{ErrorCode::InvalidState,
                        "received ChangesetMsg in unexpected state"}}, {}, {}};
                }
                auto events = impl_->apply_changeset(m.data, m.seq);
                SQLPIPE_LOG(impl_->config.on_log, LogLevel::Debug, "applied changeset seq={}", m.seq);
                return {{AckMsg{m.seq}}, std::move(events), {}};
            }
            else if constexpr (std::is_same_v<T, ErrorMsg>) {
                if (m.code == ErrorCode::SchemaMismatch &&
                    impl_->config.on_schema_mismatch) {
                    auto my_sv = schema_version();
                    if (impl_->config.on_schema_mismatch(
                            m.remote_schema_version, my_sv,
                            m.remote_schema_sql)) {
                        SQLPIPE_LOG(impl_->config.on_log, LogLevel::Info,
                                    "schema mismatch resolved by callback, "
                                    "resetting to Init");
                        impl_->state = State::Init;
                        impl_->seq = detail::read_seq(
                            impl_->db, impl_->config.seq_key);
                        return {};
                    }
                }
                impl_->state = State::Error;
                SQLPIPE_LOG(impl_->config.on_log, LogLevel::Error, "received error from master: {}", m.detail);
                return {};
            }
            else {
                return {{ErrorMsg{ErrorCode::InvalidState,
                    "unexpected message type from master"}}, {}, {}};
            }
        }, msg);

        combined.messages.insert(combined.messages.end(),
                                 result.messages.begin(),
                                 result.messages.end());
        for (const auto& ev : result.changes) {
            if (!ev.table.empty()) affected.insert(ev.table);
        }
        combined.changes.insert(combined.changes.end(),
                                result.changes.begin(),
                                result.changes.end());
    }

    // Evaluate subscriptions once for all accumulated changes.
    if (!affected.empty() && !impl_->watch.empty()) {
        combined.subscriptions = impl_->watch.notify(affected);
    }

    return combined;
}

QueryResult Replica::subscribe(const std::string& sql) {
    return impl_->watch.subscribe(sql);
}

void Replica::unsubscribe(SubscriptionId id) {
    impl_->watch.unsubscribe(id);
}

void Replica::reset() {
    impl_->state = State::Init;
    impl_->seq = detail::read_seq(impl_->db, impl_->config.seq_key);
    SQLPIPE_LOG(impl_->config.on_log, LogLevel::Info, "replica reset to Init at seq={}", impl_->seq);
}

Seq Replica::current_seq() const { return impl_->seq; }

SchemaVersion Replica::schema_version() const {
    const auto* f = impl_->config.table_filter
        ? &*impl_->config.table_filter : nullptr;
    return detail::compute_schema_fingerprint(impl_->db, f);
}

Replica::State Replica::state() const { return impl_->state; }

} // namespace sqlpipe

// ── peer_protocol.cpp ───────────────────────────────────────────

namespace sqlpipe {

std::vector<std::uint8_t> serialize(const PeerMessage& msg) {
    auto inner = serialize(msg.payload);  // [4B len][tag][payload]

    // Read inner length from bytes 0..3.
    std::uint32_t inner_len =
        static_cast<std::uint32_t>(inner[0]) |
        (static_cast<std::uint32_t>(inner[1]) << 8) |
        (static_cast<std::uint32_t>(inner[2]) << 16) |
        (static_cast<std::uint32_t>(inner[3]) << 24);

    // New total = inner_len + 1 (for sender_role byte).
    std::uint32_t total = inner_len + 1;

    std::vector<std::uint8_t> buf;
    buf.reserve(4 + 1 + inner_len);

    // Length prefix.
    buf.push_back(static_cast<std::uint8_t>(total));
    buf.push_back(static_cast<std::uint8_t>(total >> 8));
    buf.push_back(static_cast<std::uint8_t>(total >> 16));
    buf.push_back(static_cast<std::uint8_t>(total >> 24));

    // Sender role.
    buf.push_back(static_cast<std::uint8_t>(msg.sender_role));

    // Tag + payload (skip the inner's 4-byte length prefix).
    buf.insert(buf.end(), inner.begin() + 4, inner.end());

    return buf;
}

PeerMessage deserialize_peer(std::span<const std::uint8_t> buf) {
    if (buf.size() < 6) {
        throw Error(ErrorCode::ProtocolError, "peer message too short");
    }

    // Read total length (bytes 0..3).
    std::uint32_t total =
        static_cast<std::uint32_t>(buf[0]) |
        (static_cast<std::uint32_t>(buf[1]) << 8) |
        (static_cast<std::uint32_t>(buf[2]) << 16) |
        (static_cast<std::uint32_t>(buf[3]) << 24);

    auto role = static_cast<SenderRole>(buf[4]);
    if (role != SenderRole::AsMaster && role != SenderRole::AsReplica) {
        throw Error(ErrorCode::ProtocolError,
                    "invalid sender role: " +
                    std::to_string(static_cast<int>(buf[4])));
    }

    // Reconstruct inner Message buffer: [4B len][tag+payload].
    std::uint32_t msg_len = total - 1;
    std::vector<std::uint8_t> msg_buf;
    msg_buf.reserve(4 + msg_len);
    msg_buf.push_back(static_cast<std::uint8_t>(msg_len));
    msg_buf.push_back(static_cast<std::uint8_t>(msg_len >> 8));
    msg_buf.push_back(static_cast<std::uint8_t>(msg_len >> 16));
    msg_buf.push_back(static_cast<std::uint8_t>(msg_len >> 24));
    msg_buf.insert(msg_buf.end(), buf.begin() + 5, buf.end());

    return PeerMessage{role, deserialize(msg_buf)};
}

} // namespace sqlpipe

// ── peer.cpp ────────────────────────────────────────────────────

namespace sqlpipe {

struct Peer::Impl {
    sqlite3*    db;
    PeerConfig  config;
    Peer::State state = Peer::State::Init;

    std::set<std::string> my_tables;
    std::set<std::string> their_tables;

    std::unique_ptr<Master>  master;
    std::unique_ptr<Replica> replica;

    bool master_handshake_done = false;
    bool replica_handshake_done = false;

    bool is_server() const {
        return config.approve_ownership != nullptr;
    }

    void create_master() {
        MasterConfig mc;
        mc.table_filter = my_tables;
        mc.seq_key = "master_seq";
        mc.on_progress = config.on_progress;
        mc.on_schema_mismatch = config.on_schema_mismatch;
        mc.on_log = config.on_log;
        master = std::make_unique<Master>(db, mc);
    }

    void create_replica() {
        ReplicaConfig rc;
        rc.on_conflict = config.on_conflict;
        rc.table_filter = their_tables;
        rc.seq_key = "replica_seq";
        rc.on_progress = config.on_progress;
        rc.on_schema_mismatch = config.on_schema_mismatch;
        rc.on_log = config.on_log;
        replica = std::make_unique<Replica>(db, rc);
    }

    void check_live() {
        if (master_handshake_done && replica_handshake_done) {
            state = Peer::State::Live;
            SQLPIPE_LOG(config.on_log, LogLevel::Info,
                        "peer is live: owning {} tables, replicating {} tables",
                        my_tables.size(), their_tables.size());
        }
    }

    std::set<std::string> complement_tables(
            const std::set<std::string>& subset) {
        std::vector<std::string> all;
        if (config.table_filter) {
            all.assign(config.table_filter->begin(),
                       config.table_filter->end());
        } else {
            all = detail::get_tracked_tables(db);
        }
        std::set<std::string> result;
        for (auto& t : all) {
            if (subset.find(t) == subset.end()) {
                result.insert(std::move(t));
            }
        }
        return result;
    }

    PeerHandleResult handle_as_replica(const PeerMessage& msg) {
        PeerHandleResult result;

        // First AsReplica HelloMsg triggers ownership negotiation.
        if (auto* hello = std::get_if<HelloMsg>(&msg.payload)) {
            if (state == Peer::State::Init ||
                (state == Peer::State::Negotiating && !master)) {
                // Ownership negotiation.
                if (hello->owned_tables.empty()) {
                    state = Peer::State::Error;
                    result.messages.push_back(PeerMessage{
                        SenderRole::AsMaster,
                        ErrorMsg{ErrorCode::ProtocolError,
                                 "peer hello must include owned_tables"}});
                    return result;
                }

                // Reject if client claims tables outside our table_filter.
                if (config.table_filter) {
                    for (const auto& t : hello->owned_tables) {
                        if (config.table_filter->find(t) ==
                                config.table_filter->end()) {
                            state = Peer::State::Error;
                            result.messages.push_back(PeerMessage{
                                SenderRole::AsMaster,
                                ErrorMsg{ErrorCode::OwnershipRejected,
                                    "table '" + t +
                                    "' is not in table_filter"}});
                            return result;
                        }
                    }
                }

                if (config.approve_ownership) {
                    if (!config.approve_ownership(hello->owned_tables)) {
                        state = Peer::State::Error;
                        result.messages.push_back(PeerMessage{
                            SenderRole::AsMaster,
                            ErrorMsg{ErrorCode::OwnershipRejected,
                                     "ownership request rejected"}});
                        return result;
                    }
                }

                // Accept: their tables = what they claimed, ours = complement.
                their_tables = hello->owned_tables;
                my_tables = complement_tables(their_tables);
                state = Peer::State::Diffing;

                create_master();
                create_replica();

                // Patch hello's schema_version to match our Master's so
                // the Master doesn't trigger a spurious schema mismatch.
                HelloMsg patched = *hello;
                patched.schema_version = master->schema_version();
                patched.owned_tables = {};

                auto master_resp = master->handle_message(patched);
                for (auto& m : master_resp) {
                    if (std::holds_alternative<DiffReadyMsg>(m)) {
                        master_handshake_done = true;
                    }
                    result.messages.push_back(
                        PeerMessage{SenderRole::AsMaster, std::move(m)});
                }

                // Also initiate our Replica's hello (to sync their tables).
                auto our_hello = replica->hello();
                auto& h = std::get<HelloMsg>(our_hello);
                h.owned_tables = my_tables;
                result.messages.push_back(
                    PeerMessage{SenderRole::AsReplica, std::move(our_hello)});

                check_live();
                return result;
            }
        }

        // Subsequent AsReplica messages → forward to Master.
        if (!master) {
            result.messages.push_back(PeerMessage{
                SenderRole::AsMaster,
                ErrorMsg{ErrorCode::InvalidState,
                         "master not initialized"}});
            return result;
        }

        auto master_resp = master->handle_message(msg.payload);
        for (auto& m : master_resp) {
            if (std::holds_alternative<DiffReadyMsg>(m)) {
                master_handshake_done = true;
            }
            result.messages.push_back(
                PeerMessage{SenderRole::AsMaster, std::move(m)});
        }

        check_live();
        return result;
    }

    PeerHandleResult handle_as_master(const PeerMessage& msg) {
        PeerHandleResult result;

        if (!replica) {
            result.messages.push_back(PeerMessage{
                SenderRole::AsReplica,
                ErrorMsg{ErrorCode::InvalidState,
                         "replica not initialized"}});
            return result;
        }

        // If this is a HelloMsg, patch schema_version and transition state.
        Message forwarded = msg.payload;
        if (auto* hello = std::get_if<HelloMsg>(&forwarded)) {
            if (state == Peer::State::Negotiating) {
                state = Peer::State::Diffing;
            }
            hello->schema_version = replica->schema_version();
            hello->owned_tables = {};
        }

        auto hr = replica->handle_message(forwarded);

        for (auto& m : hr.messages) {
            result.messages.push_back(
                PeerMessage{SenderRole::AsReplica, std::move(m)});
        }
        result.changes = std::move(hr.changes);
        result.subscriptions = std::move(hr.subscriptions);

        // Check if replica just went live.
        if (replica->state() == Replica::State::Live) {
            replica_handshake_done = true;
            check_live();
        }

        return result;
    }
};

// ── Peer public API ─────────────────────────────────────────────────

Peer::Peer(sqlite3* db, PeerConfig config)
    : impl_(std::make_unique<Impl>()) {
    impl_->db = db;
    impl_->config = std::move(config);
    detail::ensure_meta_table(db);
    SQLPIPE_LOG(impl_->config.on_log, LogLevel::Info, "peer created ({})",
                impl_->is_server() ? "server" : "client");
}

Peer::~Peer() = default;
Peer::Peer(Peer&&) noexcept = default;
Peer& Peer::operator=(Peer&&) noexcept = default;

std::vector<PeerMessage> Peer::start() {
    if (impl_->state != State::Init) {
        throw Error(ErrorCode::InvalidState, "start() already called");
    }
    if (impl_->is_server()) {
        throw Error(ErrorCode::InvalidState,
                    "server peer must not call start()");
    }

    // Validate owned_tables ⊆ table_filter when filter is set.
    if (impl_->config.table_filter) {
        for (const auto& t : impl_->config.owned_tables) {
            if (impl_->config.table_filter->find(t) ==
                    impl_->config.table_filter->end()) {
                throw Error(ErrorCode::InvalidState,
                    "owned_tables entry '" + t +
                    "' is not in table_filter");
            }
        }
    }

    impl_->state = State::Negotiating;
    impl_->my_tables = impl_->config.owned_tables;
    impl_->their_tables = impl_->complement_tables(impl_->my_tables);

    impl_->create_master();
    impl_->create_replica();

    auto hello = impl_->replica->hello();
    auto& h = std::get<HelloMsg>(hello);
    h.owned_tables = impl_->my_tables;

    return {PeerMessage{SenderRole::AsReplica, std::move(hello)}};
}

std::vector<PeerMessage> Peer::flush() {
    if (!impl_->master) return {};
    if (impl_->state != State::Live && impl_->state != State::Diffing) return {};

    auto msgs = impl_->master->flush();
    std::vector<PeerMessage> result;
    result.reserve(msgs.size());
    for (auto& m : msgs) {
        result.push_back(PeerMessage{SenderRole::AsMaster, std::move(m)});
    }
    return result;
}

PeerHandleResult Peer::handle_message(const PeerMessage& msg) {
    if (msg.sender_role == SenderRole::AsReplica) {
        return impl_->handle_as_replica(msg);
    } else {
        return impl_->handle_as_master(msg);
    }
}

QueryResult Peer::subscribe(const std::string& sql) {
    if (!impl_->replica) {
        throw Error(ErrorCode::InvalidState,
                    "subscribe() requires replica to be initialized");
    }
    return impl_->replica->subscribe(sql);
}

void Peer::unsubscribe(SubscriptionId id) {
    if (!impl_->replica) {
        throw Error(ErrorCode::InvalidState,
                    "unsubscribe() requires replica to be initialized");
    }
    impl_->replica->unsubscribe(id);
}

void Peer::reset() {
    impl_->state = State::Init;
    impl_->master.reset();
    impl_->replica.reset();
    impl_->master_handshake_done = false;
    impl_->replica_handshake_done = false;
    SQLPIPE_LOG(impl_->config.on_log, LogLevel::Info, "peer reset to Init");
}

Peer::State Peer::state() const { return impl_->state; }

const std::set<std::string>& Peer::owned_tables() const {
    return impl_->my_tables;
}

const std::set<std::string>& Peer::remote_tables() const {
    return impl_->their_tables;
}

// ── Convenience utilities ────────────────────────────────────────

void sync_handshake(Master& master, Replica& replica) {
    auto pending = master.handle_message(replica.hello());
    while (!pending.empty()) {
        std::vector<Message> for_master;
        for (const auto& msg : pending) {
            auto hr = replica.handle_message(msg);
            for_master.insert(for_master.end(),
                              hr.messages.begin(), hr.messages.end());
        }
        pending.clear();
        for (const auto& msg : for_master) {
            auto resp = master.handle_message(msg);
            pending.insert(pending.end(), resp.begin(), resp.end());
        }
    }
}

void sync_handshake(Peer& client, Peer& server) {
    auto pending_for_server = client.start();
    while (!pending_for_server.empty() ||
           client.state() != Peer::State::Live ||
           server.state() != Peer::State::Live) {
        std::vector<PeerMessage> pending_for_client;
        for (const auto& msg : pending_for_server) {
            auto hr = server.handle_message(msg);
            pending_for_client.insert(pending_for_client.end(),
                                      hr.messages.begin(), hr.messages.end());
        }
        pending_for_server.clear();
        for (const auto& msg : pending_for_client) {
            auto hr = client.handle_message(msg);
            pending_for_server.insert(pending_for_server.end(),
                                      hr.messages.begin(), hr.messages.end());
        }
        if (pending_for_server.empty() &&
            (client.state() != Peer::State::Live ||
             server.state() != Peer::State::Live)) {
            break;
        }
    }
}

} // namespace sqlpipe
