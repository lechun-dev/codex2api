package main

// 会话存档保留策略：按天清理历史存档。
//
// 独立后台任务，不阻塞启动；数据库不可用时只记日志、下个周期再试。
// days<=0 表示永久保留，不启动任务。

import (
	"context"
	"log"
	"time"

	"github.com/codex2api/database"
)

const (
	conversationArchiveRetentionInterval = 24 * time.Hour
	conversationArchiveRetentionTimeout  = 10 * time.Minute
)

func StartConversationArchiveRetention(ctx context.Context, db *database.DB, days int) {
	if db == nil || days <= 0 || ctx == nil {
		return
	}
	go func() {
		// 启动后先等一分钟，避开启动高峰。
		initial := time.NewTimer(time.Minute)
		defer initial.Stop()
		ticker := time.NewTicker(conversationArchiveRetentionInterval)
		defer ticker.Stop()

		purge := func() {
			purgeCtx, cancel := context.WithTimeout(ctx, conversationArchiveRetentionTimeout)
			defer cancel()
			cutoff := time.Now().UTC().AddDate(0, 0, -days)
			removed, err := db.PurgeConversationArchivesBefore(purgeCtx, cutoff, 2000)
			if err != nil {
				log.Printf("[conversation-archive] 清理失败: %v", err)
				return
			}
			if removed > 0 {
				log.Printf("[conversation-archive] 已清理 %d 条早于 %s 的存档", removed, cutoff.Format(time.RFC3339))
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-initial.C:
				purge()
			case <-ticker.C:
				purge()
			}
		}
	}()
}
