package proxy

// 会话存档排除项：命中后请求完全不采集——不解析正文、不旁路复制响应、不落库。
//
// 支持的四个维度都来自请求本身，因此判定不需要额外 IO：
//   - machine：Codex 客户端机器码（X-Codex-Turn-Metadata 的 installation_id，
//     其次是 X-Codex-Installation-Id 头与请求体 client_metadata 里的同名字段）
//   - client：X-Client-Id 显式客户端标识，或鉴权后派生的 client_id
//   - ip：来源 IP / 网段（走 ClientIP，已考虑可信代理）
//   - key：API Key ID（只有鉴权之后才知道，所以判定分前后两步）

import (
	"bytes"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/codex2api/config"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// archiveIdentity 是参与排除判定的请求特征。
type archiveIdentity struct {
	MachineID string
	ClientID  string
	ClientIP  string
	APIKeyID  int64
}

// archiveExclusion 是编译后的排除项集合（集合查找，O(1)）。
type archiveExclusion struct {
	machines map[string]struct{}
	clients  map[string]struct{}
	prefixes []netip.Prefix
	keys     map[int64]struct{}
	hitLogAt atomic.Int64
}

// newArchiveExclusion 把配置转成查找结构；没有配置任何排除项时返回 nil，
// 调用方（中间件）因此保持零开销直通。
func newArchiveExclusion(cfg config.ConversationArchiveExclude) *archiveExclusion {
	if cfg.Empty() {
		return nil
	}
	e := &archiveExclusion{
		machines: make(map[string]struct{}, len(cfg.Machines)),
		clients:  make(map[string]struct{}, len(cfg.Clients)),
		prefixes: append([]netip.Prefix(nil), cfg.IPs...),
		keys:     make(map[int64]struct{}, len(cfg.APIKeys)),
	}
	for _, machine := range cfg.Machines {
		e.machines[machine] = struct{}{}
	}
	for _, client := range cfg.Clients {
		e.clients[client] = struct{}{}
	}
	for _, key := range cfg.APIKeys {
		e.keys[key] = struct{}{}
	}
	log.Printf("[conversation-archive] 排除项已启用：机器码 %d 个 / 客户端 %d 个 / IP 网段 %d 个 / API Key %d 个",
		len(e.machines), len(e.clients), len(e.prefixes), len(e.keys))
	return e
}

// match 返回命中的维度名（machine / client / ip / key），未命中返回空串。
func (e *archiveExclusion) match(identity archiveIdentity) string {
	if e == nil {
		return ""
	}
	if e.matchMachine(identity.MachineID) {
		return "machine"
	}
	if e.matchClient(identity.ClientID) {
		return "client"
	}
	if e.matchClientIP(identity.ClientIP) {
		return "ip"
	}
	if e.matchAPIKey(identity.APIKeyID) {
		return "key"
	}
	return ""
}

// needsMachineID 表示配置里是否真的存在机器码维度：只有存在时才去解析请求头/请求体，
// 只配了 ip / key 时不产生额外开销。
func (e *archiveExclusion) needsMachineID() bool {
	return e != nil && len(e.machines) > 0
}

func (e *archiveExclusion) matchMachine(machineID string) bool {
	if e == nil || len(e.machines) == 0 {
		return false
	}
	machineID = strings.ToLower(strings.TrimSpace(machineID))
	if machineID == "" {
		return false
	}
	_, ok := e.machines[machineID]
	return ok
}

func (e *archiveExclusion) matchClient(clientID string) bool {
	if e == nil || len(e.clients) == 0 {
		return false
	}
	clientID = strings.ToLower(strings.TrimSpace(clientID))
	if clientID == "" {
		return false
	}
	_, ok := e.clients[clientID]
	return ok
}

func (e *archiveExclusion) matchClientIP(raw string) bool {
	if e == nil || len(e.prefixes) == 0 {
		return false
	}
	addr, ok := parseArchiveExcludeAddr(raw)
	if !ok {
		return false
	}
	for _, prefix := range e.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (e *archiveExclusion) matchAPIKey(apiKeyID int64) bool {
	if e == nil || len(e.keys) == 0 || apiKeyID <= 0 {
		return false
	}
	_, ok := e.keys[apiKeyID]
	return ok
}

// logHit 限流写日志：命中排除项是正常行为，每分钟最多提示一次，便于确认排除项生效。
func (e *archiveExclusion) logHit(dimension string) {
	if e == nil || dimension == "" {
		return
	}
	now := time.Now().Unix()
	previous := e.hitLogAt.Load()
	if now-previous < 60 {
		return
	}
	if e.hitLogAt.CompareAndSwap(previous, now) {
		log.Printf("[conversation-archive] 命中排除项（%s），本次请求不归档", dimension)
	}
}

func parseArchiveExcludeAddr(raw string) (netip.Addr, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return netip.Addr{}, false
	}
	if addr, err := netip.ParseAddr(raw); err == nil {
		return addr.Unmap(), true
	}
	// ClientIP 在不可信代理场景下可能带端口。
	if host, _, err := net.SplitHostPort(raw); err == nil {
		if addr, err := netip.ParseAddr(host); err == nil {
			return addr.Unmap(), true
		}
	}
	return netip.Addr{}, false
}

// archiveExclusionClientIP 取归一化后的来源 IP（优先使用可信代理解析结果）。
func archiveExclusionClientIP(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if hint := normalizeClientNetworkHint(c); hint != "" {
		return hint
	}
	return strings.TrimSpace(c.ClientIP())
}

// archiveRequestMachineID 取客户端机器码。Codex 客户端把它放在 turn metadata 里，
// 部分路径放在同名请求头或请求体 client_metadata 中。
func archiveRequestMachineID(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}
	if raw := strings.TrimSpace(c.GetHeader(codexTurnMetadataHeader)); raw != "" && gjson.Valid(raw) {
		for _, path := range []string{"installation_id", "x-codex-installation-id"} {
			if id := strings.TrimSpace(gjson.Get(raw, path).String()); id != "" {
				return strings.ToLower(id)
			}
		}
	}
	if id := strings.TrimSpace(c.GetHeader(codexInstallationIDHeader)); id != "" {
		return strings.ToLower(id)
	}
	// 只有请求体里出现 client_metadata 才值得让 gjson 走一遍，避免纯 IP / Key
	// 排除时为每个请求白扫一次大请求体。
	if len(body) == 0 || !bytes.Contains(body, []byte("client_metadata")) {
		return ""
	}
	for _, path := range []string{"client_metadata.x-codex-installation-id", "client_metadata.installation_id"} {
		if id := strings.TrimSpace(gjson.GetBytes(body, path).String()); id != "" {
			return strings.ToLower(id)
		}
	}
	return ""
}
