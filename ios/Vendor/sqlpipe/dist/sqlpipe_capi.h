// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// C API shim for sqlpipe. Provides an extern "C" interface with opaque
// handles so that the C++ library can be called from Go via CGo.
//
// Messages cross the boundary as serialized bytes (wire format).
// HandleResult uses a custom binary encoding (see sqlpipe_capi.cpp).
#pragma once

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// Forward-declare sqlite3 (defined in go-sqlite3's compiled binding).
typedef struct sqlite3 sqlite3;

// ── Opaque handles ──────────────────────────────────────────────

typedef struct sqlpipe_master  sqlpipe_master;
typedef struct sqlpipe_replica sqlpipe_replica;
typedef struct sqlpipe_peer    sqlpipe_peer;

// ── Byte buffer ─────────────────────────────────────────────────

// Heap-allocated byte buffer returned by C functions.
// Caller must free with sqlpipe_free_buf().
typedef struct {
    uint8_t* data;
    size_t   len;
} sqlpipe_buf;

void sqlpipe_free_buf(sqlpipe_buf buf);

// ── Error ───────────────────────────────────────────────────────

// Error result. code == 0 means success.
// If code != 0, msg points to a heap-allocated string (free with sqlpipe_free_error).
typedef struct {
    int   code;  // 0 = OK, >0 = sqlpipe::ErrorCode
    char* msg;   // Heap-allocated error message, or NULL.
} sqlpipe_error;

void sqlpipe_free_error(sqlpipe_error err);

// ── Callback signatures ─────────────────────────────────────────

// Progress callback. phase is DiffPhase enum value.
typedef void (*sqlpipe_progress_fn)(
    void* ctx, uint8_t phase, const char* table,
    int64_t done, int64_t total);

// Schema mismatch callback. Return non-zero to retry after ALTER.
typedef int (*sqlpipe_schema_mismatch_fn)(
    void* ctx, int32_t remote_sv, int32_t local_sv,
    const char* remote_schema_sql);

// Conflict callback. event_data/event_len is an encoded ChangeEvent.
// Return ConflictAction value (0=Omit, 1=Replace, 2=Abort).
typedef uint8_t (*sqlpipe_conflict_fn)(
    void* ctx, uint8_t conflict_type,
    const uint8_t* event_data, size_t event_len);

// Approve ownership callback. Return non-zero to approve.
typedef int (*sqlpipe_approve_ownership_fn)(
    void* ctx, const char** tables, size_t table_count);

// Log callback. level is LogLevel enum value.
typedef void (*sqlpipe_log_fn)(
    void* ctx, uint8_t level, const char* message);

// Flush callback. data/len is encoded messages buffer [u32 count][msg1][msg2]...
typedef void (*sqlpipe_flush_fn)(void* ctx, const uint8_t* data, size_t len);

// ── Config structs ──────────────────────────────────────────────

typedef struct {
    const char** table_filter;       // NULL = all tables.
    size_t       table_filter_count;
    const char*  seq_key;            // NULL = "seq".
    int64_t      bucket_size;        // 0 = default (1024).
    sqlpipe_progress_fn         on_progress;
    void*                       progress_ctx;
    sqlpipe_schema_mismatch_fn  on_schema_mismatch;
    void*                       schema_mismatch_ctx;
    sqlpipe_log_fn              on_log;
    void*                       log_ctx;
    sqlpipe_flush_fn            on_flush;
    void*                       flush_ctx;
} sqlpipe_master_config;

typedef struct {
    sqlpipe_conflict_fn         on_conflict;
    void*                       conflict_ctx;
    const char** table_filter;
    size_t       table_filter_count;
    const char*  seq_key;
    int64_t      bucket_size;
    sqlpipe_progress_fn         on_progress;
    void*                       progress_ctx;
    sqlpipe_schema_mismatch_fn  on_schema_mismatch;
    void*                       schema_mismatch_ctx;
    sqlpipe_log_fn              on_log;
    void*                       log_ctx;
} sqlpipe_replica_config;

typedef struct {
    const char** owned_tables;
    size_t       owned_table_count;
    const char** table_filter;
    size_t       table_filter_count;
    sqlpipe_approve_ownership_fn approve_ownership;
    void*                        approve_ownership_ctx;
    sqlpipe_conflict_fn          on_conflict;
    void*                        conflict_ctx;
    sqlpipe_progress_fn          on_progress;
    void*                        progress_ctx;
    sqlpipe_schema_mismatch_fn   on_schema_mismatch;
    void*                        schema_mismatch_ctx;
    sqlpipe_log_fn               on_log;
    void*                        log_ctx;
} sqlpipe_peer_config;

// ── Master ──────────────────────────────────────────────────────

sqlpipe_error sqlpipe_master_new(
    sqlite3* db, sqlpipe_master_config cfg, sqlpipe_master** out);
void sqlpipe_master_free(sqlpipe_master* m);

// Returns serialized messages: [u32 count][msg1][msg2]...
// Each msg is the wire format [4B len][tag][payload].
sqlpipe_error sqlpipe_master_flush(sqlpipe_master* m, sqlpipe_buf* out);

sqlpipe_error sqlpipe_master_handle_message(
    sqlpipe_master* m,
    const uint8_t* msg_data, size_t msg_len,
    sqlpipe_buf* out);

// Execute SQL on the master's database. If on_flush is set, changes
// are delivered via the callback automatically.
sqlpipe_error sqlpipe_master_exec(sqlpipe_master* m, const char* sql);

int64_t sqlpipe_master_current_seq(sqlpipe_master* m);
int32_t sqlpipe_master_schema_version(sqlpipe_master* m);

// ── Replica ─────────────────────────────────────────────────────

sqlpipe_error sqlpipe_replica_new(
    sqlite3* db, sqlpipe_replica_config cfg, sqlpipe_replica** out);
void sqlpipe_replica_free(sqlpipe_replica* r);

// Returns a single serialized HelloMsg.
sqlpipe_error sqlpipe_replica_hello(sqlpipe_replica* r, sqlpipe_buf* out);

// Returns encoded HandleResult:
//   [u32 msg_count][msgs...][u32 change_count][changes...][u32 sub_count][subs...]
sqlpipe_error sqlpipe_replica_handle_message(
    sqlpipe_replica* r,
    const uint8_t* msg_data, size_t msg_len,
    sqlpipe_buf* out);

// msgs_buf format: [u32 count][msg1][msg2]...
sqlpipe_error sqlpipe_replica_handle_messages(
    sqlpipe_replica* r,
    const uint8_t* msgs_buf, size_t msgs_buf_len,
    sqlpipe_buf* out);

// Returns subscription ID via out parameter.
sqlpipe_error sqlpipe_replica_subscribe(
    sqlpipe_replica* r, const char* sql, uint64_t* out);

sqlpipe_error sqlpipe_replica_unsubscribe(sqlpipe_replica* r, uint64_t id);

void sqlpipe_replica_reset(sqlpipe_replica* r);
uint8_t sqlpipe_replica_state(sqlpipe_replica* r);
int64_t sqlpipe_replica_current_seq(sqlpipe_replica* r);
int32_t sqlpipe_replica_schema_version(sqlpipe_replica* r);

// ── Peer ────────────────────────────────────────────────────────

sqlpipe_error sqlpipe_peer_new(
    sqlite3* db, sqlpipe_peer_config cfg, sqlpipe_peer** out);
void sqlpipe_peer_free(sqlpipe_peer* p);

// Returns serialized PeerMessages: [u32 count][pmsg1][pmsg2]...
sqlpipe_error sqlpipe_peer_start(sqlpipe_peer* p, sqlpipe_buf* out);
sqlpipe_error sqlpipe_peer_flush(sqlpipe_peer* p, sqlpipe_buf* out);

// Subscribe to a query on the peer's replica side. Returns encoded QueryResult.
sqlpipe_error sqlpipe_peer_subscribe(sqlpipe_peer* p, const char* sql, uint64_t* out);
sqlpipe_error sqlpipe_peer_unsubscribe(sqlpipe_peer* p, uint64_t id);

// Returns encoded PeerHandleResult:
//   [u32 msg_count][pmsgs...][u32 change_count][changes...]
sqlpipe_error sqlpipe_peer_handle_message(
    sqlpipe_peer* p,
    const uint8_t* msg_data, size_t msg_len,
    sqlpipe_buf* out);

void sqlpipe_peer_reset(sqlpipe_peer* p);
uint8_t sqlpipe_peer_state(sqlpipe_peer* p);

// Returns table name arrays. Caller must free with sqlpipe_free_string_array.
void sqlpipe_peer_owned_tables(
    sqlpipe_peer* p, char*** out_tables, size_t* out_count);
void sqlpipe_peer_remote_tables(
    sqlpipe_peer* p, char*** out_tables, size_t* out_count);
void sqlpipe_free_string_array(char** arr, size_t count);

// ── Database utilities ──────────────────────────────────────────

// Execute a one-shot query. Returns encoded QueryResult in out.
sqlpipe_error sqlpipe_db_query(sqlite3* db, const char* sql, sqlpipe_buf* out);

// Execute SQL statement(s) without returning results.
sqlpipe_error sqlpipe_db_exec(sqlite3* db, const char* sql);

#ifdef __cplusplus
}
#endif
