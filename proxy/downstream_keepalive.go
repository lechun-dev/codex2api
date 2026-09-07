package proxy

import (
	"context"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	// 普通 Responses SSE 在上游长时间只思考、不产出可转发事件时，也要持续
	// 刷新下游链路的 idle timer。10 秒低于常见的 30/60 秒反代超时，同时
	// 2026-09-07 coder(lq): 每分钟仅增加少量字节；按客户端类型选择注释或完整事件。
	defaultDownstreamSSEKeepaliveInterval = 10 * time.Second
	downstreamSSEKeepaliveComment         = ": keepalive\n\n"
	downstreamSSEKeepaliveEvent           = "event: codex.keepalive\ndata: {\"type\":\"codex.keepalive\"}\n\n"
)

// 变量形式只为处理器级测试缩短等待；生产运行保持默认 10 秒。
var downstreamSSEKeepaliveInterval = defaultDownstreamSSEKeepaliveInterval

// 2026-09-07 coder(lq): Codex 客户端不会用 SSE 注释刷新事件空闲计时器，
// 因此仅在官方客户端的 Responses 入口发送完整事件；其他兼容客户端保持注释。
func downstreamSSEKeepaliveFrameForRequest(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return downstreamSSEKeepaliveComment
	}
	path := c.Request.URL.Path
	if path != "/responses" && path != "/v1/responses" {
		return downstreamSSEKeepaliveComment
	}
	if !IsCodexStrictOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("Originator")) {
		return downstreamSSEKeepaliveComment
	}
	return downstreamSSEKeepaliveEvent
}

// startDownstreamSSEKeepalive 周期执行 writeKeepalive，直到请求取消、写失败
// 或调用 stop。stop 会等待 goroutine 完整退出，保证流收尾后不再并发写入。
func startDownstreamSSEKeepalive(ctx context.Context, interval time.Duration, writeKeepalive func() bool) func() {
	if interval <= 0 || writeKeepalive == nil {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	stopCh := make(chan struct{})
	done := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if !writeKeepalive() {
					return
				}
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			}
		}
	}()

	return func() {
		stopOnce.Do(func() {
			close(stopCh)
			<-done
		})
	}
}
