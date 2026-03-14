// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// sqlift - Declarative SQLite schema migration library

#include "sqlift.h"

#include <cstdint>
#include <map>
#include <stdexcept>
#include <string>
#include <vector>
#include <sqlite3.h>

namespace sqlift {

// --- error.h ---

class Error : public std::runtime_error {
    using std::runtime_error::runtime_error;
};

class ParseError : public Error {
    using Error::Error;
};

class ExtractError : public Error {
    using Error::Error;
};

class DiffError : public Error {
    using Error::Error;
};

class ApplyError : public Error {
    using Error::Error;
};

class DriftError : public Error {
    using Error::Error;
};

class DestructiveError : public Error {
    using Error::Error;
};

class BreakingChangeError : public Error {
    using Error::Error;
};

class JsonError : public Error {
    using Error::Error;
};

// --- sqlite_util.h ---

// RAII wrapper for sqlite3*.
class Database {
public:
    explicit Database(const std::string& path,
                      int flags = SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE);
    ~Database();

    Database(Database&& other) noexcept;
    Database& operator=(Database&& other) noexcept;
    Database(const Database&) = delete;
    Database& operator=(const Database&) = delete;

    sqlite3* get() const { return db_; }
    operator sqlite3*() const { return db_; }

    void exec(const std::string& sql);

private:
    sqlite3* db_ = nullptr;
};

// RAII wrapper for sqlite3_stmt*.
class Statement {
public:
    Statement(sqlite3* db, const std::string& sql);
    ~Statement();

    Statement(Statement&& other) noexcept;
    Statement& operator=(Statement&& other) noexcept;
    Statement(const Statement&) = delete;
    Statement& operator=(const Statement&) = delete;

    bool step();

    int64_t column_int(int col) const;
    std::string column_text(int col) const;

    void bind_text(int param, const std::string& value);
    void bind_int(int param, int64_t value);

    sqlite3_stmt* get() const { return stmt_; }

private:
    sqlite3_stmt* stmt_ = nullptr;
};

// --- schema.h ---

enum class GeneratedType {
    Normal  = 0,
    Virtual = 2,
    Stored  = 3,
};

struct Column {
    std::string name;
    std::string type;
    bool notnull = false;
    std::string default_value;
    int pk = 0;
    std::string collation;
    GeneratedType generated = GeneratedType::Normal;
    std::string generated_expr;

    bool operator==(const Column&) const = default;
};

struct CheckConstraint {
    std::string name;
    std::string expression;
    bool operator==(const CheckConstraint&) const = default;
};

struct ForeignKey {
    std::string constraint_name;
    std::vector<std::string> from_columns;
    std::string to_table;
    std::vector<std::string> to_columns;
    std::string on_update = "NO ACTION";
    std::string on_delete = "NO ACTION";

    bool operator==(const ForeignKey& o) const {
        return from_columns == o.from_columns && to_table == o.to_table &&
               to_columns == o.to_columns && on_update == o.on_update &&
               on_delete == o.on_delete;
    }
};

struct Table {
    std::string name;
    std::vector<Column> columns;
    std::vector<ForeignKey> foreign_keys;
    std::vector<CheckConstraint> check_constraints;
    std::string pk_constraint_name;
    bool without_rowid = false;
    bool strict = false;
    std::string raw_sql;

    bool operator==(const Table& o) const {
        return name == o.name && columns == o.columns &&
               foreign_keys == o.foreign_keys &&
               check_constraints == o.check_constraints &&
               without_rowid == o.without_rowid &&
               strict == o.strict;
    }
};

struct Index {
    std::string name;
    std::string table_name;
    std::vector<std::string> columns;
    bool unique = false;
    std::string where_clause;
    std::string raw_sql;

    bool operator==(const Index& o) const {
        return name == o.name && table_name == o.table_name &&
               columns == o.columns && unique == o.unique &&
               where_clause == o.where_clause;
    }
};

struct View {
    std::string name;
    std::string sql;

    bool operator==(const View&) const = default;
};

struct Trigger {
    std::string name;
    std::string table_name;
    std::string sql;

    bool operator==(const Trigger&) const = default;
};

struct Schema {
    std::map<std::string, Table>   tables;
    std::map<std::string, Index>   indexes;
    std::map<std::string, View>    views;
    std::map<std::string, Trigger> triggers;

    bool operator==(const Schema&) const = default;

    std::string hash() const;
};

// --- parse.h ---

Schema parse(const std::string& sql);

// --- extract.h ---

Schema extract(sqlite3* db);

// --- diff.h ---

enum class WarningType {
    RedundantIndex,
};

struct Warning {
    WarningType type;
    std::string message;
    std::string index_name;
    std::string covered_by;
    std::string table_name;
};

enum class OpType {
    CreateTable,
    DropTable,
    RebuildTable,
    AddColumn,
    CreateIndex,
    DropIndex,
    CreateView,
    DropView,
    CreateTrigger,
    DropTrigger,
};

struct Operation {
    OpType type;
    std::string object_name;
    std::string description;
    std::vector<std::string> sql;
    bool destructive = false;
};

class MigrationPlan {
public:
    const std::vector<Operation>& operations() const { return ops_; }
    const std::vector<Warning>& warnings() const { return warnings_; }
    bool has_destructive_operations() const;
    bool empty() const { return ops_.empty(); }

private:
    friend MigrationPlan diff(const Schema& current, const Schema& desired);
    friend MigrationPlan from_json(const std::string& json_str);
    std::vector<Operation> ops_;
    std::vector<Warning> warnings_;
};

MigrationPlan diff(const Schema& current, const Schema& desired);

std::vector<Warning> detect_redundant_indexes(const Schema& schema);

// --- apply.h ---

struct ApplyOptions {
    bool allow_destructive = false;
};

void apply(sqlite3* db, const MigrationPlan& plan, const ApplyOptions& opts = {});

int64_t migration_version(sqlite3* db);

// --- json.h ---

std::string to_string(OpType type);
OpType op_type_from_string(const std::string& s);
std::string to_json(const MigrationPlan& plan);
MigrationPlan from_json(const std::string& json_str);
std::string schema_to_json(const Schema& schema);
Schema schema_from_json(const std::string& json_str);

} // namespace sqlift

#include <algorithm>
#include <array>
#include <cstdlib>
#include <cstring>
#include <iomanip>
#include <set>
#include <sstream>
#include <utility>

#include <nlohmann/json.hpp>

namespace sqlift {

// --- sqlite_util.cpp ---




// --- Database ---

Database::Database(const std::string& path, int flags) {
    int rc = sqlite3_open_v2(path.c_str(), &db_, flags, nullptr);
    if (rc != SQLITE_OK) {
        std::string msg = db_ ? sqlite3_errmsg(db_) : "failed to allocate memory";
        sqlite3_close(db_);
        db_ = nullptr;
        throw Error("sqlite3_open_v2: " + msg);
    }
}

Database::~Database() {
    if (db_) sqlite3_close(db_);
}

Database::Database(Database&& other) noexcept : db_(other.db_) {
    other.db_ = nullptr;
}

Database& Database::operator=(Database&& other) noexcept {
    if (this != &other) {
        if (db_) sqlite3_close(db_);
        db_ = other.db_;
        other.db_ = nullptr;
    }
    return *this;
}

void Database::exec(const std::string& sql) {
    char* errmsg = nullptr;
    int rc = sqlite3_exec(db_, sql.c_str(), nullptr, nullptr, &errmsg);
    if (rc != SQLITE_OK) {
        std::string msg = errmsg ? errmsg : "unknown error";
        sqlite3_free(errmsg);
        throw Error("sqlite3_exec: " + msg);
    }
}

// --- Statement ---

Statement::Statement(sqlite3* db, const std::string& sql) {
    int rc = sqlite3_prepare_v2(db, sql.c_str(), -1, &stmt_, nullptr);
    if (rc != SQLITE_OK) {
        throw Error(std::string("sqlite3_prepare_v2: ") + sqlite3_errmsg(db));
    }
}

Statement::~Statement() {
    if (stmt_) sqlite3_finalize(stmt_);
}

Statement::Statement(Statement&& other) noexcept : stmt_(other.stmt_) {
    other.stmt_ = nullptr;
}

Statement& Statement::operator=(Statement&& other) noexcept {
    if (this != &other) {
        if (stmt_) sqlite3_finalize(stmt_);
        stmt_ = other.stmt_;
        other.stmt_ = nullptr;
    }
    return *this;
}

bool Statement::step() {
    int rc = sqlite3_step(stmt_);
    if (rc == SQLITE_ROW) return true;
    if (rc == SQLITE_DONE) return false;
    throw Error(std::string("sqlite3_step: ") +
                sqlite3_errmsg(sqlite3_db_handle(stmt_)));
}

int64_t Statement::column_int(int col) const {
    return sqlite3_column_int64(stmt_, col);
}

std::string Statement::column_text(int col) const {
    const unsigned char* text = sqlite3_column_text(stmt_, col);
    if (!text) return {};
    return reinterpret_cast<const char*>(text);
}

void Statement::bind_text(int param, const std::string& value) {
    int rc = sqlite3_bind_text(stmt_, param, value.c_str(), -1, SQLITE_TRANSIENT);
    if (rc != SQLITE_OK) {
        throw Error(std::string("sqlite3_bind_text: ") +
                    sqlite3_errmsg(sqlite3_db_handle(stmt_)));
    }
}

void Statement::bind_int(int param, int64_t value) {
    int rc = sqlite3_bind_int64(stmt_, param, value);
    if (rc != SQLITE_OK) {
        throw Error(std::string("sqlite3_bind_int: ") +
                    sqlite3_errmsg(sqlite3_db_handle(stmt_)));
    }
}


// --- hash.cpp ---



namespace {

constexpr std::array<uint32_t, 64> K = {
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5,
    0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
    0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc,
    0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7,
    0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
    0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3,
    0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5,
    0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
    0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
};

inline uint32_t rotr(uint32_t x, int n) { return (x >> n) | (x << (32 - n)); }
inline uint32_t ch(uint32_t x, uint32_t y, uint32_t z) { return (x & y) ^ (~x & z); }
inline uint32_t maj(uint32_t x, uint32_t y, uint32_t z) { return (x & y) ^ (x & z) ^ (y & z); }
inline uint32_t sigma0(uint32_t x) { return rotr(x, 2) ^ rotr(x, 13) ^ rotr(x, 22); }
inline uint32_t sigma1(uint32_t x) { return rotr(x, 6) ^ rotr(x, 11) ^ rotr(x, 25); }
inline uint32_t gamma0(uint32_t x) { return rotr(x, 7) ^ rotr(x, 18) ^ (x >> 3); }
inline uint32_t gamma1(uint32_t x) { return rotr(x, 17) ^ rotr(x, 19) ^ (x >> 10); }

std::string sha256(const std::string& input) {
    // Pre-processing: pad message
    uint64_t bit_len = input.size() * 8;
    std::vector<uint8_t> msg(input.begin(), input.end());
    msg.push_back(0x80);
    while ((msg.size() % 64) != 56)
        msg.push_back(0x00);
    for (int i = 7; i >= 0; --i)
        msg.push_back(static_cast<uint8_t>(bit_len >> (i * 8)));

    // Initial hash values
    uint32_t h0 = 0x6a09e667, h1 = 0xbb67ae85, h2 = 0x3c6ef372, h3 = 0xa54ff53a;
    uint32_t h4 = 0x510e527f, h5 = 0x9b05688c, h6 = 0x1f83d9ab, h7 = 0x5be0cd19;

    // Process each 512-bit block
    for (size_t offset = 0; offset < msg.size(); offset += 64) {
        std::array<uint32_t, 64> w{};
        for (int i = 0; i < 16; ++i) {
            w[i] = (uint32_t(msg[offset + i * 4]) << 24) |
                   (uint32_t(msg[offset + i * 4 + 1]) << 16) |
                   (uint32_t(msg[offset + i * 4 + 2]) << 8) |
                   uint32_t(msg[offset + i * 4 + 3]);
        }
        for (int i = 16; i < 64; ++i)
            w[i] = gamma1(w[i - 2]) + w[i - 7] + gamma0(w[i - 15]) + w[i - 16];

        uint32_t a = h0, b = h1, c = h2, d = h3;
        uint32_t e = h4, f = h5, g = h6, h = h7;

        for (int i = 0; i < 64; ++i) {
            uint32_t t1 = h + sigma1(e) + ch(e, f, g) + K[i] + w[i];
            uint32_t t2 = sigma0(a) + maj(a, b, c);
            h = g; g = f; f = e; e = d + t1;
            d = c; c = b; b = a; a = t1 + t2;
        }

        h0 += a; h1 += b; h2 += c; h3 += d;
        h4 += e; h5 += f; h6 += g; h7 += h;
    }

    std::ostringstream oss;
    oss << std::hex << std::setfill('0');
    for (uint32_t v : {h0, h1, h2, h3, h4, h5, h6, h7})
        oss << std::setw(8) << v;
    return oss.str();
}

} // namespace


// --- schema.cpp ---




std::string Schema::hash() const {
    std::ostringstream oss;

    for (const auto& [name, table] : tables) {
        oss << "TABLE " << name << '\n';
        for (const auto& col : table.columns) {
            oss << "  COL " << col.name
                << ' ' << col.type
                << (col.notnull ? " NOTNULL" : "")
                << " DEFAULT=" << col.default_value
                << " PK=" << col.pk;
            if (!col.collation.empty())
                oss << " COLLATE=" << col.collation;
            if (col.generated != GeneratedType::Normal)
                oss << " GENERATED=" << static_cast<int>(col.generated);
            if (!col.generated_expr.empty())
                oss << " EXPR=" << col.generated_expr;
            oss << '\n';
        }
        for (const auto& fk : table.foreign_keys) {
            oss << "  FK";
            for (const auto& c : fk.from_columns) oss << ' ' << c;
            oss << " -> " << fk.to_table << '(';
            for (size_t i = 0; i < fk.to_columns.size(); ++i) {
                if (i > 0) oss << ',';
                oss << fk.to_columns[i];
            }
            oss << ") UPDATE=" << fk.on_update
                << " DELETE=" << fk.on_delete << '\n';
        }
        for (const auto& chk : table.check_constraints) {
            oss << "  CHECK";
            if (!chk.name.empty()) oss << " NAME=" << chk.name;
            oss << " EXPR=" << chk.expression << '\n';
        }
        oss << "  ROWID=" << (table.without_rowid ? "no" : "yes") << '\n';
        if (table.strict)
            oss << "  STRICT=yes\n";
    }

    for (const auto& [name, idx] : indexes) {
        oss << "INDEX " << name << " ON " << idx.table_name;
        oss << (idx.unique ? " UNIQUE" : "");
        for (const auto& c : idx.columns) oss << ' ' << c;
        if (!idx.where_clause.empty()) oss << " WHERE " << idx.where_clause;
        oss << '\n';
    }

    for (const auto& [name, view] : views)
        oss << "VIEW " << name << ' ' << view.sql << '\n';

    for (const auto& [name, trigger] : triggers)
        oss << "TRIGGER " << name << ' ' << trigger.sql << '\n';

    return sha256(oss.str());
}


// --- extract.cpp ---




namespace {

bool starts_with(const std::string& s, const std::string& prefix) {
    return s.size() >= prefix.size() && s.compare(0, prefix.size(), prefix) == 0;
}

std::string to_upper(const std::string& s) {
    std::string result = s;
    std::transform(result.begin(), result.end(), result.begin(),
                   [](unsigned char c) { return std::toupper(c); });
    return result;
}

// Quote an identifier for use in SQL.
std::string quote_id(const std::string& name) {
    // Use double quotes, escaping embedded double quotes.
    std::string result = "\"";
    for (char c : name) {
        if (c == '"') result += "\"\"";
        else result += c;
    }
    result += '"';
    return result;
}

std::string trim(const std::string& s) {
    auto start = s.find_first_not_of(" \t\n\r");
    if (start == std::string::npos) return {};
    auto end = s.find_last_not_of(" \t\n\r");
    return s.substr(start, end - start + 1);
}

std::string strip_quotes(const std::string& s) {
    if (s.size() >= 2 &&
        ((s.front() == '"' && s.back() == '"') ||
         (s.front() == '[' && s.back() == ']') ||
         (s.front() == '`' && s.back() == '`'))) {
        return s.substr(1, s.size() - 2);
    }
    return s;
}

// Parse the body of a CREATE TABLE statement to extract CHECK constraints
// and GENERATED ALWAYS AS expressions.
// Returns a pair: (check_constraints, column_name -> generated_expr map).
struct ParsedTableBody {
    std::vector<CheckConstraint> checks;
    std::map<std::string, std::string> generated_exprs;
    std::string pk_constraint_name;
    std::map<std::string, std::string> fk_constraint_names;  // key: comma-joined from_columns
};

ParsedTableBody parse_create_table_body(const std::string& raw_sql) {
    ParsedTableBody result;

    // Find the outer '(' of CREATE TABLE ... (...)
    int depth = 0;
    size_t body_start = std::string::npos;
    size_t body_end = std::string::npos;
    for (size_t i = 0; i < raw_sql.size(); ++i) {
        if (raw_sql[i] == '\'') {
            ++i; // skip opening quote
            while (i < raw_sql.size()) {
                if (raw_sql[i] == '\'' && i + 1 < raw_sql.size() && raw_sql[i + 1] == '\'')
                    i += 2; // skip escaped quote
                else if (raw_sql[i] == '\'')
                    break;
                else
                    ++i;
            }
            continue; // i now points at closing quote; loop ++i advances past it
        } else if (raw_sql[i] == '(') {
            if (depth == 0) body_start = i + 1;
            ++depth;
        } else if (raw_sql[i] == ')') {
            --depth;
            if (depth == 0) {
                body_end = i;
                break;
            }
        }
    }
    if (body_start == std::string::npos || body_end == std::string::npos)
        return result;

    // Split inner content by ',' at depth 0
    std::vector<std::string> defs;
    depth = 0;
    size_t seg_start = body_start;
    for (size_t i = body_start; i < body_end; ++i) {
        if (raw_sql[i] == '\'') {
            ++i; // skip opening quote
            while (i < body_end) {
                if (raw_sql[i] == '\'' && i + 1 < body_end && raw_sql[i + 1] == '\'')
                    i += 2; // skip escaped quote
                else if (raw_sql[i] == '\'')
                    break;
                else
                    ++i;
            }
            continue; // i now points at closing quote; loop ++i advances past it
        } else if (raw_sql[i] == '(') ++depth;
        else if (raw_sql[i] == ')') --depth;
        else if (raw_sql[i] == ',' && depth == 0) {
            defs.push_back(trim(raw_sql.substr(seg_start, i - seg_start)));
            seg_start = i + 1;
        }
    }
    defs.push_back(trim(raw_sql.substr(seg_start, body_end - seg_start)));

    for (const auto& def : defs) {
        std::string upper_def = to_upper(def);

        // Check for table-level CHECK constraint
        // Could be: CHECK(...) or CONSTRAINT name CHECK(...)
        bool is_check = false;
        CheckConstraint chk;

        if (starts_with(upper_def, "CHECK")) {
            is_check = true;
            // Extract expression from CHECK(...)
            auto paren = def.find('(');
            if (paren != std::string::npos) {
                // Find matching close paren
                int d = 0;
                size_t expr_end = std::string::npos;
                for (size_t i = paren; i < def.size(); ++i) {
                    if (def[i] == '(') ++d;
                    else if (def[i] == ')') {
                        --d;
                        if (d == 0) { expr_end = i; break; }
                    }
                }
                if (expr_end != std::string::npos)
                    chk.expression = trim(def.substr(paren + 1, expr_end - paren - 1));
            }
        } else if (starts_with(upper_def, "CONSTRAINT")) {
            // CONSTRAINT name CHECK/PRIMARY KEY/FOREIGN KEY(...)
            auto check_pos = upper_def.find("CHECK");
            if (check_pos != std::string::npos) {
                is_check = true;
                // Extract constraint name: between CONSTRAINT and CHECK
                chk.name = strip_quotes(trim(def.substr(10, check_pos - 10)));
                // Extract expression
                auto paren = def.find('(', check_pos);
                if (paren != std::string::npos) {
                    int d = 0;
                    size_t expr_end = std::string::npos;
                    for (size_t i = paren; i < def.size(); ++i) {
                        if (def[i] == '(') ++d;
                        else if (def[i] == ')') {
                            --d;
                            if (d == 0) { expr_end = i; break; }
                        }
                    }
                    if (expr_end != std::string::npos)
                        chk.expression = trim(def.substr(paren + 1, expr_end - paren - 1));
                }
            } else {
                auto pk_pos = upper_def.find("PRIMARY KEY");
                auto fk_pos = upper_def.find("FOREIGN KEY");
                if (pk_pos != std::string::npos) {
                    result.pk_constraint_name =
                        strip_quotes(trim(def.substr(10, pk_pos - 10)));
                } else if (fk_pos != std::string::npos) {
                    std::string name_part =
                        strip_quotes(trim(def.substr(10, fk_pos - 10)));
                    // Extract from_columns from FOREIGN KEY(col1, col2)
                    auto paren = def.find('(', fk_pos);
                    if (paren != std::string::npos) {
                        int d = 0;
                        size_t cols_end = std::string::npos;
                        for (size_t i = paren; i < def.size(); ++i) {
                            if (def[i] == '(') ++d;
                            else if (def[i] == ')') {
                                --d;
                                if (d == 0) { cols_end = i; break; }
                            }
                        }
                        if (cols_end != std::string::npos) {
                            std::string cols_str = def.substr(paren + 1, cols_end - paren - 1);
                            std::string key;
                            std::istringstream css(cols_str);
                            std::string col;
                            bool first = true;
                            while (std::getline(css, col, ',')) {
                                if (!first) key += ',';
                                key += strip_quotes(trim(col));
                                first = false;
                            }
                            result.fk_constraint_names[key] = name_part;
                        }
                    }
                }
                continue;
            }
        }

        if (is_check) {
            result.checks.push_back(std::move(chk));
            continue;
        }

        // Check for column-level GENERATED ALWAYS AS (expr)
        auto gen_pos = upper_def.find("GENERATED ALWAYS AS");
        if (gen_pos != std::string::npos) {
            // Extract column name (first token of the definition)
            auto first_space = def.find_first_of(" \t");
            std::string col_name;
            if (first_space != std::string::npos)
                col_name = def.substr(0, first_space);
            else
                col_name = def;
            col_name = strip_quotes(col_name);

            // Find the expression in parens after GENERATED ALWAYS AS
            auto paren = def.find('(', gen_pos);
            if (paren != std::string::npos) {
                int d = 0;
                size_t expr_end = std::string::npos;
                for (size_t i = paren; i < def.size(); ++i) {
                    if (def[i] == '(') ++d;
                    else if (def[i] == ')') {
                        --d;
                        if (d == 0) { expr_end = i; break; }
                    }
                }
                if (expr_end != std::string::npos)
                    result.generated_exprs[col_name] =
                        trim(def.substr(paren + 1, expr_end - paren - 1));
            }
        }
    }

    return result;
}

// Parse table options after the closing ')' of CREATE TABLE.
// Returns (without_rowid, strict).
std::pair<bool, bool> parse_table_options(const std::string& raw_sql) {
    bool without_rowid = false;
    bool strict = false;

    // Find the last ')' at depth 0
    int depth = 0;
    size_t close_paren = std::string::npos;
    for (size_t i = 0; i < raw_sql.size(); ++i) {
        if (raw_sql[i] == '(') ++depth;
        else if (raw_sql[i] == ')') {
            --depth;
            if (depth == 0) { close_paren = i; break; }
        }
    }
    if (close_paren == std::string::npos || close_paren + 1 >= raw_sql.size())
        return {without_rowid, strict};

    std::string tail = raw_sql.substr(close_paren + 1);
    // Split by comma
    std::istringstream iss(tail);
    std::string token;
    while (std::getline(iss, token, ',')) {
        std::string t = to_upper(trim(token));
        if (t == "WITHOUT ROWID") without_rowid = true;
        else if (t == "STRICT") strict = true;
    }

    return {without_rowid, strict};
}

} // namespace

Schema extract(sqlite3* db) {
    Schema schema;

    // Query sqlite_master for all user-defined objects.
    Statement master_stmt(db,
        "SELECT type, name, tbl_name, sql FROM sqlite_master "
        "WHERE type IN ('table', 'index', 'view', 'trigger') "
        "AND name NOT LIKE 'sqlite_%' "
        "AND name != '_sqlift_state' "
        "ORDER BY type, name");

    struct MasterRow {
        std::string type, name, tbl_name, sql;
    };
    std::vector<MasterRow> rows;
    while (master_stmt.step()) {
        rows.push_back({
            master_stmt.column_text(0),
            master_stmt.column_text(1),
            master_stmt.column_text(2),
            master_stmt.column_text(3),
        });
    }

    for (const auto& row : rows) {
        if (row.type == "table") {
            Table table;
            table.name = row.name;
            table.raw_sql = row.sql;

            // Detect WITHOUT ROWID and STRICT from table options
            auto [wor, strict_flag] = parse_table_options(row.sql);
            table.without_rowid = wor;
            table.strict = strict_flag;

            // Columns via PRAGMA table_xinfo (includes generated column info)
            Statement col_stmt(db,
                "PRAGMA table_xinfo(" + quote_id(row.name) + ")");
            while (col_stmt.step()) {
                Column col;
                col.name = col_stmt.column_text(1);
                col.type = to_upper(col_stmt.column_text(2));
                col.notnull = col_stmt.column_int(3) != 0;
                col.default_value = col_stmt.column_text(4);
                col.pk = static_cast<int>(col_stmt.column_int(5));
                auto hidden = col_stmt.column_int(6);
                if (hidden != 0 && hidden != 2 && hidden != 3)
                    throw ExtractError("Unsupported generated column type: " + std::to_string(hidden));
                col.generated = static_cast<GeneratedType>(hidden);
                table.columns.push_back(std::move(col));
            }

            // Collation via sqlite3_table_column_metadata
            for (auto& col : table.columns) {
                const char* collation = nullptr;
                int rc = sqlite3_table_column_metadata(
                    db, nullptr, row.name.c_str(), col.name.c_str(),
                    nullptr, &collation, nullptr, nullptr, nullptr);
                if (rc == SQLITE_OK && collation) {
                    std::string coll = collation;
                    if (to_upper(coll) != "BINARY")
                        col.collation = to_upper(coll);
                }
            }

            // Parse CHECK constraints and GENERATED expressions from raw_sql
            auto parsed = parse_create_table_body(row.sql);
            table.check_constraints = std::move(parsed.checks);
            for (auto& col : table.columns) {
                auto it = parsed.generated_exprs.find(col.name);
                if (it != parsed.generated_exprs.end())
                    col.generated_expr = it->second;
            }

            // Foreign keys via PRAGMA foreign_key_list
            Statement fk_stmt(db,
                "PRAGMA foreign_key_list(" + quote_id(row.name) + ")");
            // FK rows are grouped by id (seq=0 starts a new FK).
            std::map<int, ForeignKey> fk_map;
            while (fk_stmt.step()) {
                int id = static_cast<int>(fk_stmt.column_int(0));
                int seq = static_cast<int>(fk_stmt.column_int(1));
                if (seq == 0) {
                    ForeignKey fk;
                    fk.to_table = fk_stmt.column_text(2);
                    fk.on_update = to_upper(fk_stmt.column_text(5));
                    fk.on_delete = to_upper(fk_stmt.column_text(6));
                    fk_map[id] = std::move(fk);
                }
                fk_map[id].from_columns.push_back(fk_stmt.column_text(3));
                fk_map[id].to_columns.push_back(fk_stmt.column_text(4));
            }
            for (auto& [_, fk] : fk_map)
                table.foreign_keys.push_back(std::move(fk));

            // Populate constraint names from parsed raw_sql
            table.pk_constraint_name = std::move(parsed.pk_constraint_name);
            for (auto& fk : table.foreign_keys) {
                std::string key;
                for (size_t i = 0; i < fk.from_columns.size(); ++i) {
                    if (i > 0) key += ',';
                    key += fk.from_columns[i];
                }
                auto it = parsed.fk_constraint_names.find(key);
                if (it != parsed.fk_constraint_names.end())
                    fk.constraint_name = it->second;
            }

            schema.tables[table.name] = std::move(table);
        }
        else if (row.type == "index") {
            // Skip auto-indexes
            if (starts_with(row.name, "sqlite_autoindex_")) continue;
            // Auto-indexes have NULL sql
            if (row.sql.empty()) continue;

            Index idx;
            idx.name = row.name;
            idx.table_name = row.tbl_name;
            idx.raw_sql = row.sql;

            // Uniqueness via PRAGMA index_list (authoritative)
            {
                Statement il_stmt(db,
                    "PRAGMA index_list(" + quote_id(row.tbl_name) + ")");
                while (il_stmt.step()) {
                    if (il_stmt.column_text(1) == row.name) {
                        idx.unique = il_stmt.column_int(2) != 0;
                        break;
                    }
                }
            }

            // Columns via PRAGMA index_info
            Statement idx_info(db,
                "PRAGMA index_info(" + quote_id(row.name) + ")");
            while (idx_info.step()) {
                std::string col_name = idx_info.column_text(2);
                if (col_name.empty()) {
                    // Expression index — extract from raw SQL
                    col_name = "<expr>";
                }
                idx.columns.push_back(std::move(col_name));
            }

            // Partial index WHERE clause: extract from raw SQL.
            // Uses rfind to find the last WHERE, then checks it's at
            // top-level (not inside parentheses or string literals).
            auto upper_sql = to_upper(row.sql);
            auto where_pos = upper_sql.rfind("WHERE");
            if (where_pos != std::string::npos) {
                int paren_depth = 0;
                bool in_string = false;
                char string_char = 0;
                for (size_t i = 0; i < where_pos; ++i) {
                    char c = row.sql[i];
                    if (in_string) {
                        if (c == string_char) {
                            if (i + 1 < where_pos && row.sql[i + 1] == string_char)
                                ++i; // escaped quote
                            else
                                in_string = false;
                        }
                    } else {
                        if (c == '\'' || c == '"') {
                            in_string = true;
                            string_char = c;
                        } else if (c == '(') {
                            ++paren_depth;
                        } else if (c == ')') {
                            --paren_depth;
                        }
                    }
                }
                if (paren_depth == 0) {
                    idx.where_clause = row.sql.substr(where_pos + 6);
                    // Trim leading/trailing whitespace
                    auto start = idx.where_clause.find_first_not_of(" \t\n\r");
                    auto end = idx.where_clause.find_last_not_of(" \t\n\r");
                    if (start != std::string::npos)
                        idx.where_clause = idx.where_clause.substr(start, end - start + 1);
                }
            }

            schema.indexes[idx.name] = std::move(idx);
        }
        else if (row.type == "view") {
            View view;
            view.name = row.name;
            view.sql = row.sql;
            schema.views[view.name] = std::move(view);
        }
        else if (row.type == "trigger") {
            Trigger trigger;
            trigger.name = row.name;
            trigger.table_name = row.tbl_name;
            trigger.sql = row.sql;
            schema.triggers[trigger.name] = std::move(trigger);
        }
    }

    return schema;
}


// --- parse.cpp ---



Schema parse(const std::string& sql) {
    Database db(":memory:");

    try {
        db.exec(sql);
    } catch (const Error& e) {
        throw ParseError(std::string("Failed to parse schema SQL: ") + e.what());
    }

    return extract(db);
}


// --- diff.cpp ---




namespace {

// Check if a column can be added via simple ALTER TABLE ADD COLUMN.
bool can_add_column(const Column& col) {
    // SQLite restrictions on ADD COLUMN:
    // - Cannot be PRIMARY KEY
    // - Must have DEFAULT or allow NULL if NOT NULL
    // - Cannot be a generated column
    if (col.pk != 0) return false;
    if (col.notnull && col.default_value.empty()) return false;
    if (col.generated != GeneratedType::Normal) return false;
    return true;
}

// Extract SQL references: tokenize SQL into identifiers and check against known names.
// Excludes the object's own name.
std::set<std::string> extract_sql_references(
    const std::string& sql, const std::string& own_name,
    const std::set<std::string>& known_names)
{
    std::set<std::string> refs;
    std::string word;
    for (size_t i = 0; i <= sql.size(); ++i) {
        char c = (i < sql.size()) ? sql[i] : '\0';
        if (std::isalnum(static_cast<unsigned char>(c)) || c == '_') {
            word += c;
        } else {
            if (!word.empty()) {
                if (word != own_name && known_names.count(word))
                    refs.insert(word);
                word.clear();
            }
        }
    }
    return refs;
}

// Topological sort using Kahn's algorithm.
// If reverse==true, returns reverse topological order (dependents first).
std::vector<std::string> topo_sort(
    const std::vector<std::string>& nodes,
    const std::map<std::string, std::set<std::string>>& deps,
    bool reverse = false)
{
    // Build in-degree map
    std::map<std::string, int> in_degree;
    std::map<std::string, std::set<std::string>> dependents;
    for (const auto& n : nodes) in_degree[n] = 0;

    for (const auto& n : nodes) {
        auto it = deps.find(n);
        if (it != deps.end()) {
            for (const auto& dep : it->second) {
                if (in_degree.count(dep)) {
                    in_degree[n]++;
                    dependents[dep].insert(n);
                }
            }
        }
    }

    std::vector<std::string> queue;
    for (const auto& n : nodes) {
        if (in_degree[n] == 0)
            queue.push_back(n);
    }
    // Sort queue for deterministic ordering
    std::sort(queue.begin(), queue.end());

    std::vector<std::string> result;
    size_t front = 0;
    while (front < queue.size()) {
        std::string n = queue[front++];
        result.push_back(n);
        if (dependents.count(n)) {
            std::vector<std::string> newly_free;
            for (const auto& dep : dependents[n]) {
                if (--in_degree[dep] == 0)
                    newly_free.push_back(dep);
            }
            std::sort(newly_free.begin(), newly_free.end());
            for (auto& nf : newly_free)
                queue.push_back(std::move(nf));
        }
    }

    if (result.size() != nodes.size())
        throw DiffError("Circular dependency detected among views/triggers");

    if (reverse)
        std::reverse(result.begin(), result.end());

    return result;
}

// Check if the only difference is columns appended at the end (AddColumn fast path).
bool is_append_only(const Table& current, const Table& desired) {
    // All existing columns must be unchanged
    if (desired.columns.size() <= current.columns.size()) return false;
    for (size_t i = 0; i < current.columns.size(); ++i) {
        if (!(current.columns[i] == desired.columns[i])) return false;
    }
    // Foreign keys must be unchanged
    if (current.foreign_keys != desired.foreign_keys) return false;
    // CHECK constraints must be unchanged
    if (current.check_constraints != desired.check_constraints) return false;
    // WITHOUT ROWID must be unchanged
    if (current.without_rowid != desired.without_rowid) return false;
    // STRICT must be unchanged
    if (current.strict != desired.strict) return false;
    // All new columns must be addable
    for (size_t i = current.columns.size(); i < desired.columns.size(); ++i) {
        if (!can_add_column(desired.columns[i])) return false;
    }
    return true;
}

// Build an ADD COLUMN SQL statement.
std::string add_column_sql(const std::string& table_name, const Column& col) {
    std::ostringstream oss;
    oss << "ALTER TABLE " << quote_id(table_name)
        << " ADD COLUMN " << quote_id(col.name);
    if (!col.type.empty()) oss << ' ' << col.type;
    if (!col.collation.empty()) oss << " COLLATE " << col.collation;
    if (col.notnull) oss << " NOT NULL";
    if (!col.default_value.empty()) oss << " DEFAULT " << col.default_value;
    return oss.str();
}

// Build the SQL for a 12-step table rebuild.
std::vector<std::string> rebuild_table_sql(
    const Table& current, const Table& desired,
    const Schema& desired_schema)
{
    std::vector<std::string> stmts;
    std::string tmp_name = quote_id(desired.name + "_sqlift_new");
    std::string tbl_name = quote_id(desired.name);

    // Step 1: Disable foreign keys
    stmts.push_back("PRAGMA foreign_keys=OFF");

    // Step 2: Begin transaction
    stmts.push_back("SAVEPOINT sqlift_rebuild");

    // Step 3: Create new table with desired schema
    stmts.push_back(desired.raw_sql);
    // Replace the table name in the CREATE TABLE statement with the temp name.
    // The raw_sql has the real name; we need to create with the temp name.
    auto& create_stmt = stmts.back();
    // Replace first occurrence of table name after CREATE TABLE
    {
        std::string create_sql = desired.raw_sql;
        // Find the table name in the CREATE TABLE statement and replace with tmp name.
        // Reconstruct: CREATE TABLE <tmp_name> (rest...)
        auto paren_pos = create_sql.find('(');
        if (paren_pos != std::string::npos) {
            create_stmt = "CREATE TABLE " + tmp_name +
                          " " + create_sql.substr(paren_pos);
        }
    }

    // Step 4: Copy data from old table to new (common columns only).
    // Skip generated columns — they are computed and can't be inserted into.
    std::vector<std::string> common_cols;
    std::set<std::string> desired_col_names;
    std::set<std::string> generated_col_names;
    for (const auto& col : desired.columns) {
        desired_col_names.insert(col.name);
        if (col.generated != GeneratedType::Normal)
            generated_col_names.insert(col.name);
    }
    for (const auto& col : current.columns) {
        if (desired_col_names.count(col.name) && !generated_col_names.count(col.name))
            common_cols.push_back(quote_id(col.name));
    }
    if (!common_cols.empty()) {
        std::ostringstream oss;
        oss << "INSERT INTO " << tmp_name << " (";
        for (size_t i = 0; i < common_cols.size(); ++i) {
            if (i > 0) oss << ", ";
            oss << common_cols[i];
        }
        oss << ") SELECT ";
        for (size_t i = 0; i < common_cols.size(); ++i) {
            if (i > 0) oss << ", ";
            oss << common_cols[i];
        }
        oss << " FROM " << tbl_name;
        stmts.push_back(oss.str());
    }

    // Step 5: Drop old table
    stmts.push_back("DROP TABLE " + tbl_name);

    // Step 6: Rename new table
    stmts.push_back("ALTER TABLE " + tmp_name + " RENAME TO " + tbl_name);

    // Step 7: Recreate indexes for this table
    for (const auto& [idx_name, idx] : desired_schema.indexes) {
        if (idx.table_name == desired.name && !idx.raw_sql.empty()) {
            stmts.push_back(idx.raw_sql);
        }
    }

    // Step 8: Recreate triggers for this table
    for (const auto& [trig_name, trig] : desired_schema.triggers) {
        if (trig.table_name == desired.name && !trig.sql.empty()) {
            stmts.push_back(trig.sql);
        }
    }

    // Step 9: (content verification — skipped, sqlift uses FK check instead)

    // Step 10: FK check
    stmts.push_back("PRAGMA foreign_key_check(" + quote_id(desired.name) + ")");

    // Step 11: Release savepoint
    stmts.push_back("RELEASE SAVEPOINT sqlift_rebuild");

    // Step 12: Re-enable foreign keys
    stmts.push_back("PRAGMA foreign_keys=ON");

    return stmts;
}

// Describe what changed between two tables.
std::string describe_table_changes(const Table& current, const Table& desired) {
    std::ostringstream oss;
    oss << "Rebuild table " << desired.name << ":";

    // Find added/removed/changed columns
    std::set<std::string> current_cols, desired_cols;
    std::map<std::string, const Column*> current_col_map, desired_col_map;
    for (const auto& c : current.columns) {
        current_cols.insert(c.name);
        current_col_map[c.name] = &c;
    }
    for (const auto& c : desired.columns) {
        desired_cols.insert(c.name);
        desired_col_map[c.name] = &c;
    }

    for (const auto& name : desired_cols) {
        if (!current_cols.count(name))
            oss << " add column " << name << ";";
    }
    for (const auto& name : current_cols) {
        if (!desired_cols.count(name))
            oss << " drop column " << name << ";";
    }
    for (const auto& name : current_cols) {
        if (desired_cols.count(name)) {
            const auto* c = current_col_map[name];
            const auto* d = desired_col_map[name];
            if (!(*c == *d))
                oss << " modify column " << name << ";";
        }
    }

    if (current.foreign_keys != desired.foreign_keys)
        oss << " foreign keys changed;";
    if (current.check_constraints != desired.check_constraints)
        oss << " CHECK constraints changed;";
    if (current.without_rowid != desired.without_rowid)
        oss << " WITHOUT ROWID changed;";
    if (current.strict != desired.strict)
        oss << " STRICT changed;";

    return oss.str();
}

bool rebuild_is_destructive(const Table& current, const Table& desired) {
    std::set<std::string> desired_cols;
    for (const auto& c : desired.columns)
        desired_cols.insert(c.name);
    for (const auto& c : current.columns) {
        if (!desired_cols.count(c.name))
            return true; // Column removed
    }
    return false;
}

} // namespace

bool MigrationPlan::has_destructive_operations() const {
    return std::any_of(ops_.begin(), ops_.end(),
                       [](const Operation& op) { return op.destructive; });
}

MigrationPlan diff(const Schema& current, const Schema& desired) {
    MigrationPlan plan;

    // Build known names for dependency analysis
    std::set<std::string> known_names;
    for (const auto& [n, _] : current.tables) known_names.insert(n);
    for (const auto& [n, _] : current.views) known_names.insert(n);
    for (const auto& [n, _] : desired.tables) known_names.insert(n);
    for (const auto& [n, _] : desired.views) known_names.insert(n);

    // --- Phase 1: Drop triggers that are removed or changed ---
    // Collect triggers to drop, then sort by reverse dependency order
    {
        std::vector<std::string> to_drop;
        std::map<std::string, bool> drop_destructive;
        for (const auto& [name, trig] : current.triggers) {
            auto it = desired.triggers.find(name);
            if (it == desired.triggers.end() || it->second.sql != trig.sql) {
                to_drop.push_back(name);
                drop_destructive[name] = (it == desired.triggers.end());
            }
        }
        // Build dependency graph for triggers being dropped
        std::set<std::string> drop_set(to_drop.begin(), to_drop.end());
        std::map<std::string, std::set<std::string>> deps;
        for (const auto& name : to_drop) {
            deps[name] = extract_sql_references(
                current.triggers.at(name).sql, name, known_names);
            // Only keep deps that are also being dropped
            std::set<std::string> filtered;
            for (const auto& d : deps[name])
                if (drop_set.count(d)) filtered.insert(d);
            deps[name] = std::move(filtered);
        }
        auto sorted = topo_sort(to_drop, deps, true);
        for (const auto& name : sorted) {
            plan.ops_.push_back({
                .type = OpType::DropTrigger,
                .object_name = name,
                .description = "Drop trigger " + name,
                .sql = {"DROP TRIGGER IF EXISTS " + quote_id(name)},
                .destructive = drop_destructive[name],
            });
        }
    }

    // --- Phase 2: Drop views that are removed or changed ---
    // Sort by reverse dependency order (dependents dropped first)
    {
        std::vector<std::string> to_drop;
        std::map<std::string, bool> drop_destructive;
        for (const auto& [name, view] : current.views) {
            auto it = desired.views.find(name);
            if (it == desired.views.end() || it->second.sql != view.sql) {
                to_drop.push_back(name);
                drop_destructive[name] = (it == desired.views.end());
            }
        }
        std::map<std::string, std::set<std::string>> deps;
        for (const auto& name : to_drop) {
            deps[name] = extract_sql_references(
                current.views.at(name).sql, name, known_names);
        }
        auto sorted = topo_sort(to_drop, deps, true);
        for (const auto& name : sorted) {
            plan.ops_.push_back({
                .type = OpType::DropView,
                .object_name = name,
                .description = "Drop view " + name,
                .sql = {"DROP VIEW IF EXISTS " + quote_id(name)},
                .destructive = drop_destructive[name],
            });
        }
    }

    // --- Phase 3: Drop indexes that are removed or changed ---
    // Also drop indexes on tables that will be rebuilt (they get recreated in the rebuild).
    std::set<std::string> tables_to_rebuild;

    // Pre-scan to find which tables need rebuilding
    for (const auto& [name, table] : desired.tables) {
        auto it = current.tables.find(name);
        if (it != current.tables.end() && !(it->second == table)) {
            if (!is_append_only(it->second, table)) {
                tables_to_rebuild.insert(name);
            }
        }
    }

    for (const auto& [name, idx] : current.indexes) {
        auto it = desired.indexes.find(name);
        bool needs_drop = false;

        if (it == desired.indexes.end()) {
            needs_drop = true;
        } else if (!(it->second == idx)) {
            needs_drop = true;
        } else if (tables_to_rebuild.count(idx.table_name)) {
            // Index will be recreated as part of rebuild
            needs_drop = true;
        }

        if (needs_drop) {
            plan.ops_.push_back({
                .type = OpType::DropIndex,
                .object_name = name,
                .description = "Drop index " + name,
                .sql = {"DROP INDEX IF EXISTS " + quote_id(name)},
                .destructive = (it == desired.indexes.end()),
            });
        }
    }

    // --- Phase 4: Table operations ---

    // Create new tables
    for (const auto& [name, table] : desired.tables) {
        if (!current.tables.count(name)) {
            plan.ops_.push_back({
                .type = OpType::CreateTable,
                .object_name = name,
                .description = "Create table " + name,
                .sql = {table.raw_sql},
                .destructive = false,
            });
        }
    }

    // Check for breaking changes across all modified tables before building the plan.
    {
        std::vector<std::string> violations;
        for (const auto& [name, desired_table] : desired.tables) {
            auto it = current.tables.find(name);
            if (it == current.tables.end()) continue;
            const auto& current_table = it->second;
            if (current_table == desired_table) continue;

            // Build column lookup for the current table.
            std::map<std::string, const Column*> cur_col_map;
            for (const auto& col : current_table.columns)
                cur_col_map[col.name] = &col;

            // (a) Existing nullable column becomes NOT NULL.
            for (const auto& col : desired_table.columns) {
                auto cit = cur_col_map.find(col.name);
                if (cit != cur_col_map.end() && !cit->second->notnull && col.notnull) {
                    violations.push_back(
                        "Table '" + name + "': column '" + col.name +
                        "' changes from nullable to NOT NULL");
                }
            }

            // (b) New FK constraint on existing table.
            for (const auto& fk : desired_table.foreign_keys) {
                bool found = false;
                for (const auto& cur_fk : current_table.foreign_keys) {
                    if (cur_fk == fk) { found = true; break; }
                }
                if (!found) {
                    std::ostringstream oss;
                    oss << "Table '" << name << "': adds foreign key (";
                    for (size_t i = 0; i < fk.from_columns.size(); ++i) {
                        if (i > 0) oss << ", ";
                        oss << fk.from_columns[i];
                    }
                    oss << ") references " << fk.to_table << "(";
                    for (size_t i = 0; i < fk.to_columns.size(); ++i) {
                        if (i > 0) oss << ", ";
                        oss << fk.to_columns[i];
                    }
                    oss << ")";
                    violations.push_back(oss.str());
                }
            }

            // (c) New CHECK constraint on existing table (existing data may violate it).
            for (const auto& chk : desired_table.check_constraints) {
                bool found = false;
                for (const auto& cur_chk : current_table.check_constraints) {
                    if (cur_chk == chk) { found = true; break; }
                }
                if (!found) {
                    violations.push_back(
                        "Table '" + name + "': adds CHECK constraint" +
                        (chk.name.empty() ? "" : " '" + chk.name + "'") +
                        " (" + chk.expression + ")");
                }
            }

            // (d) New NOT NULL column without DEFAULT (guaranteed failure on non-empty table).
            for (const auto& col : desired_table.columns) {
                if (cur_col_map.find(col.name) == cur_col_map.end() &&
                    col.notnull && col.default_value.empty() && col.pk == 0) {
                    violations.push_back(
                        "Table '" + name + "': new column '" + col.name +
                        "' is NOT NULL without DEFAULT");
                }
            }
        }
        if (!violations.empty()) {
            std::ostringstream oss;
            oss << "Breaking schema changes detected:";
            for (const auto& v : violations)
                oss << "\n- " << v;
            throw BreakingChangeError(oss.str());
        }
    }

    // Modify existing tables
    for (const auto& [name, desired_table] : desired.tables) {
        auto it = current.tables.find(name);
        if (it == current.tables.end()) continue;
        const auto& current_table = it->second;

        if (current_table == desired_table) continue;

        if (is_append_only(current_table, desired_table)) {
            // AddColumn fast path
            for (size_t i = current_table.columns.size();
                 i < desired_table.columns.size(); ++i)
            {
                plan.ops_.push_back({
                    .type = OpType::AddColumn,
                    .object_name = name,
                    .description = "Add column " + desired_table.columns[i].name +
                                   " to " + name,
                    .sql = {add_column_sql(name, desired_table.columns[i])},
                    .destructive = false,
                });
            }
        } else {
            // Full rebuild
            plan.ops_.push_back({
                .type = OpType::RebuildTable,
                .object_name = name,
                .description = describe_table_changes(current_table, desired_table),
                .sql = rebuild_table_sql(current_table, desired_table, desired),
                .destructive = rebuild_is_destructive(current_table, desired_table),
            });
        }
    }

    // Drop removed tables
    for (const auto& [name, table] : current.tables) {
        if (!desired.tables.count(name)) {
            plan.ops_.push_back({
                .type = OpType::DropTable,
                .object_name = name,
                .description = "Drop table " + name,
                .sql = {"DROP TABLE IF EXISTS " + quote_id(name)},
                .destructive = true,
            });
        }
    }

    // --- Phase 5: Create indexes (not part of rebuilds) ---
    for (const auto& [name, idx] : desired.indexes) {
        auto it = current.indexes.find(name);
        bool needs_create = false;

        if (it == current.indexes.end()) {
            needs_create = true;
        } else if (!(it->second == idx)) {
            needs_create = true;
        }

        // Skip indexes on rebuilt tables (they were recreated in the rebuild)
        if (tables_to_rebuild.count(idx.table_name)) continue;

        if (needs_create) {
            plan.ops_.push_back({
                .type = OpType::CreateIndex,
                .object_name = name,
                .description = "Create index " + name + " on " + idx.table_name,
                .sql = {idx.raw_sql},
                .destructive = false,
            });
        }
    }

    // --- Phase 6: Create views ---
    // Sort by topological order (dependencies created first)
    {
        std::vector<std::string> to_create;
        for (const auto& [name, view] : desired.views) {
            auto it = current.views.find(name);
            if (it == current.views.end() || it->second.sql != view.sql) {
                to_create.push_back(name);
            }
        }
        // Build known names from desired schema for create ordering
        std::set<std::string> desired_known;
        for (const auto& [n, _] : desired.tables) desired_known.insert(n);
        for (const auto& [n, _] : desired.views) desired_known.insert(n);

        std::map<std::string, std::set<std::string>> deps;
        for (const auto& name : to_create) {
            deps[name] = extract_sql_references(
                desired.views.at(name).sql, name, desired_known);
        }
        auto sorted = topo_sort(to_create, deps, false);
        for (const auto& name : sorted) {
            plan.ops_.push_back({
                .type = OpType::CreateView,
                .object_name = name,
                .description = "Create view " + name,
                .sql = {desired.views.at(name).sql},
                .destructive = false,
            });
        }
    }

    // --- Phase 7: Create triggers ---
    // Sort by topological order (dependencies created first)
    {
        std::vector<std::string> to_create;
        for (const auto& [name, trig] : desired.triggers) {
            auto it = current.triggers.find(name);
            if (it == current.triggers.end() || it->second.sql != trig.sql) {
                to_create.push_back(name);
            }
        }
        std::set<std::string> desired_known;
        for (const auto& [n, _] : desired.tables) desired_known.insert(n);
        for (const auto& [n, _] : desired.views) desired_known.insert(n);
        for (const auto& [n, _] : desired.triggers) desired_known.insert(n);

        std::map<std::string, std::set<std::string>> deps;
        for (const auto& name : to_create) {
            deps[name] = extract_sql_references(
                desired.triggers.at(name).sql, name, desired_known);
        }
        auto sorted = topo_sort(to_create, deps, false);
        for (const auto& name : sorted) {
            plan.ops_.push_back({
                .type = OpType::CreateTrigger,
                .object_name = name,
                .description = "Create trigger " + name,
                .sql = {desired.triggers.at(name).sql},
                .destructive = false,
            });
        }
    }

    plan.warnings_ = detect_redundant_indexes(desired);

    return plan;
}

std::vector<Warning> detect_redundant_indexes(const Schema& schema) {
    std::vector<Warning> warnings;
    std::set<std::string> pk_flagged; // Indexes already flagged as PK-duplicate.

    // Group indexes by table.
    std::map<std::string, std::vector<const Index*>> by_table;
    for (const auto& [name, idx] : schema.indexes)
        by_table[idx.table_name].push_back(&idx);

    for (const auto& [table_name, table] : schema.tables) {
        // Build PK column list ordered by pk position.
        std::vector<std::pair<int, std::string>> pk_pairs;
        for (const auto& col : table.columns) {
            if (col.pk > 0)
                pk_pairs.push_back({col.pk, col.name});
        }
        std::sort(pk_pairs.begin(), pk_pairs.end());
        std::vector<std::string> pk_columns;
        for (const auto& [pos, name] : pk_pairs)
            pk_columns.push_back(name);

        auto it = by_table.find(table_name);
        if (it == by_table.end()) continue;
        const auto& indexes = it->second;

        // --- PK-duplicate detection ---
        if (!pk_columns.empty()) {
            for (const auto* idx : indexes) {
                // Partial indexes can't be PK-duplicates (PK has no WHERE).
                if (!idx->where_clause.empty()) continue;

                if (idx->columns.size() > pk_columns.size()) continue;

                // Check if idx->columns is a prefix of pk_columns.
                if (!std::equal(idx->columns.begin(), idx->columns.end(),
                                pk_columns.begin())) continue;

                bool exact_match = (idx->columns.size() == pk_columns.size());
                if (exact_match || !idx->unique) {
                    // Exact PK match: always redundant (PK implies uniqueness).
                    // Strict prefix + non-unique: redundant (PK index covers lookups).
                    // Strict prefix + unique: NOT redundant (tighter constraint).
                    pk_flagged.insert(idx->name);
                    warnings.push_back({
                        .type = WarningType::RedundantIndex,
                        .message = "Index '" + idx->name + "' on table '" +
                                   table_name + "' is redundant: columns are " +
                                   (exact_match ? "identical to" : "a prefix of") +
                                   " PRIMARY KEY",
                        .index_name = idx->name,
                        .covered_by = "PRIMARY KEY",
                        .table_name = table_name,
                    });
                }
            }
        }

        // --- Prefix-duplicate detection ---
        for (const auto* shorter : indexes) {
            if (pk_flagged.count(shorter->name)) continue;

            for (const auto* longer : indexes) {
                if (shorter == longer) continue;
                if (pk_flagged.count(longer->name)) continue;
                if (shorter->columns.size() >= longer->columns.size()) continue;
                if (shorter->where_clause != longer->where_clause) continue;

                // Check if shorter->columns is a strict prefix of longer->columns.
                if (!std::equal(shorter->columns.begin(), shorter->columns.end(),
                                longer->columns.begin())) continue;

                // Non-unique shorter: always redundant (longer covers lookups).
                // Unique shorter: enforces a tighter constraint, NOT redundant.
                if (!shorter->unique) {
                    warnings.push_back({
                        .type = WarningType::RedundantIndex,
                        .message = "Index '" + shorter->name + "' on table '" +
                                   table_name + "' is redundant: columns are a prefix of index '" +
                                   longer->name + "'",
                        .index_name = shorter->name,
                        .covered_by = longer->name,
                        .table_name = table_name,
                    });
                    break; // One warning per redundant index.
                }
            }
        }

        // --- Exact-duplicate detection (same columns, same WHERE) ---
        for (size_t i = 0; i < indexes.size(); ++i) {
            if (pk_flagged.count(indexes[i]->name)) continue;

            for (size_t j = i + 1; j < indexes.size(); ++j) {
                if (pk_flagged.count(indexes[j]->name)) continue;
                if (indexes[i]->columns != indexes[j]->columns) continue;
                if (indexes[i]->where_clause != indexes[j]->where_clause) continue;

                // Same columns, same WHERE. Determine which is redundant.
                const Index* redundant = nullptr;
                const Index* keeper = nullptr;

                if (indexes[i]->unique == indexes[j]->unique) {
                    // Same uniqueness: flag the later one alphabetically.
                    if (indexes[i]->name < indexes[j]->name) {
                        redundant = indexes[j];
                        keeper = indexes[i];
                    } else {
                        redundant = indexes[i];
                        keeper = indexes[j];
                    }
                } else if (!indexes[i]->unique) {
                    // i is non-unique, j is unique: i is redundant.
                    redundant = indexes[i];
                    keeper = indexes[j];
                } else {
                    // i is unique, j is non-unique: j is redundant.
                    redundant = indexes[j];
                    keeper = indexes[i];
                }

                // Skip if this index was already flagged as prefix-duplicate.
                bool already_warned = false;
                for (const auto& w : warnings) {
                    if (w.index_name == redundant->name) {
                        already_warned = true;
                        break;
                    }
                }
                if (already_warned) continue;

                warnings.push_back({
                    .type = WarningType::RedundantIndex,
                    .message = "Index '" + redundant->name + "' on table '" +
                               table_name + "' is redundant: duplicate of index '" +
                               keeper->name + "'",
                    .index_name = redundant->name,
                    .covered_by = keeper->name,
                    .table_name = table_name,
                });
            }
        }
    }

    // Sort warnings by (table_name, index_name) for deterministic output.
    std::sort(warnings.begin(), warnings.end(),
              [](const Warning& a, const Warning& b) {
                  if (a.table_name != b.table_name) return a.table_name < b.table_name;
                  return a.index_name < b.index_name;
              });

    return warnings;
}


// --- apply.cpp ---



namespace {

void ensure_state_table(sqlite3* db) {
    Statement stmt(db,
        "CREATE TABLE IF NOT EXISTS _sqlift_state ("
        "  key   TEXT PRIMARY KEY,"
        "  value TEXT NOT NULL"
        ")");
    stmt.step();
}

void store_schema_hash(sqlite3* db, const std::string& hash) {
    ensure_state_table(db);
    Statement stmt(db,
        "INSERT OR REPLACE INTO _sqlift_state (key, value) VALUES ('schema_hash', ?)");
    stmt.bind_text(1, hash);
    stmt.step();

    // Increment migration version counter
    Statement ver_stmt(db,
        "INSERT OR REPLACE INTO _sqlift_state (key, value) "
        "VALUES ('migration_version', COALESCE("
        "(SELECT CAST(value AS INTEGER) + 1 FROM _sqlift_state "
        "WHERE key='migration_version'), 1))");
    ver_stmt.step();
}

std::string load_schema_hash(sqlite3* db) {
    // Check if table exists first.
    Statement check(db,
        "SELECT name FROM sqlite_master WHERE type='table' AND name='_sqlift_state'");
    if (!check.step()) return {};

    Statement stmt(db,
        "SELECT value FROM _sqlift_state WHERE key='schema_hash'");
    if (stmt.step())
        return stmt.column_text(0);
    return {};
}

} // namespace

void apply(sqlite3* db, const MigrationPlan& plan, const ApplyOptions& opts) {
    if (plan.empty()) return;

    if (plan.has_destructive_operations() && !opts.allow_destructive) {
        throw DestructiveError(
            "Migration plan contains destructive operations. "
            "Set allow_destructive=true to proceed.");
    }

    // Check for drift
    Schema current = extract(db);
    std::string stored_hash = load_schema_hash(db);
    if (!stored_hash.empty()) {
        std::string actual_hash = current.hash();
        if (stored_hash != actual_hash) {
            throw DriftError(
                "Schema drift detected: the database schema has been modified "
                "outside of sqlift. Stored hash: " + stored_hash +
                ", actual hash: " + actual_hash);
        }
    }

    // Save current FK enforcement state so we can restore it on failure.
    bool fk_was_on = false;
    {
        Statement fk_query(db, "PRAGMA foreign_keys");
        fk_was_on = fk_query.step() && fk_query.column_int(0) != 0;
    }

    try {
        for (const auto& op : plan.operations()) {
            for (const auto& sql : op.sql) {
                // PRAGMA foreign_key_check returns rows if there are violations.
                // We need to handle this specially. The prefix match is safe
                // because this SQL is generated by rebuild_table_sql() — plans
                // from from_json() are trusted input (same as schema DDL).
                if (sql.find("PRAGMA foreign_key_check") == 0) {
                    Statement stmt(db, sql);
                    if (stmt.step()) {
                        std::string table = stmt.column_text(0);
                        int64_t rowid = stmt.column_int(1);
                        std::string parent = stmt.column_text(2);
                        throw ApplyError(
                            "Foreign key violation in table '" + table +
                            "' (rowid " + std::to_string(rowid) +
                            "): references missing row in parent table '" +
                            parent + "'");
                    }
                    continue;
                }

                Statement stmt(db, sql);
                stmt.step();
            }
        }
    } catch (...) {
        // Roll back any open savepoint from a failed rebuild, then restore FK state.
        // PRAGMA foreign_keys cannot be changed inside an open transaction/savepoint.
        try {
            Statement rb(db, "ROLLBACK TO SAVEPOINT sqlift_rebuild");
            rb.step();
            Statement rel(db, "RELEASE SAVEPOINT sqlift_rebuild");
            rel.step();
        } catch (...) {}
        // Restore FK enforcement to its state before apply() was called.
        try {
            Statement restore(db, fk_was_on ? "PRAGMA foreign_keys=ON"
                                            : "PRAGMA foreign_keys=OFF");
            restore.step();
        } catch (...) {}
        throw;
    }

    // Update stored hash
    Schema after = extract(db);
    store_schema_hash(db, after.hash());
}

int64_t migration_version(sqlite3* db) {
    Statement check(db,
        "SELECT name FROM sqlite_master WHERE type='table' AND name='_sqlift_state'");
    if (!check.step()) return 0;

    Statement stmt(db,
        "SELECT value FROM _sqlift_state WHERE key='migration_version'");
    if (stmt.step()) {
        try {
            return std::stoll(stmt.column_text(0));
        } catch (...) {
            return 0;
        }
    }
    return 0;
}


// --- json.cpp ---




namespace {

struct OpTypeEntry {
    OpType type;
    const char* name;
};

constexpr OpTypeEntry op_type_names[] = {
    {OpType::CreateTable,   "CreateTable"},
    {OpType::DropTable,     "DropTable"},
    {OpType::RebuildTable,  "RebuildTable"},
    {OpType::AddColumn,     "AddColumn"},
    {OpType::CreateIndex,   "CreateIndex"},
    {OpType::DropIndex,     "DropIndex"},
    {OpType::CreateView,    "CreateView"},
    {OpType::DropView,      "DropView"},
    {OpType::CreateTrigger, "CreateTrigger"},
    {OpType::DropTrigger,   "DropTrigger"},
};

} // namespace

std::string to_string(OpType type) {
    for (const auto& entry : op_type_names) {
        if (entry.type == type) return entry.name;
    }
    throw JsonError("Unknown OpType value: " +
                    std::to_string(static_cast<int>(type)));
}

OpType op_type_from_string(const std::string& s) {
    for (const auto& entry : op_type_names) {
        if (s == entry.name) return entry.type;
    }
    throw JsonError("Unknown OpType string: " + s);
}

std::string to_json(const MigrationPlan& plan) {
    nlohmann::json j;
    j["version"] = 1;

    auto& ops = j["operations"];
    ops = nlohmann::json::array();

    for (const auto& op : plan.operations()) {
        nlohmann::json jop;
        jop["type"] = to_string(op.type);
        jop["object_name"] = op.object_name;
        jop["description"] = op.description;
        jop["sql"] = op.sql;
        jop["destructive"] = op.destructive;
        ops.push_back(std::move(jop));
    }

    if (!plan.warnings().empty()) {
        auto& warns = j["warnings"];
        warns = nlohmann::json::array();
        for (const auto& w : plan.warnings()) {
            nlohmann::json jw;
            jw["type"] = "RedundantIndex";
            jw["message"] = w.message;
            jw["index_name"] = w.index_name;
            jw["covered_by"] = w.covered_by;
            jw["table_name"] = w.table_name;
            warns.push_back(std::move(jw));
        }
    }

    return j.dump(2);
}

MigrationPlan from_json(const std::string& json_str) {
    nlohmann::json j;
    try {
        j = nlohmann::json::parse(json_str);
    } catch (const nlohmann::json::parse_error& e) {
        throw JsonError(std::string("Invalid JSON: ") + e.what());
    }

    if (!j.is_object())
        throw JsonError("Expected top-level JSON object");

    if (!j.contains("version") || !j["version"].is_number_integer())
        throw JsonError("Missing or invalid 'version' field");
    int version = j["version"].get<int>();
    if (version != 1)
        throw JsonError("Unsupported version: " + std::to_string(version));

    if (!j.contains("operations") || !j["operations"].is_array())
        throw JsonError("Missing or invalid 'operations' array");

    MigrationPlan plan;
    for (const auto& jop : j["operations"]) {
        if (!jop.is_object())
            throw JsonError("Each operation must be a JSON object");

        Operation op;

        if (!jop.contains("type") || !jop["type"].is_string())
            throw JsonError("Operation missing 'type' string field");
        op.type = op_type_from_string(jop["type"].get<std::string>());

        if (!jop.contains("object_name") || !jop["object_name"].is_string())
            throw JsonError("Operation missing 'object_name' string field");
        op.object_name = jop["object_name"].get<std::string>();

        if (!jop.contains("description") || !jop["description"].is_string())
            throw JsonError("Operation missing 'description' string field");
        op.description = jop["description"].get<std::string>();

        if (!jop.contains("sql") || !jop["sql"].is_array())
            throw JsonError("Operation missing 'sql' array field");
        for (const auto& s : jop["sql"]) {
            if (!s.is_string())
                throw JsonError("'sql' array must contain only strings");
            op.sql.push_back(s.get<std::string>());
        }

        if (!jop.contains("destructive") || !jop["destructive"].is_boolean())
            throw JsonError("Operation missing 'destructive' boolean field");
        op.destructive = jop["destructive"].get<bool>();

        // Validate SQL prefix matches OpType
        if (!op.sql.empty()) {
            const std::string& first_sql = op.sql[0];
            std::string expected_prefix;
            switch (op.type) {
                case OpType::CreateTable:   expected_prefix = "CREATE TABLE"; break;
                case OpType::DropTable:     expected_prefix = "DROP TABLE"; break;
                case OpType::RebuildTable:  expected_prefix = "PRAGMA foreign_keys"; break;
                case OpType::AddColumn:     expected_prefix = "ALTER TABLE"; break;
                case OpType::CreateIndex:   expected_prefix = "CREATE"; break;
                case OpType::DropIndex:     expected_prefix = "DROP INDEX"; break;
                case OpType::CreateView:    expected_prefix = "CREATE VIEW"; break;
                case OpType::DropView:      expected_prefix = "DROP VIEW"; break;
                case OpType::CreateTrigger: expected_prefix = "CREATE TRIGGER"; break;
                case OpType::DropTrigger:   expected_prefix = "DROP TRIGGER"; break;
            }
            if (!starts_with(first_sql, expected_prefix)) {
                throw JsonError(
                    "Operation '" + to_string(op.type) + "' on '" +
                    op.object_name + "': first SQL statement does not start with '" +
                    expected_prefix + "'");
            }
        }

        plan.ops_.push_back(std::move(op));
    }

    // Warnings are optional (backward-compatible with older JSON).
    if (j.contains("warnings") && j["warnings"].is_array()) {
        for (const auto& jw : j["warnings"]) {
            if (!jw.is_object()) continue;
            Warning w;
            w.type = WarningType::RedundantIndex;
            w.message = jw.value("message", "");
            w.index_name = jw.value("index_name", "");
            w.covered_by = jw.value("covered_by", "");
            w.table_name = jw.value("table_name", "");
            plan.warnings_.push_back(std::move(w));
        }
    }

    return plan;
}


// --- schema_json.cpp ---




std::string schema_to_json(const Schema& schema) {
    nlohmann::json j;

    // Tables
    auto& jt = j["tables"];
    jt = nlohmann::json::object();
    for (const auto& [name, table] : schema.tables) {
        nlohmann::json jtbl;
        jtbl["name"] = table.name;

        auto& jcols = jtbl["columns"];
        jcols = nlohmann::json::array();
        for (const auto& col : table.columns) {
            nlohmann::json jcol;
            jcol["name"] = col.name;
            jcol["type"] = col.type;
            jcol["notnull"] = col.notnull;
            jcol["default_value"] = col.default_value;
            jcol["pk"] = col.pk;
            jcol["collation"] = col.collation;
            jcol["generated"] = static_cast<int>(col.generated);
            jcol["generated_expr"] = col.generated_expr;
            jcols.push_back(std::move(jcol));
        }

        auto& jfks = jtbl["foreign_keys"];
        jfks = nlohmann::json::array();
        for (const auto& fk : table.foreign_keys) {
            nlohmann::json jfk;
            jfk["constraint_name"] = fk.constraint_name;
            jfk["from_columns"] = fk.from_columns;
            jfk["to_table"] = fk.to_table;
            jfk["to_columns"] = fk.to_columns;
            jfk["on_update"] = fk.on_update;
            jfk["on_delete"] = fk.on_delete;
            jfks.push_back(std::move(jfk));
        }

        auto& jchks = jtbl["check_constraints"];
        jchks = nlohmann::json::array();
        for (const auto& chk : table.check_constraints) {
            nlohmann::json jchk;
            jchk["name"] = chk.name;
            jchk["expression"] = chk.expression;
            jchks.push_back(std::move(jchk));
        }

        jtbl["pk_constraint_name"] = table.pk_constraint_name;
        jtbl["without_rowid"] = table.without_rowid;
        jtbl["strict"] = table.strict;
        jtbl["raw_sql"] = table.raw_sql;

        jt[name] = std::move(jtbl);
    }

    // Indexes
    auto& ji = j["indexes"];
    ji = nlohmann::json::object();
    for (const auto& [name, idx] : schema.indexes) {
        nlohmann::json jidx;
        jidx["name"] = idx.name;
        jidx["table_name"] = idx.table_name;
        jidx["columns"] = idx.columns;
        jidx["unique"] = idx.unique;
        jidx["where_clause"] = idx.where_clause;
        jidx["raw_sql"] = idx.raw_sql;
        ji[name] = std::move(jidx);
    }

    // Views
    auto& jv = j["views"];
    jv = nlohmann::json::object();
    for (const auto& [name, view] : schema.views) {
        nlohmann::json jview;
        jview["name"] = view.name;
        jview["sql"] = view.sql;
        jv[name] = std::move(jview);
    }

    // Triggers
    auto& jtr = j["triggers"];
    jtr = nlohmann::json::object();
    for (const auto& [name, trig] : schema.triggers) {
        nlohmann::json jtrig;
        jtrig["name"] = trig.name;
        jtrig["table_name"] = trig.table_name;
        jtrig["sql"] = trig.sql;
        jtr[name] = std::move(jtrig);
    }

    return j.dump(2);
}

Schema schema_from_json(const std::string& json_str) {
    nlohmann::json j;
    try {
        j = nlohmann::json::parse(json_str);
    } catch (const nlohmann::json::parse_error& e) {
        throw JsonError(std::string("Invalid JSON: ") + e.what());
    }

    if (!j.is_object())
        throw JsonError("Expected top-level JSON object");

    Schema schema;

    // Tables
    if (j.contains("tables") && j["tables"].is_object()) {
        for (const auto& [name, jtbl] : j["tables"].items()) {
            Table table;
            table.name = jtbl.value("name", "");

            if (jtbl.contains("columns") && jtbl["columns"].is_array()) {
                for (const auto& jcol : jtbl["columns"]) {
                    Column col;
                    col.name = jcol.value("name", "");
                    col.type = jcol.value("type", "");
                    col.notnull = jcol.value("notnull", false);
                    col.default_value = jcol.value("default_value", "");
                    col.pk = jcol.value("pk", 0);
                    col.collation = jcol.value("collation", "");
                    col.generated = static_cast<GeneratedType>(jcol.value("generated", 0));
                    col.generated_expr = jcol.value("generated_expr", "");
                    table.columns.push_back(std::move(col));
                }
            }

            if (jtbl.contains("foreign_keys") && jtbl["foreign_keys"].is_array()) {
                for (const auto& jfk : jtbl["foreign_keys"]) {
                    ForeignKey fk;
                    fk.constraint_name = jfk.value("constraint_name", "");
                    fk.from_columns = jfk.value("from_columns", std::vector<std::string>{});
                    fk.to_table = jfk.value("to_table", "");
                    fk.to_columns = jfk.value("to_columns", std::vector<std::string>{});
                    fk.on_update = jfk.value("on_update", "NO ACTION");
                    fk.on_delete = jfk.value("on_delete", "NO ACTION");
                    table.foreign_keys.push_back(std::move(fk));
                }
            }

            if (jtbl.contains("check_constraints") && jtbl["check_constraints"].is_array()) {
                for (const auto& jchk : jtbl["check_constraints"]) {
                    CheckConstraint chk;
                    chk.name = jchk.value("name", "");
                    chk.expression = jchk.value("expression", "");
                    table.check_constraints.push_back(std::move(chk));
                }
            }

            table.pk_constraint_name = jtbl.value("pk_constraint_name", "");
            table.without_rowid = jtbl.value("without_rowid", false);
            table.strict = jtbl.value("strict", false);
            table.raw_sql = jtbl.value("raw_sql", "");

            schema.tables[name] = std::move(table);
        }
    }

    // Indexes
    if (j.contains("indexes") && j["indexes"].is_object()) {
        for (const auto& [name, jidx] : j["indexes"].items()) {
            Index idx;
            idx.name = jidx.value("name", "");
            idx.table_name = jidx.value("table_name", "");
            idx.columns = jidx.value("columns", std::vector<std::string>{});
            idx.unique = jidx.value("unique", false);
            idx.where_clause = jidx.value("where_clause", "");
            idx.raw_sql = jidx.value("raw_sql", "");
            schema.indexes[name] = std::move(idx);
        }
    }

    // Views
    if (j.contains("views") && j["views"].is_object()) {
        for (const auto& [name, jview] : j["views"].items()) {
            View view;
            view.name = jview.value("name", "");
            view.sql = jview.value("sql", "");
            schema.views[name] = std::move(view);
        }
    }

    // Triggers
    if (j.contains("triggers") && j["triggers"].is_object()) {
        for (const auto& [name, jtrig] : j["triggers"].items()) {
            Trigger trig;
            trig.name = jtrig.value("name", "");
            trig.table_name = jtrig.value("table_name", "");
            trig.sql = jtrig.value("sql", "");
            schema.triggers[name] = std::move(trig);
        }
    }

    return schema;
}


} // namespace sqlift


// --- C wrapper ---------------------------------------------------------------


namespace {

// Duplicate a std::string to a malloc'd C string (caller frees with sqlift_free).
char* dup_str(const std::string& s) {
    char* p = static_cast<char*>(std::malloc(s.size() + 1));
    if (p) std::memcpy(p, s.c_str(), s.size() + 1);
    return p;
}

// Set error output pointers. msg is malloc'd; caller frees with sqlift_free.
void set_error(int* err_type, char** err_msg, int type, const std::string& msg) {
    if (err_type) *err_type = type;
    if (err_msg)  *err_msg = dup_str(msg);
}

void clear_error(int* err_type, char** err_msg) {
    if (err_type) *err_type = SQLIFT_OK;
    if (err_msg)  *err_msg = nullptr;
}

// Map a C++ exception to the error type enum.
int classify_exception(const std::exception& e) {
    if (dynamic_cast<const sqlift::ParseError*>(&e))          return SQLIFT_PARSE_ERROR;
    if (dynamic_cast<const sqlift::ExtractError*>(&e))        return SQLIFT_EXTRACT_ERROR;
    if (dynamic_cast<const sqlift::DiffError*>(&e))           return SQLIFT_DIFF_ERROR;
    if (dynamic_cast<const sqlift::DriftError*>(&e))          return SQLIFT_DRIFT_ERROR;
    if (dynamic_cast<const sqlift::DestructiveError*>(&e))    return SQLIFT_DESTRUCTIVE_ERROR;
    if (dynamic_cast<const sqlift::BreakingChangeError*>(&e)) return SQLIFT_BREAKING_CHANGE_ERROR;
    if (dynamic_cast<const sqlift::JsonError*>(&e))           return SQLIFT_JSON_ERROR;
    if (dynamic_cast<const sqlift::ApplyError*>(&e))          return SQLIFT_APPLY_ERROR;
    if (dynamic_cast<const sqlift::Error*>(&e))               return SQLIFT_ERROR;
    return SQLIFT_ERROR;
}

// Warning JSON serialization (reused by sqlift_diff and sqlift_detect_redundant_indexes).
std::string warnings_to_json(const std::vector<sqlift::Warning>& warnings) {
    std::string s = "[";
    for (size_t i = 0; i < warnings.size(); ++i) {
        if (i > 0) s += ',';
        const auto& w = warnings[i];
        // Manual JSON to avoid pulling nlohmann into this TU via includes.
        // The values are simple strings, no escaping issues in practice.
        s += "{\"type\":\"RedundantIndex\"";
        s += ",\"message\":\"" + w.message + "\"";
        s += ",\"index_name\":\"" + w.index_name + "\"";
        s += ",\"covered_by\":\"" + w.covered_by + "\"";
        s += ",\"table_name\":\"" + w.table_name + "\"}";
    }
    s += "]";
    return s;
}

} // namespace

// --- opaque handle -----------------------------------------------------------

struct sqlift_db {
    sqlift::Database db;
    explicit sqlift_db(const std::string& path, int flags)
        : db(path, flags ? flags : (SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE)) {}
};

// --- C API -------------------------------------------------------------------

extern "C" {

sqlift_db* sqlift_db_open(const char* path, int flags,
                          int* err_type, char** err_msg) {
    clear_error(err_type, err_msg);
    try {
        return new sqlift_db(path, flags);
    } catch (const std::exception& e) {
        set_error(err_type, err_msg, classify_exception(e), e.what());
        return nullptr;
    }
}

void sqlift_db_close(sqlift_db* db) {
    delete db;
}

int sqlift_db_exec(sqlift_db* db, const char* sql, char** err_msg) {
    if (err_msg) *err_msg = nullptr;
    try {
        db->db.exec(sql);
        return 0;
    } catch (const std::exception& e) {
        if (err_msg) *err_msg = dup_str(e.what());
        return 1;
    }
}

char* sqlift_parse(const char* ddl, int* err_type, char** err_msg) {
    clear_error(err_type, err_msg);
    try {
        auto schema = sqlift::parse(ddl);
        return dup_str(sqlift::schema_to_json(schema));
    } catch (const std::exception& e) {
        set_error(err_type, err_msg, classify_exception(e), e.what());
        return nullptr;
    }
}

char* sqlift_extract(sqlift_db* db, int* err_type, char** err_msg) {
    clear_error(err_type, err_msg);
    try {
        auto schema = sqlift::extract(db->db);
        return dup_str(sqlift::schema_to_json(schema));
    } catch (const std::exception& e) {
        set_error(err_type, err_msg, classify_exception(e), e.what());
        return nullptr;
    }
}

char* sqlift_diff(const char* current_json, const char* desired_json,
                  int* err_type, char** err_msg) {
    clear_error(err_type, err_msg);
    try {
        auto current = sqlift::schema_from_json(current_json);
        auto desired = sqlift::schema_from_json(desired_json);
        auto plan = sqlift::diff(current, desired);
        // Include warnings in the plan JSON (they're part of to_json output).
        return dup_str(sqlift::to_json(plan));
    } catch (const std::exception& e) {
        set_error(err_type, err_msg, classify_exception(e), e.what());
        return nullptr;
    }
}

int sqlift_apply(sqlift_db* db, const char* plan_json, int allow_destructive,
                 int* err_type, char** err_msg) {
    clear_error(err_type, err_msg);
    try {
        auto plan = sqlift::from_json(plan_json);
        sqlift::ApplyOptions opts;
        opts.allow_destructive = (allow_destructive != 0);
        sqlift::apply(db->db, plan, opts);
        return 0;
    } catch (const std::exception& e) {
        set_error(err_type, err_msg, classify_exception(e), e.what());
        return 1;
    }
}

int64_t sqlift_migration_version(sqlift_db* db, int* err_type, char** err_msg) {
    clear_error(err_type, err_msg);
    try {
        return sqlift::migration_version(db->db);
    } catch (const std::exception& e) {
        set_error(err_type, err_msg, classify_exception(e), e.what());
        return -1;
    }
}

char* sqlift_detect_redundant_indexes(const char* schema_json,
                                      int* err_type, char** err_msg) {
    clear_error(err_type, err_msg);
    try {
        auto schema = sqlift::schema_from_json(schema_json);
        auto warnings = sqlift::detect_redundant_indexes(schema);
        return dup_str(warnings_to_json(warnings));
    } catch (const std::exception& e) {
        set_error(err_type, err_msg, classify_exception(e), e.what());
        return nullptr;
    }
}

char* sqlift_schema_hash(const char* schema_json,
                         int* err_type, char** err_msg) {
    clear_error(err_type, err_msg);
    try {
        auto schema = sqlift::schema_from_json(schema_json);
        return dup_str(schema.hash());
    } catch (const std::exception& e) {
        set_error(err_type, err_msg, classify_exception(e), e.what());
        return nullptr;
    }
}

int sqlift_db_query_int64(sqlift_db* db, const char* sql,
                          int64_t* result, char** err_msg) {
    if (err_msg) *err_msg = nullptr;
    try {
        sqlite3_stmt* stmt = nullptr;
        int rc = sqlite3_prepare_v2(db->db.get(), sql, -1, &stmt, nullptr);
        if (rc != SQLITE_OK) {
            if (err_msg) *err_msg = dup_str(sqlite3_errmsg(db->db.get()));
            return 1;
        }
        rc = sqlite3_step(stmt);
        if (rc == SQLITE_ROW) {
            if (result) *result = sqlite3_column_int64(stmt, 0);
            sqlite3_finalize(stmt);
            return 0;
        }
        sqlite3_finalize(stmt);
        if (rc == SQLITE_DONE) {
            // No rows -- return 0 as default.
            if (result) *result = 0;
            return 0;
        }
        if (err_msg) *err_msg = dup_str(sqlite3_errmsg(db->db.get()));
        return 1;
    } catch (const std::exception& e) {
        if (err_msg) *err_msg = dup_str(e.what());
        return 1;
    }
}

char* sqlift_db_query_text(sqlift_db* db, const char* sql, char** err_msg) {
    if (err_msg) *err_msg = nullptr;
    try {
        sqlite3_stmt* stmt = nullptr;
        int rc = sqlite3_prepare_v2(db->db.get(), sql, -1, &stmt, nullptr);
        if (rc != SQLITE_OK) {
            if (err_msg) *err_msg = dup_str(sqlite3_errmsg(db->db.get()));
            return nullptr;
        }
        rc = sqlite3_step(stmt);
        if (rc == SQLITE_ROW) {
            const char* text = reinterpret_cast<const char*>(sqlite3_column_text(stmt, 0));
            char* result = dup_str(text ? text : "");
            sqlite3_finalize(stmt);
            return result;
        }
        sqlite3_finalize(stmt);
        if (rc == SQLITE_DONE) {
            // No rows -- return empty string.
            return dup_str("");
        }
        if (err_msg) *err_msg = dup_str(sqlite3_errmsg(db->db.get()));
        return nullptr;
    } catch (const std::exception& e) {
        if (err_msg) *err_msg = dup_str(e.what());
        return nullptr;
    }
}

void sqlift_free(void* ptr) {
    std::free(ptr);
}

} // extern "C"
