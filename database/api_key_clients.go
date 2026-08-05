package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

const (
	apiKeyClientFlushInterval = 5 * time.Second
	apiKeyClientBatchSize     = 500
	apiKeyClientPendingLimit  = 20000
	apiKeyClientQueryBatch    = 250
)

type apiKeyClientIdentity struct {
	apiKeyID int64
	hash     string
}

type apiKeyClientSeen struct {
	identity apiKeyClientIdentity
	seenAt   time.Time
}

// APIKeyClientCounts contains the deduplicated historical and active client
// counts for one API key. Historical counts start when this table is enabled.
type APIKeyClientCounts struct {
	Active int64
	Total  int64
}

func apiKeyClientsMySQLDDL() string {
	return `CREATE TABLE IF NOT EXISTS api_key_clients (
		api_key_id BIGINT NOT NULL,
		client_id_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		first_seen_at DATETIME NOT NULL,
		last_seen_at DATETIME NOT NULL,
		PRIMARY KEY (api_key_id, client_id_hash),
		KEY idx_api_key_clients_last_seen (api_key_id, last_seen_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8`
}

func (db *DB) ensureAPIKeyClientsTable(ctx context.Context) error {
	if db == nil || db.conn == nil {
		return nil
	}
	if db.isMySQL() {
		_, err := db.conn.ExecContext(ctx, apiKeyClientsMySQLDDL())
		return err
	}

	timestampType := "TIMESTAMPTZ"
	if db.isSQLite() {
		timestampType = "TIMESTAMP"
	}
	if _, err := db.conn.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS api_key_clients (
		api_key_id BIGINT NOT NULL,
		client_id_hash VARCHAR(64) NOT NULL,
		first_seen_at %s NOT NULL,
		last_seen_at %s NOT NULL,
		PRIMARY KEY (api_key_id, client_id_hash)
	)`, timestampType, timestampType)); err != nil {
		return err
	}
	_, err := db.conn.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_api_key_clients_last_seen
		ON api_key_clients(api_key_id, last_seen_at)`)
	return err
}

func hashAPIKeyClientID(apiKeyID int64, clientID string) string {
	// Scope explicit client identifiers to their API key. This keeps the
	// database value non-reversible and prevents linking the same identifier
	// across otherwise unrelated keys.
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", apiKeyID, clientID)))
	return hex.EncodeToString(sum[:])
}

// RecordAPIKeyClient coalesces duplicate sightings in memory. It never blocks
// the proxy request on database I/O and never stores the raw client identifier.
func (db *DB) RecordAPIKeyClient(apiKeyID int64, clientID string) bool {
	clientID = strings.TrimSpace(clientID)
	if db == nil || apiKeyID <= 0 || clientID == "" {
		return false
	}
	identity := apiKeyClientIdentity{apiKeyID: apiKeyID, hash: hashAPIKeyClientID(apiKeyID, clientID)}
	now := time.Now().UTC()

	db.clientSeenMu.Lock()
	defer db.clientSeenMu.Unlock()
	if db.clientSeenClosing {
		return false
	}
	if _, deleted := db.clientSeenDeleted[apiKeyID]; deleted {
		return false
	}
	if db.clientSeenPending == nil {
		db.clientSeenPending = make(map[apiKeyClientIdentity]time.Time, apiKeyClientBatchSize)
	}
	if previous, ok := db.clientSeenPending[identity]; ok {
		if now.After(previous) {
			db.clientSeenPending[identity] = now
		}
		return true
	}
	if len(db.clientSeenPending) >= apiKeyClientPendingLimit {
		dropped := atomic.AddInt64(&db.clientSeenDropped, 1)
		if dropped == 1 || dropped%1000 == 0 {
			log.Printf("API Key 客户端统计缓冲已满，累计丢弃 %d 个新客户端标识", dropped)
		}
		return false
	}
	db.clientSeenPending[identity] = now
	if len(db.clientSeenPending) >= apiKeyClientBatchSize && db.clientSeenNotify != nil {
		select {
		case db.clientSeenNotify <- struct{}{}:
		default:
		}
	}
	return true
}

func (db *DB) startAPIKeyClientFlusher() {
	if db == nil || db.clientSeenStop == nil {
		return
	}
	db.clientSeenWg.Add(1)
	go func() {
		defer db.clientSeenWg.Done()
		ticker := time.NewTicker(apiKeyClientFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-db.clientSeenStop:
				return
			case <-ticker.C:
			case <-db.clientSeenNotify:
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := db.FlushAPIKeyClients(ctx); err != nil {
				log.Printf("批量写入 API Key 客户端统计失败，已保留等待重试: %v", err)
			}
			cancel()
		}
	}()
}

func (db *DB) stopAPIKeyClientFlusher() {
	if db == nil || db.clientSeenStop == nil {
		return
	}
	db.clientSeenStopOnce.Do(func() {
		db.clientSeenMu.Lock()
		db.clientSeenClosing = true
		db.clientSeenMu.Unlock()
		close(db.clientSeenStop)
	})
	db.clientSeenWg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.FlushAPIKeyClients(ctx); err != nil {
		log.Printf("关闭前写入 API Key 客户端统计失败: %v", err)
	}
}

// FlushAPIKeyClients persists the current deduplicated snapshot. Failed rows
// are merged back into the bounded buffer so a later flush can retry them.
func (db *DB) FlushAPIKeyClients(ctx context.Context) error {
	if db == nil {
		return nil
	}
	db.clientSeenFlushMu.Lock()
	defer db.clientSeenFlushMu.Unlock()

	db.clientSeenMu.Lock()
	if len(db.clientSeenPending) == 0 {
		db.clientSeenMu.Unlock()
		return nil
	}
	snapshot := db.clientSeenPending
	db.clientSeenPending = make(map[apiKeyClientIdentity]time.Time, apiKeyClientBatchSize)
	db.clientSeenMu.Unlock()

	rows := make([]apiKeyClientSeen, 0, len(snapshot))
	for identity, seenAt := range snapshot {
		rows = append(rows, apiKeyClientSeen{identity: identity, seenAt: seenAt})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].identity.apiKeyID != rows[j].identity.apiKeyID {
			return rows[i].identity.apiKeyID < rows[j].identity.apiKeyID
		}
		return rows[i].identity.hash < rows[j].identity.hash
	})

	for start := 0; start < len(rows); start += apiKeyClientBatchSize {
		end := start + apiKeyClientBatchSize
		if end > len(rows) {
			end = len(rows)
		}
		if err := db.insertAPIKeyClientBatch(ctx, rows[start:end]); err != nil {
			db.requeueAPIKeyClients(rows[start:])
			return err
		}
	}
	return nil
}

func (db *DB) requeueAPIKeyClients(rows []apiKeyClientSeen) {
	db.clientSeenMu.Lock()
	defer db.clientSeenMu.Unlock()
	for _, row := range rows {
		if previous, ok := db.clientSeenPending[row.identity]; ok {
			if row.seenAt.After(previous) {
				db.clientSeenPending[row.identity] = row.seenAt
			}
			continue
		}
		if len(db.clientSeenPending) >= apiKeyClientPendingLimit {
			atomic.AddInt64(&db.clientSeenDropped, 1)
			continue
		}
		db.clientSeenPending[row.identity] = row.seenAt
	}
}

// discardPendingAPIKeyClients removes observations that have not reached the
// database yet. DeleteAPIKey calls this after its transaction commits so a
// later background flush cannot recreate client rows for the deleted key.
func (db *DB) discardPendingAPIKeyClients(apiKeyID int64) {
	if db == nil || apiKeyID <= 0 {
		return
	}
	db.clientSeenMu.Lock()
	defer db.clientSeenMu.Unlock()
	if db.clientSeenDeleted == nil {
		db.clientSeenDeleted = make(map[int64]struct{})
	}
	// API key IDs are generated by monotonic sequences/auto-increment columns
	// on every supported database. Remembering the deletion for this process
	// prevents an already-authenticated in-flight request from re-queuing it.
	db.clientSeenDeleted[apiKeyID] = struct{}{}
	for identity := range db.clientSeenPending {
		if identity.apiKeyID == apiKeyID {
			delete(db.clientSeenPending, identity)
		}
	}
}

func (db *DB) insertAPIKeyClientBatch(ctx context.Context, rows []apiKeyClientSeen) error {
	if len(rows) == 0 {
		return nil
	}
	insert := func() error {
		values := make([]string, 0, len(rows))
		args := make([]interface{}, 0, len(rows)*4)
		param := 1
		for _, row := range rows {
			if db.isMySQL() || db.isSQLite() {
				values = append(values, "(?, ?, ?, ?)")
			} else {
				values = append(values, fmt.Sprintf("($%d, $%d, $%d, $%d)", param, param+1, param+2, param+3))
				param += 4
			}
			args = append(args, row.identity.apiKeyID, row.identity.hash, row.seenAt, row.seenAt)
		}
		query := `INSERT INTO api_key_clients (api_key_id, client_id_hash, first_seen_at, last_seen_at) VALUES ` + strings.Join(values, ",")
		switch {
		case db.isMySQL():
			query += ` ON DUPLICATE KEY UPDATE last_seen_at=GREATEST(last_seen_at, VALUES(last_seen_at))`
		case db.isSQLite():
			query += ` ON CONFLICT(api_key_id, client_id_hash) DO UPDATE SET last_seen_at=MAX(api_key_clients.last_seen_at, excluded.last_seen_at)`
		default:
			query += ` ON CONFLICT(api_key_id, client_id_hash) DO UPDATE SET last_seen_at=GREATEST(api_key_clients.last_seen_at, excluded.last_seen_at)`
		}
		_, err := db.conn.ExecContext(ctx, query, args...)
		return err
	}
	return db.withSQLiteWriteLock(ctx, insert)
}

// ListAPIKeyClientCounts returns all-time distinct counts and active counts for
// each key's configured sliding window.
func (db *DB) ListAPIKeyClientCounts(ctx context.Context, windows map[int64]time.Duration) (map[int64]APIKeyClientCounts, error) {
	counts := make(map[int64]APIKeyClientCounts, len(windows))
	ids := make([]int64, 0, len(windows))
	for id := range windows {
		if id > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return counts, nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	for start := 0; start < len(ids); start += apiKeyClientQueryBatch {
		end := start + apiKeyClientQueryBatch
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		if err := db.queryAPIKeyClientTotals(ctx, batch, counts); err != nil {
			return nil, err
		}
		if err := db.queryAPIKeyClientActive(ctx, batch, windows, counts); err != nil {
			return nil, err
		}
	}
	return counts, nil
}

func (db *DB) queryAPIKeyClientTotals(ctx context.Context, ids []int64, counts map[int64]APIKeyClientCounts) error {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		if db.isMySQL() || db.isSQLite() {
			placeholders[i] = "?"
		} else {
			placeholders[i] = fmt.Sprintf("$%d", i+1)
		}
		args[i] = id
	}
	rows, err := db.conn.QueryContext(ctx, `SELECT api_key_id, COUNT(*) FROM api_key_clients WHERE api_key_id IN (`+strings.Join(placeholders, ",")+`) GROUP BY api_key_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, total int64
		if err := rows.Scan(&id, &total); err != nil {
			return err
		}
		item := counts[id]
		item.Total = total
		counts[id] = item
	}
	return rows.Err()
}

func (db *DB) queryAPIKeyClientActive(ctx context.Context, ids []int64, windows map[int64]time.Duration, counts map[int64]APIKeyClientCounts) error {
	conditions := make([]string, 0, len(ids))
	args := make([]interface{}, 0, len(ids)*2)
	now := time.Now().UTC()
	param := 1
	for _, id := range ids {
		window := windows[id]
		if window <= 0 {
			window = 30 * 24 * time.Hour
		}
		if db.isMySQL() || db.isSQLite() {
			conditions = append(conditions, "(api_key_id=? AND last_seen_at>=?)")
		} else {
			conditions = append(conditions, fmt.Sprintf("(api_key_id=$%d AND last_seen_at>=$%d)", param, param+1))
			param += 2
		}
		args = append(args, id, now.Add(-window))
	}
	rows, err := db.conn.QueryContext(ctx, `SELECT api_key_id, COUNT(*) FROM api_key_clients WHERE `+strings.Join(conditions, " OR ")+` GROUP BY api_key_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, active int64
		if err := rows.Scan(&id, &active); err != nil {
			return err
		}
		item := counts[id]
		item.Active = active
		counts[id] = item
	}
	return rows.Err()
}
