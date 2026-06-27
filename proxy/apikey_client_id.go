package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/netip"
	"regexp"
	"strings"

	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

const derivedAPIKeyClientIDPrefix = "auto:"

var clientFingerprintNumberPattern = regexp.MustCompile(`\d+(?:\.\d+)*`)

func resolveAPIKeyClientID(c *gin.Context, row *database.APIKeyRow) (clientID string, explicit bool) {
	if c == nil {
		return "", false
	}
	clientID = strings.TrimSpace(c.GetHeader("X-Client-Id"))
	if clientID != "" {
		return clientID, true
	}
	return deriveAPIKeyClientID(c, row), false
}

func deriveAPIKeyClientID(c *gin.Context, row *database.APIKeyRow) string {
	if c == nil {
		return ""
	}
	key := ""
	if row != nil {
		key = strings.TrimSpace(row.Key)
	}
	headers := requestHeaders(c)
	originator := normalizeClientFingerprintComponent(headers.Get("Originator"))
	version := normalizeClientFingerprintUserAgent(headers.Get("Version"))
	packageVersion := normalizeClientFingerprintUserAgent(headers.Get("X-Stainless-Package-Version"))
	runtimeVersion := normalizeClientFingerprintUserAgent(headers.Get("X-Stainless-Runtime-Version"))
	os := normalizeClientFingerprintComponent(headers.Get("X-Stainless-Os"))
	arch := normalizeClientFingerprintComponent(headers.Get("X-Stainless-Arch"))
	userAgent := normalizeClientFingerprintUserAgent(headers.Get("User-Agent"))
	networkHint := normalizeClientNetworkHint(c)

	if key == "" && originator == "" && version == "" && packageVersion == "" && runtimeVersion == "" && os == "" && arch == "" && userAgent == "" && networkHint == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(strings.Join([]string{
		key,
		userAgent,
		version,
		packageVersion,
		runtimeVersion,
		os,
		arch,
		originator,
		networkHint,
	}, "|")))
	return derivedAPIKeyClientIDPrefix + hex.EncodeToString(sum[:16])
}

func requestHeaders(c *gin.Context) http.Header {
	if c == nil || c.Request == nil || c.Request.Header == nil {
		return http.Header{}
	}
	return c.Request.Header
}

func normalizeClientFingerprintUserAgent(raw string) string {
	raw = normalizeClientFingerprintComponent(raw)
	if raw == "" {
		return ""
	}
	return clientFingerprintNumberPattern.ReplaceAllString(raw, "#")
}

func normalizeClientFingerprintComponent(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	return strings.Join(strings.Fields(raw), " ")
}

func normalizeClientNetworkHint(c *gin.Context) string {
	if c == nil {
		return ""
	}
	clientIP := strings.TrimSpace(c.ClientIP())
	if clientIP == "" && c.Request != nil {
		clientIP = strings.TrimSpace(c.Request.RemoteAddr)
	}
	if clientIP == "" {
		return ""
	}
	addr, err := netip.ParseAddrPort(clientIP)
	if err == nil {
		return addr.Addr().String()
	}
	addrOnly, err := netip.ParseAddr(clientIP)
	if err != nil {
		return ""
	}
	return addrOnly.String()
}
