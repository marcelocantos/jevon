// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#include "sqlpipe_capi.h"
#include "sqlpipe.h"

#include <cstring>
#include <new>
#include <vector>

// ── Opaque handle structs ───────────────────────────────────────

struct sqlpipe_master {
    sqlpipe::Master impl;
    sqlpipe_master(sqlite3* db, sqlpipe::MasterConfig cfg)
        : impl(db, std::move(cfg)) {}
};

struct sqlpipe_replica {
    sqlpipe::Replica impl;
    sqlpipe_replica(sqlite3* db, sqlpipe::ReplicaConfig cfg)
        : impl(db, std::move(cfg)) {}
};

struct sqlpipe_peer {
    sqlpipe::Peer impl;
    sqlpipe_peer(sqlite3* db, sqlpipe::PeerConfig cfg)
        : impl(db, std::move(cfg)) {}
};

// ── Encoding helpers ────────────────────────────────────────────

namespace {

using Buf = std::vector<uint8_t>;

void put_u8(Buf& b, uint8_t v) { b.push_back(v); }

void put_u32(Buf& b, uint32_t v) {
    b.push_back(v & 0xFF);
    b.push_back((v >> 8) & 0xFF);
    b.push_back((v >> 16) & 0xFF);
    b.push_back((v >> 24) & 0xFF);
}

void put_i32(Buf& b, int32_t v) { put_u32(b, static_cast<uint32_t>(v)); }

void put_u64(Buf& b, uint64_t v) {
    for (int i = 0; i < 8; ++i)
        b.push_back((v >> (i * 8)) & 0xFF);
}

void put_i64(Buf& b, int64_t v) { put_u64(b, static_cast<uint64_t>(v)); }

void put_string(Buf& b, const std::string& s) {
    put_u32(b, static_cast<uint32_t>(s.size()));
    b.insert(b.end(), s.begin(), s.end());
}

void put_bytes(Buf& b, const uint8_t* data, size_t len) {
    b.insert(b.end(), data, data + len);
}

// Encode a sqlpipe::Value.
void encode_value(Buf& b, const sqlpipe::Value& v) {
    if (std::holds_alternative<std::monostate>(v)) {
        put_u8(b, 0x00);
    } else if (auto* p = std::get_if<int64_t>(&v)) {
        put_u8(b, 0x01);
        put_i64(b, *p);
    } else if (auto* p = std::get_if<double>(&v)) {
        put_u8(b, 0x02);
        uint64_t bits;
        std::memcpy(&bits, p, 8);
        put_u64(b, bits);
    } else if (auto* p = std::get_if<std::string>(&v)) {
        put_u8(b, 0x03);
        put_string(b, *p);
    } else if (auto* p = std::get_if<std::vector<uint8_t>>(&v)) {
        put_u8(b, 0x04);
        put_u32(b, static_cast<uint32_t>(p->size()));
        put_bytes(b, p->data(), p->size());
    }
}

// Encode a ChangeEvent.
void encode_change_event(Buf& b, const sqlpipe::ChangeEvent& e) {
    put_string(b, e.table);
    put_u8(b, static_cast<uint8_t>(e.op));
    put_u32(b, static_cast<uint32_t>(e.pk_flags.size()));
    for (bool f : e.pk_flags) put_u8(b, f ? 1 : 0);
    put_u32(b, static_cast<uint32_t>(e.old_values.size()));
    for (auto& v : e.old_values) encode_value(b, v);
    put_u32(b, static_cast<uint32_t>(e.new_values.size()));
    for (auto& v : e.new_values) encode_value(b, v);
}

// Encode a QueryResult.
void encode_query_result(Buf& b, const sqlpipe::QueryResult& qr) {
    put_u64(b, qr.id);
    put_u32(b, static_cast<uint32_t>(qr.columns.size()));
    for (auto& col : qr.columns) put_string(b, col);
    put_u32(b, static_cast<uint32_t>(qr.rows.size()));
    for (auto& row : qr.rows) {
        for (auto& v : row) encode_value(b, v);
    }
}

// Encode vector<Message> as [u32 count][serialized msgs...].
Buf encode_messages(const std::vector<sqlpipe::Message>& msgs) {
    Buf b;
    put_u32(b, static_cast<uint32_t>(msgs.size()));
    for (auto& msg : msgs) {
        auto wire = sqlpipe::serialize(msg);
        put_bytes(b, wire.data(), wire.size());
    }
    return b;
}

// Encode vector<PeerMessage> as [u32 count][serialized pmsgs...].
Buf encode_peer_messages(const std::vector<sqlpipe::PeerMessage>& msgs) {
    Buf b;
    put_u32(b, static_cast<uint32_t>(msgs.size()));
    for (auto& msg : msgs) {
        auto wire = sqlpipe::serialize(msg);
        put_bytes(b, wire.data(), wire.size());
    }
    return b;
}

// Encode HandleResult.
Buf encode_handle_result(const sqlpipe::HandleResult& hr) {
    Buf b;
    // Messages.
    put_u32(b, static_cast<uint32_t>(hr.messages.size()));
    for (auto& msg : hr.messages) {
        auto wire = sqlpipe::serialize(msg);
        put_bytes(b, wire.data(), wire.size());
    }
    // Changes.
    put_u32(b, static_cast<uint32_t>(hr.changes.size()));
    for (auto& ce : hr.changes) encode_change_event(b, ce);
    // Subscriptions.
    put_u32(b, static_cast<uint32_t>(hr.subscriptions.size()));
    for (auto& qr : hr.subscriptions) encode_query_result(b, qr);
    return b;
}

// Encode PeerHandleResult.
Buf encode_peer_handle_result(const sqlpipe::PeerHandleResult& phr) {
    Buf b;
    // Messages.
    put_u32(b, static_cast<uint32_t>(phr.messages.size()));
    for (auto& msg : phr.messages) {
        auto wire = sqlpipe::serialize(msg);
        put_bytes(b, wire.data(), wire.size());
    }
    // Changes.
    put_u32(b, static_cast<uint32_t>(phr.changes.size()));
    for (auto& ce : phr.changes) encode_change_event(b, ce);
    // Subscriptions.
    put_u32(b, static_cast<uint32_t>(phr.subscriptions.size()));
    for (auto& qr : phr.subscriptions) encode_query_result(b, qr);
    return b;
}

// Deserialize multiple messages from a packed buffer.
// Format: [u32 count][msg1 (with 4B length prefix)][msg2]...
std::vector<sqlpipe::Message> decode_messages(const uint8_t* data, size_t len) {
    if (len < 4) return {};
    uint32_t count = data[0] | (data[1] << 8) | (data[2] << 16) | (data[3] << 24);
    size_t pos = 4;
    std::vector<sqlpipe::Message> msgs;
    msgs.reserve(count);
    for (uint32_t i = 0; i < count && pos + 4 <= len; ++i) {
        uint32_t mlen = data[pos] | (data[pos+1] << 8) |
                        (data[pos+2] << 16) | (data[pos+3] << 24);
        size_t total = 4 + mlen;
        if (pos + total > len) break;
        auto msg = sqlpipe::deserialize(
            std::span<const uint8_t>(data + pos, total));
        msgs.push_back(std::move(msg));
        pos += total;
    }
    return msgs;
}

// Copy a Buf into a heap-allocated sqlpipe_buf.
sqlpipe_buf to_buf(Buf&& v) {
    if (v.empty()) return {nullptr, 0};
    auto* p = static_cast<uint8_t*>(std::malloc(v.size()));
    std::memcpy(p, v.data(), v.size());
    return {p, v.size()};
}

sqlpipe_error ok() { return {0, nullptr}; }

sqlpipe_error make_error(const sqlpipe::Error& e) {
    return {static_cast<int>(e.code()), strdup(e.what())};
}

sqlpipe_error make_error(int code, const char* msg) {
    return {code, strdup(msg)};
}

// Convert C config to C++ MasterConfig.
sqlpipe::MasterConfig to_master_config(sqlpipe_master_config cfg) {
    sqlpipe::MasterConfig mc;
    if (cfg.table_filter && cfg.table_filter_count > 0) {
        std::set<std::string> tf;
        for (size_t i = 0; i < cfg.table_filter_count; ++i)
            tf.insert(cfg.table_filter[i]);
        mc.table_filter = std::move(tf);
    }
    if (cfg.seq_key) mc.seq_key = cfg.seq_key;
    if (cfg.bucket_size > 0) mc.bucket_size = cfg.bucket_size;
    if (cfg.on_progress) {
        auto fn = cfg.on_progress;
        auto ctx = cfg.progress_ctx;
        mc.on_progress = [fn, ctx](const sqlpipe::DiffProgress& dp) {
            fn(ctx, static_cast<uint8_t>(dp.phase),
               dp.table.c_str(), dp.items_done, dp.items_total);
        };
    }
    if (cfg.on_schema_mismatch) {
        auto fn = cfg.on_schema_mismatch;
        auto ctx = cfg.schema_mismatch_ctx;
        mc.on_schema_mismatch = [fn, ctx](
            sqlpipe::SchemaVersion rsv, sqlpipe::SchemaVersion lsv,
            const std::string& rsql) -> bool {
            return fn(ctx, rsv, lsv, rsql.c_str()) != 0;
        };
    }
    if (cfg.on_log) {
        auto fn = cfg.on_log;
        auto ctx = cfg.log_ctx;
        mc.on_log = [fn, ctx](sqlpipe::LogLevel level, std::string_view msg) {
            fn(ctx, static_cast<uint8_t>(level),
               std::string(msg).c_str());
        };
    }
    if (cfg.on_flush) {
        auto fn = cfg.on_flush;
        auto ctx = cfg.flush_ctx;
        mc.on_flush = [fn, ctx](const std::vector<sqlpipe::Message>& msgs) {
            auto encoded = encode_messages(msgs);
            fn(ctx, encoded.data(), encoded.size());
        };
    }
    return mc;
}

// Convert C config to C++ ReplicaConfig.
sqlpipe::ReplicaConfig to_replica_config(sqlpipe_replica_config cfg) {
    sqlpipe::ReplicaConfig rc;
    if (cfg.on_conflict) {
        auto fn = cfg.on_conflict;
        auto ctx = cfg.conflict_ctx;
        rc.on_conflict = [fn, ctx](
            sqlpipe::ConflictType ct,
            const sqlpipe::ChangeEvent& ce) -> sqlpipe::ConflictAction {
            Buf eb;
            encode_change_event(eb, ce);
            uint8_t action = fn(ctx, static_cast<uint8_t>(ct), eb.data(), eb.size());
            return static_cast<sqlpipe::ConflictAction>(action);
        };
    }
    if (cfg.table_filter && cfg.table_filter_count > 0) {
        std::set<std::string> tf;
        for (size_t i = 0; i < cfg.table_filter_count; ++i)
            tf.insert(cfg.table_filter[i]);
        rc.table_filter = std::move(tf);
    }
    if (cfg.seq_key) rc.seq_key = cfg.seq_key;
    if (cfg.bucket_size > 0) rc.bucket_size = cfg.bucket_size;
    if (cfg.on_progress) {
        auto fn = cfg.on_progress;
        auto ctx = cfg.progress_ctx;
        rc.on_progress = [fn, ctx](const sqlpipe::DiffProgress& dp) {
            fn(ctx, static_cast<uint8_t>(dp.phase),
               dp.table.c_str(), dp.items_done, dp.items_total);
        };
    }
    if (cfg.on_schema_mismatch) {
        auto fn = cfg.on_schema_mismatch;
        auto ctx = cfg.schema_mismatch_ctx;
        rc.on_schema_mismatch = [fn, ctx](
            sqlpipe::SchemaVersion rsv, sqlpipe::SchemaVersion lsv,
            const std::string& rsql) -> bool {
            return fn(ctx, rsv, lsv, rsql.c_str()) != 0;
        };
    }
    if (cfg.on_log) {
        auto fn = cfg.on_log;
        auto ctx = cfg.log_ctx;
        rc.on_log = [fn, ctx](sqlpipe::LogLevel level, std::string_view msg) {
            fn(ctx, static_cast<uint8_t>(level),
               std::string(msg).c_str());
        };
    }
    return rc;
}

// Convert C config to C++ PeerConfig.
sqlpipe::PeerConfig to_peer_config(sqlpipe_peer_config cfg) {
    sqlpipe::PeerConfig pc;
    for (size_t i = 0; i < cfg.owned_table_count; ++i)
        pc.owned_tables.insert(cfg.owned_tables[i]);
    if (cfg.table_filter && cfg.table_filter_count > 0) {
        std::set<std::string> tf;
        for (size_t i = 0; i < cfg.table_filter_count; ++i)
            tf.insert(cfg.table_filter[i]);
        pc.table_filter = std::move(tf);
    }
    if (cfg.approve_ownership) {
        auto fn = cfg.approve_ownership;
        auto ctx = cfg.approve_ownership_ctx;
        pc.approve_ownership = [fn, ctx](
            const std::set<std::string>& tables) -> bool {
            std::vector<const char*> ptrs;
            ptrs.reserve(tables.size());
            for (auto& t : tables) ptrs.push_back(t.c_str());
            return fn(ctx, ptrs.data(), ptrs.size()) != 0;
        };
    }
    if (cfg.on_conflict) {
        auto fn = cfg.on_conflict;
        auto ctx = cfg.conflict_ctx;
        pc.on_conflict = [fn, ctx](
            sqlpipe::ConflictType ct,
            const sqlpipe::ChangeEvent& ce) -> sqlpipe::ConflictAction {
            Buf eb;
            encode_change_event(eb, ce);
            uint8_t action = fn(ctx, static_cast<uint8_t>(ct), eb.data(), eb.size());
            return static_cast<sqlpipe::ConflictAction>(action);
        };
    }
    if (cfg.on_progress) {
        auto fn = cfg.on_progress;
        auto ctx = cfg.progress_ctx;
        pc.on_progress = [fn, ctx](const sqlpipe::DiffProgress& dp) {
            fn(ctx, static_cast<uint8_t>(dp.phase),
               dp.table.c_str(), dp.items_done, dp.items_total);
        };
    }
    if (cfg.on_schema_mismatch) {
        auto fn = cfg.on_schema_mismatch;
        auto ctx = cfg.schema_mismatch_ctx;
        pc.on_schema_mismatch = [fn, ctx](
            sqlpipe::SchemaVersion rsv, sqlpipe::SchemaVersion lsv,
            const std::string& rsql) -> bool {
            return fn(ctx, rsv, lsv, rsql.c_str()) != 0;
        };
    }
    if (cfg.on_log) {
        auto fn = cfg.on_log;
        auto ctx = cfg.log_ctx;
        pc.on_log = [fn, ctx](sqlpipe::LogLevel level, std::string_view msg) {
            fn(ctx, static_cast<uint8_t>(level),
               std::string(msg).c_str());
        };
    }
    return pc;
}

// Helper to copy a std::set<std::string> into a C string array.
void set_to_string_array(const std::set<std::string>& s,
                         char*** out, size_t* count) {
    *count = s.size();
    if (s.empty()) { *out = nullptr; return; }
    *out = static_cast<char**>(std::malloc(s.size() * sizeof(char*)));
    size_t i = 0;
    for (auto& str : s) {
        (*out)[i++] = strdup(str.c_str());
    }
}

} // anonymous namespace

// ── Free functions ──────────────────────────────────────────────

extern "C" {

void sqlpipe_free_buf(sqlpipe_buf buf) {
    std::free(buf.data);
}

void sqlpipe_free_error(sqlpipe_error err) {
    std::free(err.msg);
}

void sqlpipe_free_string_array(char** arr, size_t count) {
    if (!arr) return;
    for (size_t i = 0; i < count; ++i) std::free(arr[i]);
    std::free(arr);
}

// ── Master ──────────────────────────────────────────────────────

sqlpipe_error sqlpipe_master_new(
    sqlite3* db, sqlpipe_master_config cfg, sqlpipe_master** out) {
    try {
        *out = new sqlpipe_master(db, to_master_config(cfg));
        return ok();
    } catch (const sqlpipe::Error& e) {
        *out = nullptr;
        return make_error(e);
    } catch (const std::exception& e) {
        *out = nullptr;
        return make_error(1, e.what());
    }
}

void sqlpipe_master_free(sqlpipe_master* m) { delete m; }

sqlpipe_error sqlpipe_master_flush(sqlpipe_master* m, sqlpipe_buf* out) {
    try {
        auto msgs = m->impl.flush();
        *out = to_buf(encode_messages(msgs));
        return ok();
    } catch (const sqlpipe::Error& e) { return make_error(e); }
      catch (const std::exception& e) { return make_error(1, e.what()); }
}

sqlpipe_error sqlpipe_master_exec(sqlpipe_master* m, const char* sql) {
    try {
        m->impl.exec(sql);
        return ok();
    } catch (const sqlpipe::Error& e) { return make_error(e); }
      catch (const std::exception& e) { return make_error(1, e.what()); }
}

sqlpipe_error sqlpipe_master_handle_message(
    sqlpipe_master* m,
    const uint8_t* msg_data, size_t msg_len,
    sqlpipe_buf* out) {
    try {
        auto msg = sqlpipe::deserialize(
            std::span<const uint8_t>(msg_data, msg_len));
        auto resp = m->impl.handle_message(msg);
        *out = to_buf(encode_messages(resp));
        return ok();
    } catch (const sqlpipe::Error& e) { return make_error(e); }
      catch (const std::exception& e) { return make_error(1, e.what()); }
}

int64_t sqlpipe_master_current_seq(sqlpipe_master* m) {
    return m->impl.current_seq();
}

int32_t sqlpipe_master_schema_version(sqlpipe_master* m) {
    return m->impl.schema_version();
}

// ── Replica ─────────────────────────────────────────────────────

sqlpipe_error sqlpipe_replica_new(
    sqlite3* db, sqlpipe_replica_config cfg, sqlpipe_replica** out) {
    try {
        *out = new sqlpipe_replica(db, to_replica_config(cfg));
        return ok();
    } catch (const sqlpipe::Error& e) {
        *out = nullptr;
        return make_error(e);
    } catch (const std::exception& e) {
        *out = nullptr;
        return make_error(1, e.what());
    }
}

void sqlpipe_replica_free(sqlpipe_replica* r) { delete r; }

sqlpipe_error sqlpipe_replica_hello(sqlpipe_replica* r, sqlpipe_buf* out) {
    try {
        auto msg = r->impl.hello();
        auto wire = sqlpipe::serialize(msg);
        *out = to_buf(Buf(wire.begin(), wire.end()));
        return ok();
    } catch (const sqlpipe::Error& e) { return make_error(e); }
      catch (const std::exception& e) { return make_error(1, e.what()); }
}

sqlpipe_error sqlpipe_replica_handle_message(
    sqlpipe_replica* r,
    const uint8_t* msg_data, size_t msg_len,
    sqlpipe_buf* out) {
    try {
        auto msg = sqlpipe::deserialize(
            std::span<const uint8_t>(msg_data, msg_len));
        auto hr = r->impl.handle_message(msg);
        *out = to_buf(encode_handle_result(hr));
        return ok();
    } catch (const sqlpipe::Error& e) { return make_error(e); }
      catch (const std::exception& e) { return make_error(1, e.what()); }
}

sqlpipe_error sqlpipe_replica_handle_messages(
    sqlpipe_replica* r,
    const uint8_t* msgs_buf, size_t msgs_buf_len,
    sqlpipe_buf* out) {
    try {
        auto msgs = decode_messages(msgs_buf, msgs_buf_len);
        auto hr = r->impl.handle_messages(msgs);
        *out = to_buf(encode_handle_result(hr));
        return ok();
    } catch (const sqlpipe::Error& e) { return make_error(e); }
      catch (const std::exception& e) { return make_error(1, e.what()); }
}

sqlpipe_error sqlpipe_replica_subscribe(
    sqlpipe_replica* r, const char* sql, uint64_t* out) {
    try {
        *out = r->impl.subscribe(sql);
        return ok();
    } catch (const sqlpipe::Error& e) { return make_error(e); }
      catch (const std::exception& e) { return make_error(1, e.what()); }
}

sqlpipe_error sqlpipe_replica_unsubscribe(sqlpipe_replica* r, uint64_t id) {
    try {
        r->impl.unsubscribe(id);
        return ok();
    } catch (const sqlpipe::Error& e) { return make_error(e); }
      catch (const std::exception& e) { return make_error(1, e.what()); }
}

void sqlpipe_replica_reset(sqlpipe_replica* r) {
    r->impl.reset();
}

uint8_t sqlpipe_replica_state(sqlpipe_replica* r) {
    return static_cast<uint8_t>(r->impl.state());
}

int64_t sqlpipe_replica_current_seq(sqlpipe_replica* r) {
    return r->impl.current_seq();
}

int32_t sqlpipe_replica_schema_version(sqlpipe_replica* r) {
    return r->impl.schema_version();
}

// ── Peer ────────────────────────────────────────────────────────

sqlpipe_error sqlpipe_peer_new(
    sqlite3* db, sqlpipe_peer_config cfg, sqlpipe_peer** out) {
    try {
        *out = new sqlpipe_peer(db, to_peer_config(cfg));
        return ok();
    } catch (const sqlpipe::Error& e) {
        *out = nullptr;
        return make_error(e);
    } catch (const std::exception& e) {
        *out = nullptr;
        return make_error(1, e.what());
    }
}

void sqlpipe_peer_free(sqlpipe_peer* p) { delete p; }

sqlpipe_error sqlpipe_peer_start(sqlpipe_peer* p, sqlpipe_buf* out) {
    try {
        auto msgs = p->impl.start();
        *out = to_buf(encode_peer_messages(msgs));
        return ok();
    } catch (const sqlpipe::Error& e) { return make_error(e); }
      catch (const std::exception& e) { return make_error(1, e.what()); }
}

sqlpipe_error sqlpipe_peer_flush(sqlpipe_peer* p, sqlpipe_buf* out) {
    try {
        auto msgs = p->impl.flush();
        *out = to_buf(encode_peer_messages(msgs));
        return ok();
    } catch (const sqlpipe::Error& e) { return make_error(e); }
      catch (const std::exception& e) { return make_error(1, e.what()); }
}

sqlpipe_error sqlpipe_peer_handle_message(
    sqlpipe_peer* p,
    const uint8_t* msg_data, size_t msg_len,
    sqlpipe_buf* out) {
    try {
        auto msg = sqlpipe::deserialize_peer(
            std::span<const uint8_t>(msg_data, msg_len));
        auto phr = p->impl.handle_message(msg);
        *out = to_buf(encode_peer_handle_result(phr));
        return ok();
    } catch (const sqlpipe::Error& e) { return make_error(e); }
      catch (const std::exception& e) { return make_error(1, e.what()); }
}

sqlpipe_error sqlpipe_peer_subscribe(
    sqlpipe_peer* p, const char* sql, uint64_t* out) {
    try {
        *out = p->impl.subscribe(sql);
        return ok();
    } catch (const sqlpipe::Error& e) { return make_error(e); }
      catch (const std::exception& e) { return make_error(1, e.what()); }
}

sqlpipe_error sqlpipe_peer_unsubscribe(sqlpipe_peer* p, uint64_t id) {
    try {
        p->impl.unsubscribe(id);
        return ok();
    } catch (const sqlpipe::Error& e) { return make_error(e); }
      catch (const std::exception& e) { return make_error(1, e.what()); }
}

void sqlpipe_peer_reset(sqlpipe_peer* p) {
    p->impl.reset();
}

uint8_t sqlpipe_peer_state(sqlpipe_peer* p) {
    return static_cast<uint8_t>(p->impl.state());
}

void sqlpipe_peer_owned_tables(
    sqlpipe_peer* p, char*** out_tables, size_t* out_count) {
    set_to_string_array(p->impl.owned_tables(), out_tables, out_count);
}

void sqlpipe_peer_remote_tables(
    sqlpipe_peer* p, char*** out_tables, size_t* out_count) {
    set_to_string_array(p->impl.remote_tables(), out_tables, out_count);
}

// ── Database utilities ──────────────────────────────────────────

sqlpipe_error sqlpipe_db_query(sqlite3* db, const char* sql, sqlpipe_buf* out) {
    try {
        auto qr = sqlpipe::query(db, sql);
        Buf b;
        encode_query_result(b, qr);
        *out = to_buf(std::move(b));
        return ok();
    } catch (const sqlpipe::Error& e) { return make_error(e); }
      catch (const std::exception& e) { return make_error(1, e.what()); }
}

sqlpipe_error sqlpipe_db_exec(sqlite3* db, const char* sql) {
    char* err = nullptr;
    int rc = sqlite3_exec(db, sql, nullptr, nullptr, &err);
    if (rc != SQLITE_OK) {
        std::string msg = err ? err : "sqlite3_exec failed";
        if (err) sqlite3_free(err);
        return make_error(1, msg.c_str());
    }
    return ok();
}

} // extern "C"
