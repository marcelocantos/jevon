// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#pragma once

// sqlift - Declarative SQLite schema migration library
// C API for FFI consumers (cgo, etc.). Data interchange is JSON strings.
// Callers must free returned strings with sqlift_free().

#define SQLIFT_VERSION "0.12.0"
#define SQLIFT_VERSION_MAJOR 0
#define SQLIFT_VERSION_MINOR 12
#define SQLIFT_VERSION_PATCH 0

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// Error types returned by all C wrapper functions.
enum sqlift_error_type {
    SQLIFT_OK               = 0,
    SQLIFT_ERROR            = 1,
    SQLIFT_PARSE_ERROR      = 2,
    SQLIFT_EXTRACT_ERROR    = 3,
    SQLIFT_DIFF_ERROR       = 4,
    SQLIFT_APPLY_ERROR      = 5,
    SQLIFT_DRIFT_ERROR      = 6,
    SQLIFT_DESTRUCTIVE_ERROR = 7,
    SQLIFT_BREAKING_CHANGE_ERROR = 8,
    SQLIFT_JSON_ERROR       = 9,
};

// Opaque database handle.
typedef struct sqlift_db sqlift_db;

// Open a database. Returns NULL on error with err_type/err_msg set.
// flags: SQLite open flags (0 = default READWRITE|CREATE).
sqlift_db* sqlift_db_open(const char* path, int flags,
                          int* err_type, char** err_msg);

// Close a database handle. Safe to call with NULL.
void sqlift_db_close(sqlift_db* db);

// Execute SQL with no result. Returns 0 on success, non-zero on error.
int sqlift_db_exec(sqlift_db* db, const char* sql, char** err_msg);

// Parse DDL into a schema. Returns JSON string (caller frees with sqlift_free).
// On error, returns NULL and sets err_type + err_msg.
char* sqlift_parse(const char* ddl, int* err_type, char** err_msg);

// Extract schema from an open database. Returns JSON string.
char* sqlift_extract(sqlift_db* db, int* err_type, char** err_msg);

// Diff two schemas (JSON). Returns migration plan as JSON string.
char* sqlift_diff(const char* current_json, const char* desired_json,
                  int* err_type, char** err_msg);

// Apply a migration plan (JSON) to a database.
// Returns 0 on success, non-zero on error.
int sqlift_apply(sqlift_db* db, const char* plan_json, int allow_destructive,
                 int* err_type, char** err_msg);

// Return the migration version counter (0 if no migrations have run).
int64_t sqlift_migration_version(sqlift_db* db, int* err_type, char** err_msg);

// Detect redundant indexes in a schema (JSON). Returns warnings as JSON array.
char* sqlift_detect_redundant_indexes(const char* schema_json,
                                      int* err_type, char** err_msg);

// Compute the deterministic SHA-256 hash of a schema (JSON).
// Returns the hex hash string (caller frees with sqlift_free).
char* sqlift_schema_hash(const char* schema_json,
                         int* err_type, char** err_msg);

// Execute a query that returns a single int64 value.
// Returns 0 on success, non-zero on error.
int sqlift_db_query_int64(sqlift_db* db, const char* sql,
                          int64_t* result, char** err_msg);

// Execute a query that returns a single TEXT value.
// Returns a malloc'd string on success (caller frees with sqlift_free),
// or NULL on error with err_msg set.
char* sqlift_db_query_text(sqlift_db* db, const char* sql, char** err_msg);

// Free a string or buffer allocated by the C wrapper.
void sqlift_free(void* ptr);

#ifdef __cplusplus
}
#endif
