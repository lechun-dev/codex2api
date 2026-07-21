package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// UpstreamGrok 标记 Grok CLI 上游账号（upstream_type 凭据字段取值）。
// 凭据形态有两种：
//   - OAuth：access_token + refresh_token（+ client_id），走 chat-proxy 上游，可自动刷新；
//   - API Key：api_key（xai-...），走官方 API 上游，永不过期。
const UpstreamGrok = "grok"

const (
	GrokAuthKindOAuth  = "oauth"
	GrokAuthKindAPIKey = "api_key"
)

// Grok 上游默认端点：OAuth 凭据走 Grok CLI 的 chat-proxy，API Key 走官方 xAI API。
// base_url 凭据字段可覆盖（留空 = 按凭据类型自动选择）。
const (
	GrokDefaultChatProxyBaseURL = "https://cli-chat-proxy.grok.com/v1"
	GrokDefaultAPIBaseURL       = "https://api.x.ai/v1"
	GrokDefaultOIDCIssuer       = "https://auth.x.ai"
)

func (a *Account) isGrokAPILocked() bool {
	if a == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(a.UpstreamType), UpstreamGrok) {
		return false
	}
	return strings.TrimSpace(a.APIKey) != "" ||
		strings.TrimSpace(a.AccessToken) != "" ||
		strings.TrimSpace(a.RefreshToken) != ""
}

// IsGrokAPI 判断账号是否为 Grok 上游账号。
func (a *Account) IsGrokAPI() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.isGrokAPILocked()
}

// isRelayStyleLocked：openai_responses 中转或 Grok —— 一切「非 Codex OAuth 官方上游」
// 的账号。这类账号不参与 Codex 专属行为（wham 探针、WS 上游、manifest、alpha search）。
func (a *Account) isRelayStyleLocked() bool {
	return a.isOpenAIResponsesAPILocked() || a.isGrokAPILocked()
}

// IsRelayStyle 判断账号是否为「非 Codex 官方」的外部上游账号（中转或 Grok）。
func (a *Account) IsRelayStyle() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.isRelayStyleLocked()
}

// GrokAuthKind 返回 Grok 账号的凭据类型（api_key / oauth）；非 Grok 账号返回空。
func (a *Account) GrokAuthKind() string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.isGrokAPILocked() {
		return ""
	}
	if strings.TrimSpace(a.APIKey) != "" {
		return GrokAuthKindAPIKey
	}
	return GrokAuthKindOAuth
}

// GrokCredentials 返回 Grok 账号的上游 base URL 与 Bearer 凭据。
// base_url 未配置时按凭据类型选默认端点。bearer 为空表示 OAuth 账号尚未刷出 AT。
func (a *Account) GrokCredentials() (baseURL, bearer string) {
	if a == nil {
		return "", ""
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.isGrokAPILocked() {
		return "", ""
	}
	baseURL = strings.TrimRight(strings.TrimSpace(a.BaseURL), "/")
	if apiKey := strings.TrimSpace(a.APIKey); apiKey != "" {
		if baseURL == "" {
			baseURL = GrokDefaultAPIBaseURL
		}
		return baseURL, apiKey
	}
	if baseURL == "" {
		baseURL = GrokDefaultChatProxyBaseURL
	}
	return baseURL, strings.TrimSpace(a.AccessToken)
}

// GrokUserID 返回 Grok 账号的上游用户标识（JWT sub，导入时存入 account_id）。
func (a *Account) GrokUserID() string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.isGrokAPILocked() {
		return ""
	}
	return strings.TrimSpace(a.AccountID)
}

// NormalizeGrokBaseURL 校验 Grok 账号的 base_url 覆盖值；空串合法（自动选默认端点）。
func NormalizeGrokBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("base_url 必须是完整的 http/https URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("base_url 仅支持 http/https")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

// ==================== auth.json 导入解析 ====================

// GrokImportedCredential 是从 Grok CLI auth.json 解析出的一条逻辑凭据。
type GrokImportedCredential struct {
	AccessToken   string
	RefreshToken  string
	APIKey        string
	ClientID      string
	TokenEndpoint string
	OIDCIssuer    string
	Subject       string
	PrincipalType string
	PrincipalID   string
	ExpiresAt     time.Time
}

// AuthKind 返回该凭据的类型（api_key / oauth）。
func (c *GrokImportedCredential) AuthKind() string {
	if c != nil && strings.TrimSpace(c.APIKey) != "" {
		return GrokAuthKindAPIKey
	}
	return GrokAuthKindOAuth
}

// ParseGrokAuthJSON 解析 Grok CLI 的 auth.json。兼容三种布局：
// 顶层单凭据、tokens 包装、多 scope（一个文件含多条逻辑凭据，全部返回）。
// scope 为 xai::api_key 的条目按 API Key 凭据处理。
func ParseGrokAuthJSON(raw []byte) ([]*GrokImportedCredential, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("auth.json 不是合法的 JSON: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("auth.json 必须是 JSON 对象")
	}

	type candidate struct {
		scope string
		node  map[string]any
	}
	isCredNode := func(node map[string]any) bool {
		return grokFirstString(node, "access_token", "AccessToken", "refresh_token", "RefreshToken", "key", "session_token", "SessionToken") != ""
	}
	var candidates []candidate
	if isCredNode(root) {
		candidates = append(candidates, candidate{node: root})
	} else {
		container := root
		if tokens, ok := root["tokens"].(map[string]any); ok {
			if isCredNode(tokens) {
				candidates = append(candidates, candidate{node: tokens})
			} else {
				container = tokens
			}
		}
		if len(candidates) == 0 {
			for scope, value := range container {
				if node, ok := value.(map[string]any); ok && isCredNode(node) {
					candidates = append(candidates, candidate{scope: scope, node: node})
				}
			}
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("auth.json 中没有可用凭据（缺少 access_token/refresh_token/key）")
	}

	result := make([]*GrokImportedCredential, 0, len(candidates))
	for _, cand := range candidates {
		cred, err := parseGrokCredentialNode(cand.scope, cand.node)
		if err != nil {
			return nil, err
		}
		result = append(result, cred)
	}
	return result, nil
}

func parseGrokCredentialNode(scope string, node map[string]any) (*GrokImportedCredential, error) {
	access := grokFirstString(node, "access_token", "AccessToken", "key", "session_token", "SessionToken")
	refresh := grokFirstString(node, "refresh_token", "RefreshToken")
	if access == "" && refresh == "" {
		return nil, fmt.Errorf("凭据缺少 access_token 和 refresh_token")
	}

	scopeNorm := strings.ToLower(strings.TrimSpace(scope))
	authMode := strings.ToLower(grokFirstString(node, "auth_mode", "authMode"))
	isAPIKey := authMode == "api_key" || authMode == "apikey" || authMode == "api-key" ||
		strings.Contains(scopeNorm, "api_key")

	claims := grokJWTClaims(access)
	cred := &GrokImportedCredential{
		AccessToken:   access,
		RefreshToken:  refresh,
		ClientID:      grokFirstString(node, "client_id", "clientId", "oidc_client_id", "oidcClientId"),
		TokenEndpoint: grokFirstString(node, "token_endpoint", "tokenEndpoint"),
		OIDCIssuer:    strings.TrimRight(grokFirstString(node, "oidc_issuer", "oidcIssuer", "issuer"), "/"),
		PrincipalType: grokFirstString(node, "principal_type", "principalType"),
		PrincipalID:   grokFirstString(node, "principal_id", "principalId", "team_id", "teamId"),
	}
	if isAPIKey {
		cred.APIKey = access
		cred.AccessToken = ""
		cred.RefreshToken = ""
		return cred, nil
	}

	cred.Subject = grokFirstString(node, "user_id", "userId", "UserId", "sub")
	if cred.Subject == "" && claims != nil {
		cred.Subject = grokClaimString(claims, "sub")
	}
	if cred.PrincipalID == "" && claims != nil {
		cred.PrincipalID = grokClaimString(claims, "principal_id")
	}
	if cred.Subject == "" {
		cred.Subject = cred.PrincipalID
	}
	if cred.ClientID == "" && claims != nil {
		cred.ClientID = grokClaimString(claims, "client_id")
	}
	if expires := grokFirstString(node, "expired", "expires_at", "ExpiresAt", "expiry", "expiration"); expires != "" {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if t, err := time.Parse(layout, expires); err == nil {
				cred.ExpiresAt = t
				break
			}
		}
	}
	if cred.ExpiresAt.IsZero() && claims != nil {
		if exp, ok := claims["exp"].(float64); ok && exp > 0 {
			cred.ExpiresAt = time.Unix(int64(exp), 0)
		}
	}
	if refresh == "" {
		return nil, fmt.Errorf("OAuth 凭据缺少 refresh_token，无法长期使用（如为 API Key 请设置 auth_mode=api_key）")
	}
	return cred, nil
}

func grokFirstString(node map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := node[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// grokJWTClaims 解析 JWT payload（不验签），非 JWT 返回 nil。
func grokJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		if decoded, err = base64.StdEncoding.DecodeString(payload); err != nil {
			return nil
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil
	}
	return claims
}

func grokClaimString(claims map[string]any, key string) string {
	if value, ok := claims[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

// ==================== OAuth 刷新 ====================

// GrokTokenData 是一次 Grok OAuth 刷新的结果。
type GrokTokenData struct {
	AccessToken  string
	RefreshToken string // 上游轮换时非空
	IDToken      string
	ExpiresAt    time.Time
}

// grokDiscoveryCache 缓存 OIDC discovery 出的 token_endpoint（1 小时）。
var grokDiscoveryCache = struct {
	sync.RWMutex
	entries map[string]struct {
		endpoint string
		at       time.Time
	}
}{entries: make(map[string]struct {
	endpoint string
	at       time.Time
})}

func grokAllowedTokenEndpoint(u *url.URL) bool {
	return u != nil && u.Hostname() != "" && u.Scheme == "https"
}

func grokResolveTokenEndpoint(ctx context.Context, client *http.Client, tokenURL, issuer string) (string, error) {
	if tokenURL = strings.TrimSpace(tokenURL); tokenURL != "" {
		parsed, err := url.Parse(tokenURL)
		if err != nil || !grokAllowedTokenEndpoint(parsed) {
			return "", fmt.Errorf("grok token_endpoint 无效: %s", tokenURL)
		}
		return parsed.String(), nil
	}
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	if issuer == "" {
		issuer = GrokDefaultOIDCIssuer
	}
	grokDiscoveryCache.RLock()
	cached, ok := grokDiscoveryCache.entries[issuer]
	grokDiscoveryCache.RUnlock()
	if ok && time.Since(cached.at) < time.Hour {
		return cached.endpoint, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("grok OIDC discovery 请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("grok OIDC discovery 失败 (status=%d)", resp.StatusCode)
	}
	var document struct {
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return "", fmt.Errorf("grok OIDC discovery 响应解析失败: %w", err)
	}
	endpointURL, parseErr := url.Parse(document.TokenEndpoint)
	if document.TokenEndpoint == "" || parseErr != nil || !grokAllowedTokenEndpoint(endpointURL) {
		return "", fmt.Errorf("grok OIDC discovery 未返回可用的 token_endpoint")
	}
	grokDiscoveryCache.Lock()
	grokDiscoveryCache.entries[issuer] = struct {
		endpoint string
		at       time.Time
	}{endpoint: document.TokenEndpoint, at: time.Now()}
	grokDiscoveryCache.Unlock()
	return document.TokenEndpoint, nil
}

// GrokRefreshParams 描述一次 Grok OAuth refresh_token 交换所需的全部字段。
type GrokRefreshParams struct {
	RefreshToken  string
	ClientID      string
	TokenEndpoint string
	OIDCIssuer    string
	PrincipalType string
	PrincipalID   string
	ProxyURL      string
}

// grokRefreshPermanentError 标记不可重试的刷新失败（invalid_grant / invalid_client），
// 账号应转入 error 状态而非退避重试。
type grokRefreshPermanentError struct{ code string }

func (e *grokRefreshPermanentError) Error() string {
	return "grok OAuth 刷新永久失败: " + e.code
}

// IsGrokRefreshPermanentError 判断刷新错误是否为永久失败（RT 已失效）。
func IsGrokRefreshPermanentError(err error) bool {
	var permanent *grokRefreshPermanentError
	return errors.As(err, &permanent)
}

// RefreshGrokAccessToken 用 refresh_token 交换新的 Grok access_token。
func RefreshGrokAccessToken(ctx context.Context, params GrokRefreshParams) (*GrokTokenData, error) {
	if strings.TrimSpace(params.RefreshToken) == "" {
		return nil, fmt.Errorf("grok refresh_token 为空")
	}
	if strings.TrimSpace(params.ClientID) == "" {
		return nil, fmt.Errorf("grok client_id 为空，无法刷新")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if err := ConfigureTransportProxy(transport, params.ProxyURL, nil); err != nil {
		return nil, fmt.Errorf("grok 刷新代理配置失败: %w", err)
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}

	endpoint, err := grokResolveTokenEndpoint(ctx, client, params.TokenEndpoint, params.OIDCIssuer)
	if err != nil {
		return nil, err
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {params.RefreshToken},
		"client_id":     {params.ClientID},
	}
	if params.PrincipalType != "" {
		form.Set("principal_type", params.PrincipalType)
	}
	if params.PrincipalID != "" {
		form.Set("principal_id", params.PrincipalID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("grok token 刷新请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("grok token 刷新响应读取失败: %w", err)
	}

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		Code         string `json:"code"`
	}
	_ = json.Unmarshal(body, &payload)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code := strings.ToLower(firstNonEmptyTrimmed(payload.Error, payload.Code))
		if code == "invalid_grant" || code == "invalid_client" {
			return nil, &grokRefreshPermanentError{code: code}
		}
		if code == "" {
			code = fmt.Sprintf("status_%d", resp.StatusCode)
		}
		return nil, fmt.Errorf("grok token 刷新失败: %s (status=%d)", code, resp.StatusCode)
	}
	if payload.AccessToken == "" {
		return nil, fmt.Errorf("grok token 刷新响应缺少 access_token")
	}

	expiresIn := payload.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 21600
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)
	if claims := grokJWTClaims(payload.AccessToken); claims != nil {
		if exp, ok := claims["exp"].(float64); ok && exp > 0 {
			expiresAt = time.Unix(int64(exp), 0)
		}
	}
	return &GrokTokenData{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		IDToken:      payload.IDToken,
		ExpiresAt:    expiresAt,
	}, nil
}

// refreshGrokAccount 刷新 Grok OAuth 账号的 AT。API Key 账号无需刷新，直接返回。
// 与 Codex 刷新共用 tokenCache 的跨进程刷新锁，避免多副本重复消费 refresh token
// （Grok 的 RT 家族会轮换，重复消费会导致 invalid_grant）。
func (s *Store) refreshGrokAccount(ctx context.Context, acc *Account, forceRefresh bool) error {
	acc.mu.RLock()
	authKindIsAPIKey := strings.TrimSpace(acc.APIKey) != ""
	rt := acc.RefreshToken
	dbID := acc.DBID
	clientID := acc.GrokClientID
	tokenEndpoint := acc.GrokTokenEndpoint
	oidcIssuer := acc.GrokOIDCIssuer
	principalType := acc.GrokPrincipalType
	principalID := acc.GrokPrincipalID
	cooldownUntil := acc.CooldownUtil
	cooldownReason := acc.CooldownReason
	activeCooldown := acc.Status == StatusCooldown && time.Now().Before(acc.CooldownUtil)
	acc.mu.RUnlock()

	if authKindIsAPIKey {
		return nil
	}
	if strings.TrimSpace(rt) == "" {
		return fmt.Errorf("grok refresh_token 为空")
	}

	// 跨进程刷新锁（复用 Codex 的 tokenCache 锁语义）
	if s.tokenCache != nil {
		acquired, lockErr := s.tokenCache.AcquireRefreshLock(ctx, dbID, 30*time.Second)
		if lockErr != nil {
			log.Printf("[账号 %d] 获取 grok 刷新锁失败: %v", dbID, lockErr)
		}
		if !acquired && lockErr == nil {
			token, waitErr := s.tokenCache.WaitForRefreshComplete(ctx, dbID, 30*time.Second)
			if !forceRefresh && waitErr == nil && token != "" {
				acc.mu.Lock()
				acc.AccessToken = token
				if expiresAt := grokAccessTokenExpiry(token); !expiresAt.IsZero() {
					acc.ExpiresAt = expiresAt
				} else {
					acc.ExpiresAt = time.Now().Add(30 * time.Minute)
				}
				if !activeCooldown {
					acc.Status = StatusReady
				}
				acc.recomputeSchedulerLocked(atomic.LoadInt64(&s.maxConcurrency))
				acc.mu.Unlock()
				s.fastSchedulerUpdate(acc)
				return nil
			}
			if !forceRefresh {
				return fmt.Errorf("账号 %d 正在刷新，请稍后重试", dbID)
			}
		}
		if acquired {
			defer s.tokenCache.ReleaseRefreshLock(ctx, dbID)
		}
	}

	td, err := RefreshGrokAccessToken(ctx, GrokRefreshParams{
		RefreshToken:  rt,
		ClientID:      clientID,
		TokenEndpoint: tokenEndpoint,
		OIDCIssuer:    oidcIssuer,
		PrincipalType: principalType,
		PrincipalID:   principalID,
		ProxyURL:      s.ResolveProxyForAccount(acc),
	})
	if err != nil {
		if IsGrokRefreshPermanentError(err) {
			acc.mu.Lock()
			acc.Status = StatusError
			acc.ErrorMsg = err.Error()
			acc.mu.Unlock()
			s.fastSchedulerUpdate(acc)
			if s.db != nil {
				_ = s.db.SetError(ctx, dbID, err.Error())
			}
		}
		return err
	}

	acc.mu.Lock()
	acc.AccessToken = td.AccessToken
	if td.RefreshToken != "" {
		acc.RefreshToken = td.RefreshToken
	}
	acc.ExpiresAt = td.ExpiresAt
	acc.ErrorMsg = ""
	if activeCooldown {
		acc.Status = StatusCooldown
		acc.CooldownUtil = cooldownUntil
		acc.CooldownReason = cooldownReason
	} else {
		acc.Status = StatusReady
		acc.CooldownUtil = time.Time{}
		acc.CooldownReason = ""
	}
	if acc.HealthTier != HealthTierBanned {
		acc.HealthTier = HealthTierHealthy
	}
	acc.recomputeSchedulerLocked(atomic.LoadInt64(&s.maxConcurrency))
	acc.mu.Unlock()
	s.fastSchedulerUpdate(acc)

	if s.tokenCache != nil {
		if ttl := time.Until(td.ExpiresAt) - 5*time.Minute; ttl > 0 {
			_ = s.tokenCache.SetAccessToken(ctx, dbID, td.AccessToken, ttl)
		}
	}

	if s.db != nil {
		credentials := map[string]interface{}{
			"access_token": td.AccessToken,
			"expires_at":   td.ExpiresAt.Format(time.RFC3339),
		}
		if td.RefreshToken != "" {
			credentials["refresh_token"] = td.RefreshToken
		}
		if td.IDToken != "" {
			credentials["id_token"] = td.IDToken
		}
		if err := s.db.UpdateCredentials(ctx, dbID, credentials); err != nil {
			log.Printf("[账号 %d] grok 刷新后写库失败: %v", dbID, err)
		} else {
			_ = s.db.ClearError(ctx, dbID)
		}
	}
	return nil
}

func grokAccessTokenExpiry(token string) time.Time {
	if claims := grokJWTClaims(token); claims != nil {
		if exp, ok := claims["exp"].(float64); ok && exp > 0 {
			return time.Unix(int64(exp), 0)
		}
	}
	return time.Time{}
}

// ApplyGrokConfig 热更新运行时 Grok 账号的可编辑配置（凭据留空 = 不改）。
func (s *Store) ApplyGrokConfig(dbID int64, baseURL, apiKey string, models []string, modelMapping, proxyURL string) bool {
	acc := s.FindByID(dbID)
	if acc == nil {
		return false
	}
	acc.mu.Lock()
	acc.UpstreamType = UpstreamGrok
	acc.BaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.TrimSpace(apiKey) != "" {
		acc.APIKey = strings.TrimSpace(apiKey)
	}
	acc.Models = normalizeModelList(models)
	acc.ModelMapping = strings.TrimSpace(modelMapping)
	acc.ProxyURL = strings.TrimSpace(proxyURL)
	if acc.Status != StatusError {
		acc.HealthTier = HealthTierHealthy
	}
	acc.recomputeSchedulerLocked(atomic.LoadInt64(&s.maxConcurrency))
	acc.mu.Unlock()
	s.fastSchedulerUpdate(acc)
	return true
}
