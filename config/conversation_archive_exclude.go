package config

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// ConversationArchiveExclude 是会话存档的排除项集合：命中的请求完全不采集
// （不解析正文、不缓存响应、不落库）。
//
// 配置来源 CODEX_CONVERSATION_EXCLUDE，逗号 / 分号 / 空白分隔，四种前缀可混用：
//
//	machine:<installation_id>   Codex 客户端机器码（X-Codex-Turn-Metadata 的 installation_id）
//	client:<client_id>          X-Client-Id 显式客户端标识（也是存档里的 client_id）
//	ip:<ip 或 cidr>             来源 IP / 网段
//	key:<api_key_id>            API Key ID
//
// 不带前缀的取值按机器码处理；本身是合法 IP 时按 IP 处理。
type ConversationArchiveExclude struct {
	Machines []string
	Clients  []string
	IPs      []netip.Prefix
	APIKeys  []int64
}

// Empty 表示没有配置任何排除项。
func (e ConversationArchiveExclude) Empty() bool {
	return len(e.Machines) == 0 && len(e.Clients) == 0 && len(e.IPs) == 0 && len(e.APIKeys) == 0
}

// ParseConversationArchiveExclude 解析 CODEX_CONVERSATION_EXCLUDE。
// 机器码与客户端标识大小写不敏感；IP 支持单地址与 CIDR；key 只接受正整数 ID。
func ParseConversationArchiveExclude(raw string) (ConversationArchiveExclude, error) {
	var out ConversationArchiveExclude
	machines := make(map[string]struct{})
	clients := make(map[string]struct{})
	prefixes := make(map[netip.Prefix]struct{})
	keys := make(map[int64]struct{})

	for _, field := range splitArchiveExcludeFields(raw) {
		kind, value := splitArchiveExcludeToken(field)
		switch kind {
		case "machine":
			if value == "" {
				return out, fmt.Errorf("CODEX_CONVERSATION_EXCLUDE 中的 %q 缺少机器码", field)
			}
			normalized := strings.ToLower(value)
			if _, ok := machines[normalized]; ok {
				continue
			}
			machines[normalized] = struct{}{}
			out.Machines = append(out.Machines, normalized)
		case "client":
			if value == "" {
				return out, fmt.Errorf("CODEX_CONVERSATION_EXCLUDE 中的 %q 缺少客户端标识", field)
			}
			normalized := strings.ToLower(value)
			if _, ok := clients[normalized]; ok {
				continue
			}
			clients[normalized] = struct{}{}
			out.Clients = append(out.Clients, normalized)
		case "ip":
			prefix, err := parseArchiveExcludePrefix(value)
			if err != nil {
				return out, fmt.Errorf("CODEX_CONVERSATION_EXCLUDE 中的 %q 不是合法的 IP 或 CIDR", field)
			}
			if _, ok := prefixes[prefix]; ok {
				continue
			}
			prefixes[prefix] = struct{}{}
			out.IPs = append(out.IPs, prefix)
		case "key":
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil || id <= 0 {
				return out, fmt.Errorf("CODEX_CONVERSATION_EXCLUDE 中的 %q 不是合法的 API Key ID", field)
			}
			if _, ok := keys[id]; ok {
				continue
			}
			keys[id] = struct{}{}
			out.APIKeys = append(out.APIKeys, id)
		}
	}
	return out, nil
}

func splitArchiveExcludeFields(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ';', ' ', '\t', '\n', '\r':
			return true
		default:
			return false
		}
	})
}

// splitArchiveExcludeToken 拆出维度与取值。注意不能简单按冒号切分：
// IPv6 地址本身带冒号，所以只认下面四个显式前缀。
func splitArchiveExcludeToken(token string) (kind, value string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", ""
	}
	lower := strings.ToLower(token)
	for _, prefix := range []string{"machine", "client", "ip", "key"} {
		if !strings.HasPrefix(lower, prefix+":") {
			continue
		}
		return prefix, strings.TrimSpace(token[len(prefix)+1:])
	}
	if _, err := netip.ParseAddr(token); err == nil {
		return "ip", token
	}
	if _, err := netip.ParsePrefix(token); err == nil {
		return "ip", token
	}
	return "machine", token
}

func parseArchiveExcludePrefix(value string) (netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return netip.Prefix{}, fmt.Errorf("empty ip")
	}
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Prefix{}, err
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}
