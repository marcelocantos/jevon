// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#pragma once

// sqldeep — C interface for transpiling JSON5-like SQL syntax to standard SQL.
// Callers must free returned strings with sqldeep_free().

#define SQLDEEP_VERSION       "0.8.0"
#define SQLDEEP_VERSION_MAJOR 0
#define SQLDEEP_VERSION_MINOR 8
#define SQLDEEP_VERSION_PATCH 0

#ifdef __cplusplus
extern "C" {
#endif

// FK column pair (borrowed pointers — valid for duration of call).
typedef struct {
    const char* from_column;
    const char* to_column;
} sqldeep_column_pair;

// FK relationship descriptor.
typedef struct {
    const char* from_table;
    const char* to_table;
    const sqldeep_column_pair* columns;
    int column_count;
} sqldeep_foreign_key;

// Target database backend.
typedef enum { SQLDEEP_SQLITE = 0, SQLDEEP_POSTGRES = 1 } sqldeep_backend;

// Convention-based transpile (SQLite backend). Returns malloc'd string (caller
// frees with sqldeep_free). On error returns NULL, sets err_msg/err_line/err_col.
char* sqldeep_transpile(const char* input,
                        char** err_msg, int* err_line, int* err_col);

// FK-guided transpile (SQLite backend).
char* sqldeep_transpile_fk(const char* input,
                           const sqldeep_foreign_key* fks, int fk_count,
                           char** err_msg, int* err_line, int* err_col);

// Convention-based transpile for the specified backend.
char* sqldeep_transpile_backend(const char* input,
                                sqldeep_backend backend,
                                char** err_msg, int* err_line, int* err_col);

// FK-guided transpile for the specified backend.
char* sqldeep_transpile_fk_backend(const char* input,
                                   sqldeep_backend backend,
                                   const sqldeep_foreign_key* fks, int fk_count,
                                   char** err_msg, int* err_line, int* err_col);

// Library version string (static, do not free).
const char* sqldeep_version(void);

// Free a string allocated by the library.
void sqldeep_free(void* ptr);

#ifdef __cplusplus
}
#endif
