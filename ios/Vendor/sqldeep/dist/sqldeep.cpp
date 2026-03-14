// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
#include "sqldeep.h"

#include <cstdlib>
#include <cstring>
#include <map>
#include <memory>
#include <stdexcept>
#include <string>
#include <unordered_map>
#include <variant>
#include <vector>

namespace sqldeep {

// ── Internal C++ types (not exposed in public C header) ─────────────

struct ForeignKey {
    std::string from_table;   // child table (has the FK column(s))
    std::string to_table;     // parent/referenced table

    struct ColumnPair {
        std::string from_column;  // FK column in child
        std::string to_column;    // referenced column in parent
    };
    std::vector<ColumnPair> columns;  // supports multi-column FKs
};

enum class Backend { sqlite, postgres };

class Error : public std::runtime_error {
public:
    Error(const std::string& msg, int line, int col)
        : std::runtime_error(msg), line_(line), col_(col) {}
    int line() const { return line_; }
    int col() const { return col_; }
private:
    int line_;
    int col_;
};

namespace {

static constexpr int kMaxNestingDepth = 200;

// ── Lexer ───────────────────────────────────────────────────────────

enum class TokenType {
    Ident,       // unquoted identifier or keyword
    DqString,    // double-quoted string "..."
    SqString,    // single-quoted string '...'
    Number,      // numeric literal
    LBrace,      // {
    RBrace,      // }
    LBracket,    // [
    RBracket,    // ]
    LParen,      // (
    RParen,      // )
    Comma,       // ,
    Colon,       // :
    Semicolon,   // ;
    Other,       // any other character or operator
    Eof,
};

struct Token {
    TokenType type;
    std::string text;
    int line;
    int col;
    size_t src_begin; // offset in source where token text starts
    size_t src_end;   // offset right after token text ends
};

struct LexerState {
    size_t pos;
    int line;
    int col;
};

class Lexer {
public:
    explicit Lexer(const std::string& input)
        : src_(input), pos_(0), line_(1), col_(1) {}

    Token next() {
        skip_whitespace_and_comments();
        if (pos_ >= src_.size())
            return {TokenType::Eof, "", line_, col_, pos_, pos_};

        int tline = line_, tcol = col_;
        size_t begin = pos_;
        char c = src_[pos_];

        switch (c) {
        case '{': advance(); return {TokenType::LBrace,   "{", tline, tcol, begin, pos_};
        case '}': advance(); return {TokenType::RBrace,   "}", tline, tcol, begin, pos_};
        case '[': advance(); return {TokenType::LBracket, "[", tline, tcol, begin, pos_};
        case ']': advance(); return {TokenType::RBracket, "]", tline, tcol, begin, pos_};
        case '(': advance(); return {TokenType::LParen,   "(", tline, tcol, begin, pos_};
        case ')': advance(); return {TokenType::RParen,   ")", tline, tcol, begin, pos_};
        case ',': advance(); return {TokenType::Comma,    ",", tline, tcol, begin, pos_};
        case ':': advance(); return {TokenType::Colon,    ":", tline, tcol, begin, pos_};
        case ';': advance(); return {TokenType::Semicolon,";", tline, tcol, begin, pos_};
        case '\'': return lex_string('\'', TokenType::SqString, tline, tcol, begin);
        case '"':  return lex_string('"',  TokenType::DqString,  tline, tcol, begin);
        default: break;
        }

        if (is_ident_start(c)) return lex_ident(tline, tcol, begin);
        if (is_digit(c) || (c == '.' && pos_ + 1 < src_.size() && is_digit(src_[pos_ + 1])))
            return lex_number(tline, tcol, begin);

        // Operator or other character
        std::string s(1, c);
        advance();
        if (pos_ < src_.size()) {
            char n = src_[pos_];
            if ((c == '<' && (n == '=' || n == '>' || n == '-')) ||
                (c == '>' && n == '=') ||
                (c == '!' && n == '=') ||
                (c == '|' && n == '|') ||
                (c == '<' && n == '<') ||
                (c == '>' && n == '>') ||
                (c == '-' && n == '>')) {
                s += n;
                advance();
                // Extend -> to ->> when the > is touching (SQL JSON operator)
                if (s == "->" && pos_ < src_.size() && src_[pos_] == '>') {
                    s += '>';
                    advance();
                }
            }
        }
        return {TokenType::Other, s, tline, tcol, begin, pos_};
    }

    Token peek() {
        auto st = save();
        Token t = next();
        restore(st);
        return t;
    }

    LexerState save() const { return {pos_, line_, col_}; }
    void restore(const LexerState& st) { pos_ = st.pos; line_ = st.line; col_ = st.col; }

    // Current position in source (right after last consumed token).
    size_t offset() const { return pos_; }

    const std::string& source() const { return src_; }

    [[noreturn]] void error(const std::string& msg) {
        throw Error(msg, line_, col_);
    }

    [[noreturn]] void error(const std::string& msg, int line, int col) {
        throw Error(msg, line, col);
    }

private:
    void advance() {
        if (pos_ < src_.size()) {
            if (src_[pos_] == '\n') { ++line_; col_ = 1; }
            else { ++col_; }
            ++pos_;
        }
    }

    void skip_whitespace_and_comments() {
        while (pos_ < src_.size()) {
            if (std::isspace(static_cast<unsigned char>(src_[pos_]))) {
                advance();
            } else if (pos_ + 1 < src_.size() && src_[pos_] == '-' && src_[pos_ + 1] == '-') {
                // SQL line comment: -- to end of line
                advance(); advance();
                while (pos_ < src_.size() && src_[pos_] != '\n') advance();
            } else if (pos_ + 1 < src_.size() && src_[pos_] == '/' && src_[pos_ + 1] == '*') {
                // SQL block comment: /* ... */ (flat, not nested)
                int cline = line_, ccol = col_;
                advance(); advance();
                while (pos_ < src_.size()) {
                    if (src_[pos_] == '*' && pos_ + 1 < src_.size() && src_[pos_ + 1] == '/') {
                        advance(); advance();
                        break;
                    }
                    advance();
                }
                if (pos_ >= src_.size() && (pos_ < 2 || src_[pos_ - 2] != '*' || src_[pos_ - 1] != '/'))
                    error("unterminated block comment", cline, ccol);
            } else {
                break;
            }
        }
    }

    Token lex_string(char quote, TokenType type, int tline, int tcol, size_t begin) {
        std::string s(1, quote);
        advance(); // skip opening quote
        while (pos_ < src_.size()) {
            if (src_[pos_] == quote) {
                // SQL doubled-quote escape: '' inside '...' or "" inside "..."
                if (pos_ + 1 < src_.size() && src_[pos_ + 1] == quote) {
                    s += quote; advance();
                    s += quote; advance();
                    continue;
                }
                break; // end of string
            }
            if (src_[pos_] == '\\' && pos_ + 1 < src_.size()) {
                s += src_[pos_]; advance();
                s += src_[pos_]; advance();
            } else {
                s += src_[pos_]; advance();
            }
        }
        if (pos_ >= src_.size()) error("unterminated string literal", tline, tcol);
        s += quote;
        advance(); // skip closing quote
        return {type, s, tline, tcol, begin, pos_};
    }

    Token lex_ident(int tline, int tcol, size_t begin) {
        std::string s;
        while (pos_ < src_.size() && is_ident_cont(src_[pos_])) {
            s += src_[pos_]; advance();
        }
        return {TokenType::Ident, s, tline, tcol, begin, pos_};
    }

    Token lex_number(int tline, int tcol, size_t begin) {
        std::string s;
        while (pos_ < src_.size() && is_digit(src_[pos_])) {
            s += src_[pos_]; advance();
        }
        if (pos_ < src_.size() && src_[pos_] == '.' &&
            pos_ + 1 < src_.size() && is_digit(src_[pos_ + 1])) {
            s += src_[pos_]; advance(); // '.'
            while (pos_ < src_.size() && is_digit(src_[pos_])) {
                s += src_[pos_]; advance();
            }
        }
        if (pos_ < src_.size() && (src_[pos_] == 'e' || src_[pos_] == 'E')) {
            s += src_[pos_]; advance();
            if (pos_ < src_.size() && (src_[pos_] == '+' || src_[pos_] == '-')) {
                s += src_[pos_]; advance();
            }
            while (pos_ < src_.size() && is_digit(src_[pos_])) {
                s += src_[pos_]; advance();
            }
        }
        return {TokenType::Number, s, tline, tcol, begin, pos_};
    }

    static bool is_ident_start(char c) {
        return std::isalpha(static_cast<unsigned char>(c)) || c == '_';
    }
    static bool is_ident_cont(char c) {
        return std::isalnum(static_cast<unsigned char>(c)) || c == '_';
    }
    static bool is_digit(char c) {
        return std::isdigit(static_cast<unsigned char>(c));
    }

    const std::string& src_;
    size_t pos_;
    int line_;
    int col_;
};

// ── AST ─────────────────────────────────────────────────────────────

struct DeepSelect;
struct ObjectLiteral;
struct ArrayLiteral;
struct JoinPath;
struct RecursiveSelect;

using SqlPart = std::variant<
    std::string,
    std::unique_ptr<DeepSelect>,
    std::unique_ptr<ObjectLiteral>,
    std::unique_ptr<ArrayLiteral>,
    std::unique_ptr<JoinPath>,
    std::unique_ptr<RecursiveSelect>
>;
using SqlParts = std::vector<SqlPart>;

struct ObjectLiteral {
    struct Field {
        std::string key;
        SqlParts computed_key; // non-empty = (expr) computed key
        SqlParts value; // empty = bare field
        bool aggregate = false; // SELECT expr (no FROM) → json_group_array(expr)
        bool recursive = false; // * = recurse with same shape
    };
    std::vector<Field> fields;
};

struct ArrayLiteral {
    std::vector<SqlParts> elements;
};

struct JoinPath {
    struct Step {
        bool forward;       // true = ->, false = <-
        std::string table;
        std::string alias;  // empty if none
        // Explicit column pairs: {child_col, parent_col}.
        // Empty = use convention/FK resolution.
        std::vector<std::pair<std::string, std::string>> columns;
    };
    std::string start_alias;  // e.g. "c"
    std::string start_table;  // e.g. "customers" (resolved from alias_map)
    std::vector<Step> steps;
};

struct DeepSelect {
    std::variant<ObjectLiteral, ArrayLiteral, SqlParts> projection;
    SqlParts tail;
    bool singular = false; // SELECT/1: no json_group_array, add LIMIT 1
};

struct RecursiveSelect {
    std::vector<ObjectLiteral::Field> fields; // non-recursive fields
    std::string children_field;               // name of recursive field
    std::string table;                        // table to recurse on
    std::string fk_column;                    // self-referential FK column
    std::string pk_column;                    // PK column (default: "id")
    SqlParts root_condition;                  // WHERE condition (without WHERE keyword)
    bool singular = false;                    // SELECT/1: single root
};

// ── Parser ──────────────────────────────────────────────────────────

static bool is_keyword(const Token& t, const char* kw) {
    if (t.type != TokenType::Ident) return false;
    const auto& s = t.text;
    size_t len = std::strlen(kw);
    if (s.size() != len) return false;
    for (size_t i = 0; i < len; ++i) {
        if (std::toupper(static_cast<unsigned char>(s[i])) !=
            std::toupper(static_cast<unsigned char>(kw[i])))
            return false;
    }
    return true;
}

static bool is_sql_keyword(const std::string& s) {
    static const char* keywords[] = {
        "SELECT", "FROM", "WHERE", "JOIN", "INNER", "LEFT", "RIGHT",
        "OUTER", "CROSS", "NATURAL", "ON", "ORDER", "GROUP", "HAVING",
        "LIMIT", "UNION", "INTERSECT", "EXCEPT", "AS", "AND", "OR",
        "NOT", "IN", "IS", "NULL", "LIKE", "BETWEEN", "EXISTS",
        "CASE", "WHEN", "THEN", "ELSE", "END", "SET", "INTO",
        "VALUES", "INSERT", "UPDATE", "DELETE", "DISTINCT", "ALL",
        "ASC", "DESC", "BY", "OFFSET", "FETCH", "FOR", "WITH", "USING",
        "RECURSE",
    };
    for (const char* kw : keywords) {
        if (is_keyword({TokenType::Ident, s, 0, 0, 0, 0}, kw))
            return true;
    }
    return false;
}

static bool is_from_or_join(const Token& t) {
    return is_keyword(t, "FROM") || is_keyword(t, "JOIN");
}

// Parse ON/USING clause after a join path step.
// If out is non-null, stores {child_col, parent_col} pairs.
// If out is null (skip mode for prescan), just advances past the tokens.
static void parse_on_using(Lexer& lex, bool forward,
                           std::vector<std::pair<std::string,std::string>>* out) {
    Token t = lex.peek();

    if (is_keyword(t, "ON")) {
        lex.next(); // consume ON
        Token first = lex.peek();
        if (first.type != TokenType::Ident)
            lex.error("expected column name after ON", first.line, first.col);
        lex.next(); // consume first ident

        Token eq = lex.peek();
        if (eq.type == TokenType::Other && eq.text == "=") {
            // Explicit pair mode: left_col = right_col
            lex.next(); // consume =
            Token second = lex.peek();
            if (second.type != TokenType::Ident)
                lex.error("expected column name after '='",
                          second.line, second.col);
            lex.next(); // consume second ident

            if (out) {
                if (forward) {
                    // child = right of arrow: {right_col, left_col}
                    out->push_back({second.text, first.text});
                } else {
                    // child = left of arrow: {left_col, right_col}
                    out->push_back({first.text, second.text});
                }
            }

            // Loop: AND ident = ident (save/restore to avoid consuming
            // outer SQL's AND when pattern doesn't match).
            while (true) {
                auto st = lex.save();
                Token and_tok = lex.peek();
                if (!is_keyword(and_tok, "AND")) break;
                lex.next(); // tentatively consume AND
                Token col1 = lex.peek();
                if (col1.type != TokenType::Ident) { lex.restore(st); break; }
                lex.next();
                Token eq2 = lex.peek();
                if (eq2.type != TokenType::Other || eq2.text != "=") {
                    lex.restore(st); break;
                }
                lex.next();
                Token col2 = lex.peek();
                if (col2.type != TokenType::Ident) { lex.restore(st); break; }
                lex.next();

                if (out) {
                    if (forward) {
                        out->push_back({col2.text, col1.text});
                    } else {
                        out->push_back({col1.text, col2.text});
                    }
                }
            }
        } else {
            // Shorthand mode: same column name in both tables
            if (out) {
                out->push_back({first.text, first.text});
            }
        }
    } else if (is_keyword(t, "USING")) {
        lex.next(); // consume USING
        Token lparen = lex.peek();
        if (lparen.type != TokenType::LParen)
            lex.error("expected '(' after USING", lparen.line, lparen.col);
        lex.next(); // consume (

        Token check = lex.peek();
        if (check.type == TokenType::RParen)
            lex.error("empty USING clause", check.line, check.col);

        while (true) {
            Token col = lex.peek();
            if (col.type != TokenType::Ident)
                lex.error("expected column name in USING clause",
                          col.line, col.col);
            lex.next();

            if (out) {
                out->push_back({col.text, col.text});
            }

            Token next = lex.peek();
            if (next.type == TokenType::Comma) {
                lex.next();
            } else if (next.type == TokenType::RParen) {
                lex.next();
                break;
            } else {
                lex.error("expected ',' or ')' in USING clause",
                          next.line, next.col);
            }
        }
    }
}

// Pre-scan input to build alias → table name map.
static std::unordered_map<std::string, std::string>
build_alias_map(const std::string& input) {
    std::unordered_map<std::string, std::string> map;
    Lexer lex(input);
    int paren_depth = 0;

    while (true) {
        Token t = lex.next();
        if (t.type == TokenType::Eof) break;

        if (t.type == TokenType::LParen) { ++paren_depth; continue; }
        if (t.type == TokenType::RParen) {
            if (paren_depth > 0) --paren_depth;
            continue;
        }

        // Only look for aliases at paren depth 0.
        if (paren_depth > 0) continue;

        if (!is_from_or_join(t)) continue;

        // After FROM/JOIN, expect table name or alias->child pattern.
        Token first = lex.peek();
        if (first.type != TokenType::Ident) continue;
        lex.next(); // consume first ident

        Token second = lex.peek();

        // Pattern: ident (-> | <-) table [alias] [(-> | <-) table [alias] ...]
        if (second.type == TokenType::Other &&
            (second.text == "->" || second.text == "<-")) {
            while (true) {
                Token arrow = lex.peek();
                if (arrow.type != TokenType::Other ||
                    (arrow.text != "->" && arrow.text != "<-"))
                    break;
                lex.next(); // consume arrow
                Token table = lex.peek();
                if (table.type != TokenType::Ident) break;
                lex.next(); // consume table
                Token alias = lex.peek();
                if (alias.type == TokenType::Ident && !is_sql_keyword(alias.text)) {
                    lex.next();
                    map[alias.text] = table.text;
                }
                parse_on_using(lex, true, nullptr); // skip past ON/USING
            }
            continue;
        }

        // Pattern: ident AS ident
        if (is_keyword(second, "AS")) {
            lex.next(); // consume AS
            Token alias = lex.peek();
            if (alias.type == TokenType::Ident) {
                lex.next();
                map[alias.text] = first.text;
            }
            continue;
        }

        // Pattern: ident ident (table alias)
        if (second.type == TokenType::Ident && !is_sql_keyword(second.text)) {
            lex.next();
            map[second.text] = first.text;
            continue;
        }
    }

    return map;
}

class Parser {
public:
    Parser(Lexer& lex, std::unordered_map<std::string, std::string> alias_map,
           Backend backend = Backend::sqlite)
        : lex_(lex), alias_map_(std::move(alias_map)), backend_(backend) {}

    SqlParts parse_document() {
        return parse_sql_parts(/*stop_comma=*/false,
                               /*stop_rbrace=*/false,
                               /*stop_rbracket=*/false,
                               /*stop_rparen=*/false,
                               /*depth=*/0);
    }

private:
    void check_depth(int depth, int line, int col) {
        if (depth > kMaxNestingDepth)
            lex_.error("maximum nesting depth exceeded", line, col);
    }

    // Try to consume /1 after SELECT. Returns true if consumed.
    bool try_consume_singular() {
        auto st = lex_.save();
        Token slash = lex_.peek();
        if (slash.type == TokenType::Other && slash.text == "/") {
            lex_.next();
            Token one = lex_.peek();
            if (one.type == TokenType::Number && one.text == "1") {
                lex_.next();
                return true;
            }
        }
        lex_.restore(st);
        return false;
    }

    // Lookahead: is the current position the start of a FROM-first deep
    // select?  Scans forward (tracking nesting depth) looking for
    // SELECT {/[ at depth 0.  Restores lexer state before returning.
    bool is_from_first(bool stop_comma, bool stop_rbrace,
                       bool stop_rbracket, bool stop_rparen) {
        auto st = lex_.save();
        int pd = 0, bd = 0, bkd = 0;
        while (true) {
            Token t = lex_.next();
            if (t.type == TokenType::Eof) break;

            if (pd == 0 && bd == 0 && bkd == 0) {
                if (stop_comma && t.type == TokenType::Comma) break;
                if (stop_rbrace && t.type == TokenType::RBrace) break;
                if (stop_rbracket && t.type == TokenType::RBracket) break;
                if (stop_rparen && t.type == TokenType::RParen) break;
                if (t.type == TokenType::Semicolon) break;

                if (is_keyword(t, "SELECT")) {
                    lex_.restore(st);
                    return true;
                }
            }

            if (t.type == TokenType::LParen) ++pd;
            if (t.type == TokenType::RParen && pd > 0) --pd;
            if (t.type == TokenType::LBrace) ++bd;
            if (t.type == TokenType::RBrace && bd > 0) --bd;
            if (t.type == TokenType::LBracket) ++bkd;
            if (t.type == TokenType::RBracket && bkd > 0) --bkd;
        }
        lex_.restore(st);
        return false;
    }

    // Parse FROM-first select: FROM ... SELECT ...
    // Current position is before FROM.
    std::unique_ptr<DeepSelect> parse_from_first_select(
            bool stop_comma, bool stop_rbrace,
            bool stop_rbracket, bool stop_rparen,
            int depth) {
        Token from_tok = lex_.peek();
        check_depth(depth, from_tok.line, from_tok.col);

        // Parse body (FROM ... WHERE ... etc.) until SELECT
        auto body = parse_sql_parts(stop_comma, stop_rbrace,
                                    stop_rbracket, stop_rparen,
                                    depth, /*stop_at_select=*/true);

        // Consume SELECT [/1]
        Token select_tok = lex_.next();
        if (!is_keyword(select_tok, "SELECT"))
            lex_.error("expected SELECT after FROM clause",
                       select_tok.line, select_tok.col);
        bool singular = try_consume_singular();

        // Parse projection
        auto ds = std::make_unique<DeepSelect>();
        ds->singular = singular;
        Token t = lex_.peek();
        if (t.type == TokenType::LBrace) {
            ds->projection = std::move(*parse_object_literal(depth));
        } else if (t.type == TokenType::LBracket) {
            ds->projection = std::move(*parse_array_literal(depth));
        } else {
            // Plain SELECT — just rearrange, no JSON wrapping
            ds->projection = parse_sql_parts(stop_comma, stop_rbrace,
                                             stop_rbracket, stop_rparen,
                                             depth);
        }

        ds->tail = std::move(body);
        return ds;
    }

    // Parse a sequence of SQL fragments interleaved with deep constructs.
    SqlParts parse_sql_parts(bool stop_comma,
                             bool stop_rbrace,
                             bool stop_rbracket,
                             bool stop_rparen,
                             int depth,
                             bool stop_at_select = false) {
        SqlParts parts;
        std::string accum;
        size_t last_end = 0; // src position after last consumed raw token
        bool has_raw = false;
        std::vector<size_t> accum_paren_starts; // stack of '(' positions in accum

        auto flush = [&]() {
            if (!accum.empty()) {
                parts.push_back(std::move(accum));
                accum.clear();
            }
            has_raw = false;
        };

        // Flush accumulated raw SQL, preserving spacing before the
        // deep construct whose first source token is next_tok.
        auto flush_before = [&](const Token& next_tok) {
            if (has_raw && last_end < next_tok.src_begin)
                accum += " ";
            flush();
        };

        bool need_space = false; // space needed after a non-string AST part
        bool in_from_context = false; // true after FROM/JOIN at depth 0

        auto accum_token = [&](const Token& tok) {
            if (has_raw) {
                // Add space only if there was whitespace/comments in source
                if (last_end < tok.src_begin) accum += " ";
            } else if (need_space && last_end < tok.src_begin) {
                accum += " ";
            }
            accum += tok.text;
            last_end = tok.src_end;
            has_raw = true;
            need_space = false;
        };

        int paren_depth = 0;

        while (true) {
            Token t = lex_.peek();

            if (t.type == TokenType::Eof) break;

            // Stop conditions at paren depth 0
            if (paren_depth == 0) {
                if (stop_comma && t.type == TokenType::Comma) break;
                if (stop_rbrace && t.type == TokenType::RBrace) break;
                if (stop_rbracket && t.type == TokenType::RBracket) break;
                if (stop_rparen && t.type == TokenType::RParen) break;
            }

            // Semicolons at depth 0 pass through at top level, stop otherwise
            if (t.type == TokenType::Semicolon && paren_depth == 0) {
                if (!stop_comma && !stop_rbrace && !stop_rbracket && !stop_rparen) {
                    Token tok = lex_.next();
                    accum_token(tok);
                    continue;
                }
                break;
            }

            // Check for (SELECT {/[) pattern — subquery with deep construct
            if (t.type == TokenType::LParen && paren_depth == 0) {
                auto st = lex_.save();
                lex_.next(); // consume (
                Token t2 = lex_.peek();
                if (is_keyword(t2, "SELECT")) {
                    lex_.next(); // consume SELECT
                    bool singular = try_consume_singular();
                    Token t3 = lex_.peek();
                    if (t3.type == TokenType::LBrace || t3.type == TokenType::LBracket) {
                        // Found (SELECT[/1] {/[)
                        flush_before(t);
                        auto part = parse_deep_or_recursive_select(
                            t2, singular,
                            /*stop_comma=*/false, /*stop_rbrace=*/false,
                            /*stop_rbracket=*/false, /*stop_rparen=*/true,
                            depth + 1);
                        Token rp = lex_.next(); // consume )
                        if (rp.type != TokenType::RParen)
                            lex_.error("expected ')' after subquery", rp.line, rp.col);
                        parts.push_back(std::move(part));
                        last_end = rp.src_end;
                        need_space = true;
                        continue;
                    }
                }
                // Not (SELECT {/[) — try (FROM ... SELECT ...)
                lex_.restore(st);
                lex_.next(); // re-consume (
                t2 = lex_.peek();
                if (is_keyword(t2, "FROM") &&
                    is_from_first(false, false, false, /*stop_rparen=*/true)) {
                    flush_before(t);
                    auto ds = parse_from_first_select(
                        /*stop_comma=*/false, /*stop_rbrace=*/false,
                        /*stop_rbracket=*/false, /*stop_rparen=*/true,
                        depth + 1);
                    Token rp = lex_.next(); // consume )
                    if (rp.type != TokenType::RParen)
                        lex_.error("expected ')' after subquery",
                                   rp.line, rp.col);
                    // Plain projection: inline with explicit parens
                    // (deep projections use DeepSelect whose renderer
                    // adds parens when nested)
                    if (std::holds_alternative<SqlParts>(ds->projection)) {
                        parts.push_back(std::string("(SELECT "));
                        for (auto& p : std::get<SqlParts>(ds->projection))
                            parts.push_back(std::move(p));
                        if (!ds->tail.empty()) {
                            parts.push_back(std::string(" "));
                            for (auto& p : ds->tail)
                                parts.push_back(std::move(p));
                        }
                        parts.push_back(std::string(")"));
                    } else {
                        parts.push_back(std::move(ds));
                    }
                    last_end = rp.src_end;
                    need_space = true;
                    continue;
                }

                // Not a deep subquery pattern, restore to before (
                lex_.restore(st);
            }

            // Check for SELECT[/1] {/[ at depth 0 (top-level deep select)
            if (is_keyword(t, "SELECT") && paren_depth == 0 &&
                !stop_at_select) {
                auto st = lex_.save();
                lex_.next(); // consume SELECT
                bool singular = try_consume_singular();
                Token t2 = lex_.peek();
                if (t2.type == TokenType::LBrace || t2.type == TokenType::LBracket) {
                    flush_before(t);
                    Token sel = {TokenType::Ident, "SELECT", t.line, t.col,
                                 t.src_begin, t.src_end};
                    auto part = parse_deep_or_recursive_select(
                                                sel, singular,
                                                stop_comma, stop_rbrace,
                                                stop_rbracket, stop_rparen,
                                                depth + 1);
                    parts.push_back(std::move(part));
                    last_end = lex_.offset();
                    need_space = true;
                    continue;
                }
                // Not deep — restore and accumulate SELECT as raw SQL
                lex_.restore(st);
            }

            // stop_at_select: break when SELECT at depth 0
            if (stop_at_select && is_keyword(t, "SELECT") &&
                paren_depth == 0) {
                break;
            }

            // Check for FROM-first: FROM ... SELECT {/[
            if (is_keyword(t, "FROM") && paren_depth == 0 &&
                !stop_at_select) {
                if (is_from_first(stop_comma, stop_rbrace,
                                  stop_rbracket, stop_rparen)) {
                    flush_before(t);
                    auto ds = parse_from_first_select(
                        stop_comma, stop_rbrace,
                        stop_rbracket, stop_rparen,
                        depth + 1);
                    parts.push_back(std::move(ds));
                    last_end = lex_.offset();
                    need_space = true;
                    continue;
                }
            }

            // Check for inline { or [ at depth 0
            if (paren_depth == 0 && t.type == TokenType::LBrace) {
                flush_before(t);
                auto obj = parse_object_literal(depth + 1);
                parts.push_back(std::move(obj));
                last_end = lex_.offset();
                need_space = true;
                continue;
            }

            if (paren_depth == 0 && t.type == TokenType::LBracket) {
                flush_before(t);
                auto arr = parse_array_literal(depth + 1);
                parts.push_back(std::move(arr));
                last_end = lex_.offset();
                need_space = true;
                continue;
            }

            // Track paren depth
            if (t.type == TokenType::LParen) ++paren_depth;
            if (t.type == TokenType::RParen) {
                if (paren_depth == 0)
                    lex_.error("unmatched ')'", t.line, t.col);
                --paren_depth;
            }

            // Track FROM context for join path detection.
            // -> and <- are only join operators after FROM/JOIN.
            if (paren_depth == 0 && t.type == TokenType::Ident) {
                if (is_from_or_join(t)) {
                    in_from_context = true;
                } else if (is_keyword(t, "SELECT") || is_keyword(t, "WHERE") ||
                           is_keyword(t, "GROUP") || is_keyword(t, "ORDER") ||
                           is_keyword(t, "HAVING") || is_keyword(t, "LIMIT") ||
                           is_keyword(t, "UNION") || is_keyword(t, "INTERSECT") ||
                           is_keyword(t, "EXCEPT") || is_keyword(t, "SET")) {
                    in_from_context = false;
                }
            }

            // Check for ident (-> | <-) ... (join path) — only in FROM context
            if (t.type == TokenType::Ident && paren_depth == 0 &&
                in_from_context) {
                auto st = lex_.save();
                Token alias_tok = lex_.next(); // consume ident
                Token arrow = lex_.peek();
                if (arrow.type == TokenType::Other &&
                    (arrow.text == "->" || arrow.text == "<-")) {
                    auto it = alias_map_.find(alias_tok.text);
                    if (it == alias_map_.end())
                        lex_.error("unknown table alias '" +
                                   alias_tok.text + "'",
                                   alias_tok.line, alias_tok.col);
                    auto jp = std::make_unique<JoinPath>();
                    jp->start_alias = alias_tok.text;
                    jp->start_table = it->second;
                    while (true) {
                        Token arr = lex_.peek();
                        if (arr.type != TokenType::Other ||
                            (arr.text != "->" && arr.text != "<-"))
                            break;
                        lex_.next(); // consume arrow
                        bool forward = (arr.text == "->");
                        Token table_tok = lex_.peek();
                        if (table_tok.type != TokenType::Ident)
                            lex_.error("expected table name after '" +
                                       arr.text + "'",
                                       arr.line, arr.col);
                        lex_.next(); // consume table
                        std::string alias;
                        Token next = lex_.peek();
                        if (next.type == TokenType::Ident &&
                            !is_sql_keyword(next.text)) {
                            lex_.next(); // consume alias
                            alias = next.text;
                        }
                        std::vector<std::pair<std::string,std::string>> columns;
                        parse_on_using(lex_, forward, &columns);
                        jp->steps.push_back({forward, table_tok.text, alias,
                                             std::move(columns)});
                    }
                    flush_before(alias_tok);
                    parts.push_back(std::move(jp));
                    last_end = lex_.offset();
                    need_space = true;
                    continue;
                }
                lex_.restore(st);
            }

            // Accumulate raw SQL token, with JSON path detection on ')'
            if (t.type == TokenType::LParen) {
                Token tok = lex_.next();
                // Record position in accum where '(' will be appended
                size_t pos = accum.size();
                if (has_raw && last_end < tok.src_begin) ++pos; // space will be added
                else if (need_space && last_end < tok.src_begin) ++pos;
                accum_paren_starts.push_back(pos);
                accum_token(tok);
            } else if (t.type == TokenType::RParen) {
                Token tok = lex_.next();
                accum_token(tok);
                if (!accum_paren_starts.empty()) {
                    size_t start = accum_paren_starts.back();
                    accum_paren_starts.pop_back();
                    if (try_transform_json_path(accum, start))
                        last_end = lex_.offset();
                }
            } else {
                Token tok = lex_.next();
                accum_token(tok);
            }
        }

        flush();
        return parts;
    }

    // Parse RECURSE ON (fk [= pk]) [WHERE ...]
    // Called after parsing object literal with a recursive field.
    std::unique_ptr<RecursiveSelect> parse_recursive_select(
            ObjectLiteral obj, bool singular,
            bool stop_comma, bool stop_rbrace,
            bool stop_rbracket, bool stop_rparen,
            int depth) {
        auto rs = std::make_unique<RecursiveSelect>();
        rs->singular = singular;

        // Separate recursive field from non-recursive fields
        for (auto& f : obj.fields) {
            if (f.recursive) {
                rs->children_field = f.key;
            } else {
                rs->fields.push_back(std::move(f));
            }
        }

        // Expect FROM table
        Token from_tok = lex_.peek();
        if (!is_keyword(from_tok, "FROM"))
            lex_.error("expected FROM after recursive object literal",
                       from_tok.line, from_tok.col);
        lex_.next(); // consume FROM
        Token table_tok = lex_.peek();
        if (table_tok.type != TokenType::Ident)
            lex_.error("expected table name after FROM",
                       table_tok.line, table_tok.col);
        lex_.next();
        rs->table = table_tok.text;

        // Expect RECURSE ON (fk [= pk])
        Token recurse_tok = lex_.peek();
        if (!is_keyword(recurse_tok, "RECURSE"))
            lex_.error("expected RECURSE after table name",
                       recurse_tok.line, recurse_tok.col);
        lex_.next();
        Token on_tok = lex_.peek();
        if (!is_keyword(on_tok, "ON"))
            lex_.error("expected ON after RECURSE",
                       on_tok.line, on_tok.col);
        lex_.next();
        Token lparen = lex_.peek();
        if (lparen.type != TokenType::LParen)
            lex_.error("expected '(' after RECURSE ON",
                       lparen.line, lparen.col);
        lex_.next();
        Token fk_tok = lex_.peek();
        if (fk_tok.type != TokenType::Ident)
            lex_.error("expected FK column name",
                       fk_tok.line, fk_tok.col);
        lex_.next();
        rs->fk_column = fk_tok.text;
        rs->pk_column = "id"; // default

        Token eq_or_rp = lex_.peek();
        if (eq_or_rp.type == TokenType::Other && eq_or_rp.text == "=") {
            lex_.next(); // consume =
            Token pk_tok = lex_.peek();
            if (pk_tok.type != TokenType::Ident)
                lex_.error("expected PK column name after '='",
                           pk_tok.line, pk_tok.col);
            lex_.next();
            rs->pk_column = pk_tok.text;
            eq_or_rp = lex_.peek();
        }
        if (eq_or_rp.type != TokenType::RParen)
            lex_.error("expected ')' after RECURSE ON clause",
                       eq_or_rp.line, eq_or_rp.col);
        lex_.next(); // consume )

        // Optional WHERE condition
        Token where_tok = lex_.peek();
        if (is_keyword(where_tok, "WHERE")) {
            lex_.next(); // consume WHERE
            rs->root_condition = parse_sql_parts(stop_comma, stop_rbrace,
                                                  stop_rbracket, stop_rparen,
                                                  depth);
        }

        return rs;
    }

    // Parse deep select — SELECT keyword has already been consumed.
    // singular: true if /1 was already consumed after SELECT.
    // Returns either a DeepSelect or a RecursiveSelect (via SqlPart).
    SqlPart parse_deep_or_recursive_select(
            const Token& select_tok,
            bool singular,
            bool stop_comma, bool stop_rbrace,
            bool stop_rbracket, bool stop_rparen,
            int depth) {
        check_depth(depth, select_tok.line, select_tok.col);

        Token t = lex_.peek();
        if (t.type == TokenType::LBrace) {
            auto obj = parse_object_literal(depth);

            // Check if any field is recursive
            for (const auto& f : obj->fields) {
                if (f.recursive) {
                    return parse_recursive_select(
                        std::move(*obj), singular,
                        stop_comma, stop_rbrace,
                        stop_rbracket, stop_rparen, depth);
                }
            }

            // Normal deep select
            auto ds = std::make_unique<DeepSelect>();
            ds->singular = singular;
            ds->projection = std::move(*obj);
            ds->tail = parse_sql_parts(stop_comma, stop_rbrace,
                                       stop_rbracket, stop_rparen, depth);
            return ds;
        } else if (t.type == TokenType::LBracket) {
            auto ds = std::make_unique<DeepSelect>();
            ds->singular = singular;
            ds->projection = std::move(*parse_array_literal(depth));
            ds->tail = parse_sql_parts(stop_comma, stop_rbrace,
                                       stop_rbracket, stop_rparen, depth);
            return ds;
        } else {
            lex_.error("expected '{' or '[' after SELECT",
                       select_tok.line, select_tok.col);
        }
    }

    std::unique_ptr<ObjectLiteral> parse_object_literal(int depth) {
        Token lbrace = lex_.next();
        if (lbrace.type != TokenType::LBrace)
            lex_.error("expected '{'", lbrace.line, lbrace.col);
        check_depth(depth, lbrace.line, lbrace.col);

        auto obj = std::make_unique<ObjectLiteral>();

        while (true) {
            Token t = lex_.peek();
            if (t.type == TokenType::RBrace) { lex_.next(); break; }
            if (t.type == TokenType::Eof)
                lex_.error("unterminated '{'", lbrace.line, lbrace.col);

            obj->fields.push_back(parse_field(depth));

            t = lex_.peek();
            if (t.type == TokenType::Comma) {
                lex_.next();
            } else if (t.type != TokenType::RBrace) {
                lex_.error("expected ',' or '}' in object literal");
            }
        }

        return obj;
    }

    ObjectLiteral::Field parse_field(int depth) {
        ObjectLiteral::Field field;

        Token key = lex_.peek();
        if (key.type == TokenType::LParen) {
            // Computed key: (expr): value
            lex_.next(); // consume '('
            field.computed_key = parse_sql_parts(/*stop_comma=*/false,
                                                 /*stop_rbrace=*/false,
                                                 /*stop_rbracket=*/false,
                                                 /*stop_rparen=*/true,
                                                 depth);
            if (field.computed_key.empty())
                lex_.error("expected expression in computed key", key.line, key.col);
            Token rparen = lex_.peek();
            if (rparen.type != TokenType::RParen)
                lex_.error("expected ')' after computed key", rparen.line, rparen.col);
            lex_.next(); // consume ')'
        } else {
            key = lex_.next();
            if (key.type == TokenType::Ident) {
                field.key = key.text;
            } else if (key.type == TokenType::DqString) {
                // Strip outer quotes and unescape \" → " and \\ → \.
                auto raw = key.text.substr(1, key.text.size() - 2);
                field.key.reserve(raw.size());
                for (size_t i = 0; i < raw.size(); ++i) {
                    if (raw[i] == '\\' && i + 1 < raw.size() &&
                        (raw[i + 1] == '"' || raw[i + 1] == '\\')) {
                        field.key += raw[++i];
                    } else if (raw[i] == '"' && i + 1 < raw.size() && raw[i + 1] == '"') {
                        field.key += '"';
                        ++i; // skip doubled quote
                    } else {
                        field.key += raw[i];
                    }
                }
            } else {
                lex_.error("expected field name (identifier, double-quoted string, or computed key)",
                           key.line, key.col);
            }
        }

        Token t = lex_.peek();
        if (!field.computed_key.empty() && t.type != TokenType::Colon)
            lex_.error("expected ':' after computed key", t.line, t.col);
        if (t.type == TokenType::Colon) {
            lex_.next();

            // Check for * → recursive field
            Token t2 = lex_.peek();
            if (t2.type == TokenType::Other && t2.text == "*") {
                lex_.next(); // consume *
                field.recursive = true;
                return field;
            }

            // Check for SELECT expr (no FROM) → aggregate field
            t2 = lex_.peek();
            if (is_keyword(t2, "SELECT")) {
                auto st = lex_.save();
                lex_.next(); // consume SELECT
                bool singular = try_consume_singular();
                Token t3 = lex_.peek();
                if (t3.type != TokenType::LBrace && t3.type != TokenType::LBracket) {
                    // SELECT expr (no { or [) — aggregate over current group
                    field.aggregate = !singular;
                    field.value = parse_sql_parts(/*stop_comma=*/true,
                                                  /*stop_rbrace=*/true,
                                                  /*stop_rbracket=*/false,
                                                  /*stop_rparen=*/false,
                                                  depth);
                    if (field.value.empty())
                        lex_.error("expected expression after 'SELECT'",
                                   t2.line, t2.col);
                    return field;
                }
                // SELECT {/[ — restore and fall through to normal parsing
                lex_.restore(st);
            }

            field.value = parse_sql_parts(/*stop_comma=*/true,
                                          /*stop_rbrace=*/true,
                                          /*stop_rbracket=*/false,
                                          /*stop_rparen=*/false,
                                          depth);
            if (field.value.empty())
                lex_.error("expected expression after ':'", t.line, t.col);
        }

        return field;
    }

    std::unique_ptr<ArrayLiteral> parse_array_literal(int depth) {
        Token lbracket = lex_.next();
        if (lbracket.type != TokenType::LBracket)
            lex_.error("expected '['", lbracket.line, lbracket.col);
        check_depth(depth, lbracket.line, lbracket.col);

        auto arr = std::make_unique<ArrayLiteral>();

        while (true) {
            Token t = lex_.peek();
            if (t.type == TokenType::RBracket) { lex_.next(); break; }
            if (t.type == TokenType::Eof)
                lex_.error("unterminated '['", lbracket.line, lbracket.col);

            auto elem = parse_sql_parts(/*stop_comma=*/true,
                                        /*stop_rbrace=*/false,
                                        /*stop_rbracket=*/true,
                                        /*stop_rparen=*/false,
                                        depth);
            if (elem.empty())
                lex_.error("expected expression in array literal");
            arr->elements.push_back(std::move(elem));

            t = lex_.peek();
            if (t.type == TokenType::Comma) {
                lex_.next();
            } else if (t.type != TokenType::RBracket) {
                lex_.error("expected ',' or ']' in array literal");
            }
        }

        return arr;
    }

    // Check if '(' at position start in accum is a JSON path base
    // (not a function call). A function call has an identifier (not a SQL
    // keyword) immediately before '('. SQL keywords like WHERE, AND, SELECT
    // can precede parenthesized JSON path bases.
    static bool can_be_json_path_base(const std::string& accum, size_t start) {
        if (start == 0) return true;
        size_t i = start;
        // Skip trailing spaces
        while (i > 0 && accum[i - 1] == ' ') --i;
        if (i == 0) return true;
        char c = accum[i - 1];
        if (c == ')') return false; // nested parens = function-like
        if (!(std::isalnum(static_cast<unsigned char>(c)) || c == '_'))
            return true; // operator, comma, etc. — not a function call
        // Extract the preceding word
        size_t end = i;
        while (i > 0 && (std::isalnum(static_cast<unsigned char>(accum[i - 1])) ||
                         accum[i - 1] == '_'))
            --i;
        std::string word = accum.substr(i, end - i);
        // SQL keywords can precede a path base; bare identifiers are function calls
        return is_sql_keyword(word);
    }

    // After accumulating ')' at the end of accum, check if the paren group
    // starting at `start` is followed by .ident or [number] path segments.
    // If so, transform it into json_extract() / jsonb_extract_path() in place.
    // Returns true if a transformation was applied (tokens consumed from lexer).
    bool try_transform_json_path(std::string& accum, size_t start) {
        if (!can_be_json_path_base(accum, start)) return false;

        // Peek ahead for .ident or [
        Token next = lex_.peek();
        bool has_dot = (next.type == TokenType::Other && next.text == ".");
        bool has_bracket = (next.type == TokenType::LBracket);
        if (!has_dot && !has_bracket) return false;

        // If dot, check it's followed by an ident (not a number or operator)
        if (has_dot) {
            auto st = lex_.save();
            lex_.next(); // consume .
            Token after_dot = lex_.peek();
            lex_.restore(st);
            if (after_dot.type != TokenType::Ident) return false;
        }

        // Extract base expression (everything inside parens, excluding parens)
        std::string base = accum.substr(start + 1, accum.size() - start - 2);
        accum.resize(start);

        // Parse path segments
        struct PathSeg {
            bool is_field; // true = .ident, false = [number]
            std::string value;
        };
        std::vector<PathSeg> segs;

        while (true) {
            Token t = lex_.peek();
            if (t.type == TokenType::Other && t.text == ".") {
                auto st = lex_.save();
                lex_.next(); // consume .
                Token ident = lex_.peek();
                if (ident.type != TokenType::Ident) {
                    lex_.restore(st);
                    break;
                }
                lex_.next(); // consume ident
                segs.push_back({true, ident.text});
            } else if (t.type == TokenType::LBracket) {
                lex_.next(); // consume [
                Token idx = lex_.peek();
                if (idx.type != TokenType::Number)
                    lex_.error("expected array index", idx.line, idx.col);
                lex_.next(); // consume number
                Token rb = lex_.peek();
                if (rb.type != TokenType::RBracket)
                    lex_.error("expected ']'", rb.line, rb.col);
                lex_.next(); // consume ]
                segs.push_back({false, idx.text});
            } else {
                break;
            }
        }

        if (segs.empty()) {
            // No segments parsed — restore the parens
            accum += "(";
            accum += base;
            accum += ")";
            return false;
        }

        // Render json_extract / jsonb_extract_path
        if (backend_ == Backend::postgres) {
            accum += "jsonb_extract_path(";
            accum += base;
            for (const auto& seg : segs) {
                accum += ", '";
                accum += seg.value;
                accum += "'";
            }
            accum += ")";
        } else {
            accum += "json_extract(";
            accum += base;
            accum += ", '$";
            for (const auto& seg : segs) {
                if (seg.is_field) {
                    accum += ".";
                    accum += seg.value;
                } else {
                    accum += "[";
                    accum += seg.value;
                    accum += "]";
                }
            }
            accum += "')";
        }
        return true;
    }

    Lexer& lex_;
    std::unordered_map<std::string, std::string> alias_map_;
    Backend backend_;
};

// ── Renderer ────────────────────────────────────────────────────────

// Escape single-quote characters for use inside a SQL string literal.
static std::string sql_escape_key(const std::string& s) {
    std::string r;
    r.reserve(s.size());
    for (char c : s) {
        if (c == '\'') r += "''";
        else r += c;
    }
    return r;
}

// FK index: maps (from_table, to_table) → list of FKs between them.
using FkIndex = std::map<std::pair<std::string,std::string>,
                         std::vector<const ForeignKey*>>;

FkIndex build_fk_index(const std::vector<ForeignKey>& fks) {
    FkIndex idx;
    for (const auto& fk : fks) {
        idx[{fk.from_table, fk.to_table}].push_back(&fk);
    }
    return idx;
}

// Resolve column pairs for a join between child_table and parent_table.
// In convention mode (fk_index == nullptr), returns {(parent+"_id", parent+"_id")}.
// In FK mode, looks up the index and errors if 0 or 2+ matches.
std::vector<std::pair<std::string,std::string>>
resolve_fk_columns(const std::string& child_table,
                   const std::string& parent_table,
                   const FkIndex* fk_index) {
    if (!fk_index) {
        // Convention mode
        std::string col = parent_table + "_id";
        return {{col, col}};
    }
    auto it = fk_index->find({child_table, parent_table});
    if (it == fk_index->end() || it->second.empty()) {
        throw Error("no foreign key from '" + child_table + "' to '" +
                    parent_table + "'", 0, 0);
    }
    if (it->second.size() > 1) {
        throw Error("ambiguous foreign key from '" + child_table + "' to '" +
                    parent_table + "' (" + std::to_string(it->second.size()) +
                    " candidates)", 0, 0);
    }
    const auto& fk = *it->second[0];
    std::vector<std::pair<std::string,std::string>> cols;
    cols.reserve(fk.columns.size());
    for (const auto& cp : fk.columns) {
        cols.emplace_back(cp.from_column, cp.to_column);
    }
    return cols;
}

class Renderer {
public:
    explicit Renderer(const FkIndex* fk_index = nullptr,
                      Backend backend = Backend::sqlite)
        : fk_index_(fk_index), backend_(backend) {
        switch (backend) {
        case Backend::postgres:
            fn_object_      = "jsonb_build_object";
            fn_array_       = "jsonb_build_array";
            fn_group_array_ = "jsonb_agg";
            break;
        default:
            fn_object_      = "json_object";
            fn_array_       = "json_array";
            fn_group_array_ = "json_group_array";
            break;
        }
    }

    std::string render_document(const SqlParts& parts) {
        std::string out;
        render_parts(parts, out, /*nested=*/false);
        return out;
    }

private:
    void render_parts(const SqlParts& parts, std::string& out, bool nested) {
        for (const auto& part : parts) {
            std::visit([&](const auto& v) {
                using T = std::decay_t<decltype(v)>;
                if constexpr (std::is_same_v<T, std::string>) {
                    out += v;
                } else if constexpr (std::is_same_v<T, std::unique_ptr<DeepSelect>>) {
                    render_deep_select(*v, out, nested);
                } else if constexpr (std::is_same_v<T, std::unique_ptr<ObjectLiteral>>) {
                    render_object(*v, out);
                } else if constexpr (std::is_same_v<T, std::unique_ptr<ArrayLiteral>>) {
                    render_array(*v, out);
                } else if constexpr (std::is_same_v<T, std::unique_ptr<JoinPath>>) {
                    render_join_path(*v, out);
                } else if constexpr (std::is_same_v<T, std::unique_ptr<RecursiveSelect>>) {
                    render_recursive_select(*v, out);
                }
            }, part);
        }
    }

    void render_deep_select(const DeepSelect& ds, std::string& out, bool nested) {
        // Plain FROM-first: just rearrange, no JSON wrapping
        if (std::holds_alternative<SqlParts>(ds.projection)) {
            if (nested) out += "(";
            out += "SELECT ";
            render_parts(std::get<SqlParts>(ds.projection), out, true);
            if (!ds.tail.empty()) {
                out += " ";
                render_parts(ds.tail, out, true);
            }
            if (nested) out += ")";
            return;
        }

        if (nested) out += "(";
        out += "SELECT ";

        bool is_object = std::holds_alternative<ObjectLiteral>(ds.projection);
        bool use_group = nested && !ds.singular;

        if (use_group) { out += fn_group_array_; out += "("; }

        if (is_object) {
            render_object(std::get<ObjectLiteral>(ds.projection), out);
        } else {
            const auto& arr = std::get<ArrayLiteral>(ds.projection);
            if (arr.elements.size() == 1) {
                if (!nested && !ds.singular) { out += fn_group_array_; out += "("; }
                render_parts(arr.elements[0], out, /*nested=*/true);
                if (!nested && !ds.singular) out += ")";
            } else {
                if (!nested && !ds.singular) { out += fn_group_array_; out += "("; }
                render_array(arr, out);
                if (!nested && !ds.singular) out += ")";
            }
        }

        if (use_group) out += ")";

        if (!ds.tail.empty()) {
            out += " ";
            render_parts(ds.tail, out, /*nested=*/true);
        }

        if (ds.singular) out += " LIMIT 1";

        if (nested) out += ")";
    }

    void render_object(const ObjectLiteral& obj, std::string& out) {
        out += fn_object_;
        out += "(";
        for (size_t i = 0; i < obj.fields.size(); ++i) {
            if (i > 0) out += ", ";
            const auto& f = obj.fields[i];
            if (!f.computed_key.empty()) {
                render_parts(f.computed_key, out, /*nested=*/true);
            } else {
                out += "'";
                out += sql_escape_key(f.key);
                out += "'";
            }
            out += ", ";
            if (f.value.empty()) {
                out += f.key;
            } else if (f.aggregate) {
                out += fn_group_array_;
                out += "(";
                render_parts(f.value, out, /*nested=*/true);
                out += ")";
            } else {
                render_parts(f.value, out, /*nested=*/true);
            }
        }
        out += ")";
    }

    void render_array(const ArrayLiteral& arr, std::string& out) {
        out += fn_array_;
        out += "(";
        for (size_t i = 0; i < arr.elements.size(); ++i) {
            if (i > 0) out += ", ";
            render_parts(arr.elements[i], out, /*nested=*/true);
        }
        out += ")";
    }

    // Emit "lhs.col1 = rhs.col1 [AND lhs.col2 = rhs.col2 ...]"
    static void emit_join_condition(
            const std::vector<std::pair<std::string,std::string>>& cols,
            const std::string& child_ref,
            const std::string& parent_ref,
            std::string& out) {
        for (size_t i = 0; i < cols.size(); ++i) {
            if (i > 0) out += " AND ";
            out += child_ref + "." + cols[i].first + " = " +
                   parent_ref + "." + cols[i].second;
        }
    }

    void render_join_path(const JoinPath& jp, std::string& out) {
        // Step 1: FROM target
        const auto& s1 = jp.steps[0];
        out += s1.table;
        const auto& s1_ref = s1.alias.empty() ? s1.table : s1.alias;
        if (!s1.alias.empty()) {
            out += " ";
            out += s1.alias;
        }

        // Steps 2+: JOINs
        std::string prev_table = s1.table;
        std::string prev_ref = s1_ref;
        for (size_t i = 1; i < jp.steps.size(); ++i) {
            const auto& step = jp.steps[i];
            const auto& step_ref = step.alias.empty() ? step.table : step.alias;
            out += " JOIN ";
            out += step.table;
            if (!step.alias.empty()) {
                out += " ";
                out += step.alias;
            }
            out += " ON ";
            if (step.forward) {
                // curr is child of prev
                auto cols = step.columns.empty()
                    ? resolve_fk_columns(step.table, prev_table, fk_index_)
                    : step.columns;
                emit_join_condition(cols, step_ref, prev_ref, out);
            } else {
                // prev is child of curr
                auto cols = step.columns.empty()
                    ? resolve_fk_columns(prev_table, step.table, fk_index_)
                    : step.columns;
                emit_join_condition(cols, prev_ref, step_ref, out);
            }
            prev_table = step.table;
            prev_ref = step_ref;
        }

        // WHERE: correlate step 1 to start alias
        out += " WHERE ";
        if (s1.forward) {
            auto cols = s1.columns.empty()
                ? resolve_fk_columns(s1.table, jp.start_table, fk_index_)
                : s1.columns;
            emit_join_condition(cols, s1_ref, jp.start_alias, out);
        } else {
            auto cols = s1.columns.empty()
                ? resolve_fk_columns(jp.start_table, s1.table, fk_index_)
                : s1.columns;
            emit_join_condition(cols, jp.start_alias, s1_ref, out);
        }
    }

    void render_recursive_select(const RecursiveSelect& rs, std::string& out) {
        bool is_pg = (backend_ == Backend::postgres);

        // Build json_object(...) argument list for non-recursive fields
        std::string obj_args;
        std::string col_list;    // column names for CTE
        std::string col_select;  // column references with c. prefix for recursive step
        for (size_t i = 0; i < rs.fields.size(); ++i) {
            const auto& f = rs.fields[i];
            std::string col = f.value.empty() ? f.key : f.key; // column name
            std::string expr;
            if (f.value.empty()) {
                expr = f.key; // bare field
            } else {
                // For renamed fields, the value is the SQL expression
                std::string val_str;
                // Render the value parts to a string
                Renderer tmp(fk_index_, backend_);
                val_str = tmp.render_document(f.value);
                expr = val_str;
                col = val_str; // use the expression as the column name
            }
            if (i > 0) { col_list += ", "; col_select += ", "; }
            col_list += col;
            col_select += "c." + col;

            if (i > 0) obj_args += ", ";
            obj_args += "'";
            obj_args += sql_escape_key(f.key);
            obj_args += "', ";
            obj_args += col;
        }

        // Add FK and PK columns to CTE column list
        col_list += ", " + rs.fk_column;
        col_select += ", c." + rs.fk_column;
        if (rs.pk_column != rs.fk_column) {
            // PK might already be in the field list
            bool pk_in_fields = false;
            for (const auto& f : rs.fields) {
                std::string col = f.value.empty() ? f.key : f.key;
                if (f.value.empty() && f.key == rs.pk_column) { pk_in_fields = true; break; }
            }
            if (!pk_in_fields) {
                col_list += ", " + rs.pk_column;
                col_select += ", c." + rs.pk_column;
            }
        }

        std::string pad_fn = is_pg
            ? "lpad(CAST(" + rs.pk_column + " AS text), 10, '0')"
            : "printf('%010d', " + rs.pk_column + ")";
        std::string c_pad_fn = is_pg
            ? "lpad(CAST(c." + rs.pk_column + " AS text), 10, '0')"
            : "printf('%010d', c." + rs.pk_column + ")";
        std::string high_char = is_pg ? "chr(127)" : "char(127)";
        std::string concat_fn_open = is_pg
            ? "string_agg(_fragment, '' ORDER BY _sort_key)"
            : "group_concat(_fragment, '')";

        // Emit the 3-CTE bracket-injection template
        out += "WITH RECURSIVE _sdq_dfs(";
        out += col_list;
        out += ", _depth, _path) AS (SELECT ";
        out += col_list;
        out += ", 0, ";
        out += pad_fn;
        out += " FROM ";
        out += rs.table;
        if (!rs.root_condition.empty()) {
            out += " WHERE ";
            render_parts(rs.root_condition, out, /*nested=*/false);
        }
        out += " UNION ALL SELECT ";
        out += col_select;
        out += ", d._depth + 1, d._path || '/' || ";
        out += c_pad_fn;
        out += " FROM ";
        out += rs.table;
        out += " c JOIN _sdq_dfs d ON c.";
        out += rs.fk_column;
        out += " = d.";
        out += rs.pk_column;
        out += "), _sdq_ranked AS (SELECT *, ";
        out += fn_object_;
        out += "(";
        out += obj_args;
        out += ") AS _obj, ROW_NUMBER() OVER (PARTITION BY ";
        out += rs.fk_column;
        out += " ORDER BY ";
        out += rs.pk_column;
        out += ") AS _child_rank FROM _sdq_dfs), ";
        out += "_sdq_events(_sort_key, _fragment) AS (SELECT _path, ";
        out += "CASE WHEN _child_rank > 1 THEN ',' ELSE '' END || ";
        out += "substr(_obj, 1, length(_obj) - 1) || ',\"";
        out += sql_escape_key(rs.children_field);
        out += "\":[' FROM _sdq_ranked UNION ALL SELECT _path || ";
        out += high_char;
        out += ", ']}' FROM _sdq_ranked) SELECT ";

        if (!rs.singular) {
            out += "'[' || ";
        }
        if (is_pg) {
            out += concat_fn_open;
        } else {
            out += "group_concat(_fragment, '')";
        }
        if (!rs.singular) {
            out += " || ']'";
        }
        out += " FROM (SELECT _fragment FROM _sdq_events ORDER BY _sort_key)";
    }

    const FkIndex* fk_index_;
    Backend backend_;
    const char* fn_object_;
    const char* fn_array_;
    const char* fn_group_array_;
};

} // anonymous namespace

// ── Public API ──────────────────────────────────────────────────────

std::string transpile(const std::string& input) {
    auto alias_map = build_alias_map(input);
    Lexer lex(input);
    Parser parser(lex, std::move(alias_map));
    SqlParts doc = parser.parse_document();
    Renderer renderer;
    return renderer.render_document(doc);
}

std::string transpile(const std::string& input,
                      const std::vector<ForeignKey>& foreign_keys) {
    auto alias_map = build_alias_map(input);
    Lexer lex(input);
    Parser parser(lex, std::move(alias_map));
    SqlParts doc = parser.parse_document();
    auto fk_idx = build_fk_index(foreign_keys);
    Renderer renderer(&fk_idx);
    return renderer.render_document(doc);
}

std::string transpile(const std::string& input, Backend backend) {
    auto alias_map = build_alias_map(input);
    Lexer lex(input);
    Parser parser(lex, std::move(alias_map), backend);
    SqlParts doc = parser.parse_document();
    Renderer renderer(nullptr, backend);
    return renderer.render_document(doc);
}

std::string transpile(const std::string& input,
                      const std::vector<ForeignKey>& foreign_keys,
                      Backend backend) {
    auto alias_map = build_alias_map(input);
    Lexer lex(input);
    Parser parser(lex, std::move(alias_map), backend);
    SqlParts doc = parser.parse_document();
    auto fk_idx = build_fk_index(foreign_keys);
    Renderer renderer(&fk_idx, backend);
    return renderer.render_document(doc);
}

} // namespace sqldeep

// ── C API bridge ────────────────────────────────────────────────────

namespace {

// Duplicate a std::string to a malloc'd C string (caller frees with sqldeep_free).
char* dup_str(const std::string& s) {
    char* p = static_cast<char*>(std::malloc(s.size() + 1));
    if (p) std::memcpy(p, s.c_str(), s.size() + 1);
    return p;
}

// Set error output pointers. msg is malloc'd; caller frees with sqldeep_free.
void set_error(char** err_msg, int* err_line, int* err_col,
               const sqldeep::Error& e) {
    if (err_msg)  *err_msg = dup_str(e.what());
    if (err_line) *err_line = e.line();
    if (err_col)  *err_col = e.col();
}

void clear_error(char** err_msg, int* err_line, int* err_col) {
    if (err_msg)  *err_msg = nullptr;
    if (err_line) *err_line = 0;
    if (err_col)  *err_col = 0;
}

sqldeep::Backend to_backend(sqldeep_backend b) {
    return b == SQLDEEP_POSTGRES ? sqldeep::Backend::postgres
                                 : sqldeep::Backend::sqlite;
}

std::vector<sqldeep::ForeignKey> to_cpp_fks(const sqldeep_foreign_key* fks,
                                             int fk_count) {
    std::vector<sqldeep::ForeignKey> cpp_fks;
    cpp_fks.reserve(fk_count);
    for (int i = 0; i < fk_count; ++i) {
        sqldeep::ForeignKey fk;
        fk.from_table = fks[i].from_table;
        fk.to_table   = fks[i].to_table;
        fk.columns.reserve(fks[i].column_count);
        for (int j = 0; j < fks[i].column_count; ++j) {
            fk.columns.push_back({
                fks[i].columns[j].from_column,
                fks[i].columns[j].to_column,
            });
        }
        cpp_fks.push_back(std::move(fk));
    }
    return cpp_fks;
}

} // namespace

extern "C" {

char* sqldeep_transpile(const char* input,
                        char** err_msg, int* err_line, int* err_col) {
    return sqldeep_transpile_backend(input, SQLDEEP_SQLITE,
                                     err_msg, err_line, err_col);
}

char* sqldeep_transpile_fk(const char* input,
                           const sqldeep_foreign_key* fks, int fk_count,
                           char** err_msg, int* err_line, int* err_col) {
    return sqldeep_transpile_fk_backend(input, SQLDEEP_SQLITE, fks, fk_count,
                                        err_msg, err_line, err_col);
}

char* sqldeep_transpile_backend(const char* input,
                                sqldeep_backend backend,
                                char** err_msg, int* err_line, int* err_col) {
    clear_error(err_msg, err_line, err_col);
    try {
        return dup_str(sqldeep::transpile(input, to_backend(backend)));
    } catch (const sqldeep::Error& e) {
        set_error(err_msg, err_line, err_col, e);
        return nullptr;
    }
}

char* sqldeep_transpile_fk_backend(const char* input,
                                   sqldeep_backend backend,
                                   const sqldeep_foreign_key* fks, int fk_count,
                                   char** err_msg, int* err_line, int* err_col) {
    clear_error(err_msg, err_line, err_col);
    try {
        auto cpp_fks = to_cpp_fks(fks, fk_count);
        return dup_str(sqldeep::transpile(input, cpp_fks, to_backend(backend)));
    } catch (const sqldeep::Error& e) {
        set_error(err_msg, err_line, err_col, e);
        return nullptr;
    }
}

const char* sqldeep_version(void) {
    return SQLDEEP_VERSION;
}

void sqldeep_free(void* ptr) {
    std::free(ptr);
}

} // extern "C"
