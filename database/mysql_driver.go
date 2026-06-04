package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	mysql "github.com/go-sql-driver/mysql"
)

const mysqlDriverName = "mysql56"

var mysqlExcludedValuePattern = regexp.MustCompile(`(?i)\bexcluded\.([A-Za-z_][A-Za-z0-9_]*)`)
var mysqlCastAsTextPattern = regexp.MustCompile(`(?i)CAST\(([^()]*)\s+AS\s+TEXT\)`)
var mysqlDoNothingPattern = regexp.MustCompile(`(?is)\s+ON\s+CONFLICT\s*(?:\([^)]*\))?\s+DO\s+NOTHING(?:\s+RETURNING\s+.+)?\s*$`)
var mysqlKeywordBeforeKey = map[string]struct{}{
	"duplicate": {},
	"foreign":   {},
	"primary":   {},
	"unique":    {},
}

func init() {
	sql.Register(mysqlDriverName, mysqlRewriteDriver{inner: &mysql.MySQLDriver{}})
}

type mysqlRewriteDriver struct {
	inner driver.Driver
}

func (d mysqlRewriteDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return mysqlRewriteConn{Conn: conn}, nil
}

type mysqlRewriteConn struct {
	driver.Conn
}

func (c mysqlRewriteConn) Prepare(query string) (driver.Stmt, error) {
	rewritten, paramOrder := rewriteSQLForMySQLWithParamOrder(query)
	stmt, err := c.Conn.Prepare(rewritten)
	if err != nil {
		return nil, err
	}
	return mysqlRewriteStmt{Stmt: stmt, paramOrder: paramOrder}, nil
}

func (c mysqlRewriteConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	rewritten, paramOrder := rewriteSQLForMySQLWithParamOrder(query)
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		stmt, err := pc.PrepareContext(ctx, rewritten)
		if err != nil {
			return nil, err
		}
		return mysqlRewriteStmt{Stmt: stmt, paramOrder: paramOrder}, nil
	}
	stmt, err := c.Conn.Prepare(rewritten)
	if err != nil {
		return nil, err
	}
	return mysqlRewriteStmt{Stmt: stmt, paramOrder: paramOrder}, nil
}

func (c mysqlRewriteConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if execer, ok := c.Conn.(driver.ExecerContext); ok {
		rewritten, paramOrder := rewriteSQLForMySQLWithParamOrder(query)
		rewrittenArgs, err := rewriteMySQLArgs(args, paramOrder)
		if err != nil {
			return nil, err
		}
		return execer.ExecContext(ctx, rewritten, rewrittenArgs)
	}
	return nil, driver.ErrSkip
}

func (c mysqlRewriteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if queryer, ok := c.Conn.(driver.QueryerContext); ok {
		rewritten, paramOrder := rewriteSQLForMySQLWithParamOrder(query)
		rewrittenArgs, err := rewriteMySQLArgs(args, paramOrder)
		if err != nil {
			return nil, err
		}
		return queryer.QueryContext(ctx, rewritten, rewrittenArgs)
	}
	return nil, driver.ErrSkip
}

func (c mysqlRewriteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if beginner, ok := c.Conn.(driver.ConnBeginTx); ok {
		return beginner.BeginTx(ctx, opts)
	}
	return c.Conn.Begin()
}

func (c mysqlRewriteConn) Ping(ctx context.Context) error {
	if pinger, ok := c.Conn.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

func (c mysqlRewriteConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c mysqlRewriteConn) CheckNamedValue(v *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(v)
	}
	return driver.ErrSkip
}

func (c mysqlRewriteConn) IsValid() bool {
	if validator, ok := c.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

func (c mysqlRewriteConn) Close() error {
	if closer, ok := c.Conn.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

type mysqlRewriteStmt struct {
	driver.Stmt
	paramOrder []int
}

func (s mysqlRewriteStmt) NumInput() int {
	if len(s.paramOrder) > 0 {
		max := 0
		for _, n := range s.paramOrder {
			if n > max {
				max = n
			}
		}
		return max
	}
	return s.Stmt.NumInput()
}

func (s mysqlRewriteStmt) Exec(args []driver.Value) (driver.Result, error) {
	rewrittenArgs, err := rewriteMySQLValues(args, s.paramOrder)
	if err != nil {
		return nil, err
	}
	return s.Stmt.Exec(rewrittenArgs)
}

func (s mysqlRewriteStmt) Query(args []driver.Value) (driver.Rows, error) {
	rewrittenArgs, err := rewriteMySQLValues(args, s.paramOrder)
	if err != nil {
		return nil, err
	}
	return s.Stmt.Query(rewrittenArgs)
}

func (s mysqlRewriteStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	rewrittenArgs, err := rewriteMySQLArgs(args, s.paramOrder)
	if err != nil {
		return nil, err
	}
	if execer, ok := s.Stmt.(driver.StmtExecContext); ok {
		return execer.ExecContext(ctx, rewrittenArgs)
	}
	values := namedValuesToValues(rewrittenArgs)
	return s.Stmt.Exec(values)
}

func (s mysqlRewriteStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	rewrittenArgs, err := rewriteMySQLArgs(args, s.paramOrder)
	if err != nil {
		return nil, err
	}
	if queryer, ok := s.Stmt.(driver.StmtQueryContext); ok {
		return queryer.QueryContext(ctx, rewrittenArgs)
	}
	values := namedValuesToValues(rewrittenArgs)
	return s.Stmt.Query(values)
}

func rewriteSQLForMySQL(query string) string {
	rewritten, _ := rewriteSQLForMySQLWithParamOrder(query)
	return rewritten
}

func rewriteSQLForMySQLWithParamOrder(query string) (string, []int) {
	if query == "" {
		return query, nil
	}
	query = rewritePostgresUpsertForMySQL(query)
	query = mysqlCastAsTextPattern.ReplaceAllString(query, "CAST($1 AS CHAR)")
	if strings.Contains(strings.ToLower(query), "api_keys") {
		query = quoteMySQLAPIKeyIdentifier(query)
	}
	var b strings.Builder
	b.Grow(len(query))
	paramOrder := []int{}

	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(query); i++ {
		ch := query[i]
		next := byte(0)
		if i+1 < len(query) {
			next = query[i+1]
		}

		if inLineComment {
			b.WriteByte(ch)
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			b.WriteByte(ch)
			if ch == '*' && next == '/' {
				b.WriteByte(next)
				i++
				inBlockComment = false
			}
			continue
		}
		if inSingle {
			b.WriteByte(ch)
			if ch == '\'' {
				if next == '\'' || next == '\\' {
					b.WriteByte(next)
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			b.WriteByte(ch)
			if ch == '"' {
				inDouble = false
			}
			continue
		}

		switch {
		case ch == '-' && next == '-':
			b.WriteByte(ch)
			b.WriteByte(next)
			i++
			inLineComment = true
		case ch == '/' && next == '*':
			b.WriteByte(ch)
			b.WriteByte(next)
			i++
			inBlockComment = true
		case ch == '\'':
			b.WriteByte(ch)
			inSingle = true
		case ch == '"':
			b.WriteByte(ch)
			inDouble = true
		case ch == '$' && next >= '0' && next <= '9':
			start := i + 1
			b.WriteByte('?')
			i++
			for i+1 < len(query) && query[i+1] >= '0' && query[i+1] <= '9' {
				i++
			}
			if n, err := strconv.Atoi(query[start : i+1]); err == nil && n > 0 {
				paramOrder = append(paramOrder, n)
			}
		case ch == ':' && next == ':':
			i += 2
			for i < len(query) {
				r, size := rune(query[i]), 1
				if r >= utf8.RuneSelf {
					r, size = utf8.DecodeRuneInString(query[i:])
				}
				if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
					i--
					break
				}
				i += size
			}
		default:
			b.WriteByte(ch)
		}
	}
	return b.String(), paramOrder
}

func rewriteMySQLArgs(args []driver.NamedValue, paramOrder []int) ([]driver.NamedValue, error) {
	if len(paramOrder) == 0 {
		return args, nil
	}
	rewritten := make([]driver.NamedValue, 0, len(paramOrder))
	for i, paramIndex := range paramOrder {
		if paramIndex < 1 || paramIndex > len(args) {
			return nil, fmt.Errorf("mysql placeholder $%d has no matching argument", paramIndex)
		}
		arg := args[paramIndex-1]
		arg.Ordinal = i + 1
		arg.Name = ""
		rewritten = append(rewritten, arg)
	}
	return rewritten, nil
}

func rewriteMySQLValues(args []driver.Value, paramOrder []int) ([]driver.Value, error) {
	if len(paramOrder) == 0 {
		return args, nil
	}
	rewritten := make([]driver.Value, 0, len(paramOrder))
	for _, paramIndex := range paramOrder {
		if paramIndex < 1 || paramIndex > len(args) {
			return nil, fmt.Errorf("mysql placeholder $%d has no matching argument", paramIndex)
		}
		rewritten = append(rewritten, args[paramIndex-1])
	}
	return rewritten, nil
}

func namedValuesToValues(args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, 0, len(args))
	for _, arg := range args {
		values = append(values, arg.Value)
	}
	return values
}

func quoteMySQLAPIKeyIdentifier(query string) string {
	var b strings.Builder
	b.Grow(len(query))

	inSingle := false
	inDouble := false
	inBacktick := false
	inLineComment := false
	inBlockComment := false
	lastToken := ""

	for i := 0; i < len(query); i++ {
		ch := query[i]
		next := byte(0)
		if i+1 < len(query) {
			next = query[i+1]
		}

		if inLineComment {
			b.WriteByte(ch)
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			b.WriteByte(ch)
			if ch == '*' && next == '/' {
				b.WriteByte(next)
				i++
				inBlockComment = false
			}
			continue
		}
		if inSingle {
			b.WriteByte(ch)
			if ch == '\'' {
				if next == '\'' || next == '\\' {
					b.WriteByte(next)
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			b.WriteByte(ch)
			if ch == '"' {
				inDouble = false
			}
			continue
		}
		if inBacktick {
			b.WriteByte(ch)
			if ch == '`' {
				inBacktick = false
			}
			continue
		}

		switch {
		case ch == '-' && next == '-':
			b.WriteByte(ch)
			b.WriteByte(next)
			i++
			inLineComment = true
		case ch == '/' && next == '*':
			b.WriteByte(ch)
			b.WriteByte(next)
			i++
			inBlockComment = true
		case ch == '\'':
			b.WriteByte(ch)
			inSingle = true
		case ch == '"':
			b.WriteByte(ch)
			inDouble = true
		case ch == '`':
			b.WriteByte(ch)
			inBacktick = true
		case isSQLIdentifierStart(ch):
			start := i
			for i+1 < len(query) && isSQLIdentifierPart(query[i+1]) {
				i++
			}
			token := query[start : i+1]
			lower := strings.ToLower(token)
			if lower == "key" {
				if _, skip := mysqlKeywordBeforeKey[lastToken]; skip {
					b.WriteString(token)
				} else {
					b.WriteString("`key`")
				}
			} else {
				b.WriteString(token)
			}
			lastToken = lower
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

func isSQLIdentifierStart(ch byte) bool {
	return (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_'
}

func isSQLIdentifierPart(ch byte) bool {
	return isSQLIdentifierStart(ch) || (ch >= '0' && ch <= '9')
}

func rewritePostgresUpsertForMySQL(query string) string {
	lower := strings.ToLower(query)
	if !strings.Contains(lower, "on conflict") {
		return query
	}
	if strings.Contains(lower, "do nothing") {
		query = mysqlDoNothingPattern.ReplaceAllString(query, "")
		return replaceFirstFold(query, "insert into", "INSERT IGNORE INTO")
	}
	for {
		lower = strings.ToLower(query)
		idx := strings.Index(lower, "on conflict")
		if idx < 0 {
			break
		}
		after := lower[idx:]
		updateIdxRel := strings.Index(after, "do update set")
		if updateIdxRel < 0 {
			break
		}
		updateStart := idx + updateIdxRel
		query = query[:idx] + "ON DUPLICATE KEY UPDATE" + query[updateStart+len("do update set"):]
	}
	query = mysqlExcludedValuePattern.ReplaceAllString(query, "VALUES($1)")
	return query
}

func replaceFirstFold(s, old, new string) string {
	idx := strings.Index(strings.ToLower(s), strings.ToLower(old))
	if idx < 0 {
		return s
	}
	return s[:idx] + new + s[idx+len(old):]
}
