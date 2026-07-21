package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/codex2api/auth"
	"github.com/codex2api/proxy"
	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
)

// addGrokAccountReq 是新增/更新 Grok 账号的请求体。
// AuthKind 取 api_key / oauth；oauth 时 AuthJSON 为 Grok CLI auth.json 原文，
// api_key 时 APIKey 为 xAI API Key。
type addGrokAccountReq struct {
	Name         string   `json:"name"`
	AuthKind     string   `json:"auth_kind"`
	AuthJSON     string   `json:"auth_json"`
	APIKey       string   `json:"api_key"`
	BaseURL      string   `json:"base_url"`
	Models       []string `json:"models"`
	ModelMapping string   `json:"model_mapping"`
	ProxyURL     string   `json:"proxy_url"`
}

// grokCredentialsFromRequest 校验请求并构造待入库的 credentials map。
// 返回的 email 用于列表展示（OAuth 取 subject，API Key 取脱敏 key）。
func grokCredentialsFromRequest(req *addGrokAccountReq) (map[string]interface{}, string, error) {
	baseURL, err := auth.NormalizeGrokBaseURL(req.BaseURL)
	if err != nil {
		return nil, "", err
	}
	credentials := map[string]interface{}{
		"upstream_type": auth.UpstreamGrok,
		"plan_type":     "api",
	}
	if baseURL != "" {
		credentials["base_url"] = baseURL
	}

	authKind := strings.ToLower(strings.TrimSpace(req.AuthKind))
	email := ""
	switch authKind {
	case auth.GrokAuthKindAPIKey:
		apiKey := strings.TrimSpace(req.APIKey)
		if apiKey == "" {
			return nil, "", fmt.Errorf("API Key 是必填字段")
		}
		credentials["api_key"] = apiKey
		email = "xai-api-key"
	case auth.GrokAuthKindOAuth, "":
		creds, err := auth.ParseGrokAuthJSON([]byte(req.AuthJSON))
		if err != nil {
			return nil, "", fmt.Errorf("解析 auth.json 失败: %w", err)
		}
		// 取第一条可用凭据（多 scope 文件通常首条即目标账号）
		cred := creds[0]
		if cred.AuthKind() == auth.GrokAuthKindAPIKey {
			credentials["api_key"] = cred.APIKey
			email = "xai-api-key"
			break
		}
		if strings.TrimSpace(cred.RefreshToken) == "" {
			return nil, "", fmt.Errorf("auth.json 中的 OAuth 凭据缺少 refresh_token")
		}
		if strings.TrimSpace(cred.ClientID) == "" {
			return nil, "", fmt.Errorf("auth.json 中的 OAuth 凭据缺少 client_id，无法刷新")
		}
		credentials["refresh_token"] = cred.RefreshToken
		credentials["grok_client_id"] = cred.ClientID
		if cred.AccessToken != "" {
			credentials["access_token"] = cred.AccessToken
		}
		if !cred.ExpiresAt.IsZero() {
			credentials["expires_at"] = cred.ExpiresAt.Format(time.RFC3339)
		}
		if cred.TokenEndpoint != "" {
			credentials["grok_token_endpoint"] = cred.TokenEndpoint
		}
		if cred.OIDCIssuer != "" {
			credentials["grok_oidc_issuer"] = cred.OIDCIssuer
		}
		if cred.PrincipalType != "" {
			credentials["grok_principal_type"] = cred.PrincipalType
		}
		if cred.PrincipalID != "" {
			credentials["grok_principal_id"] = cred.PrincipalID
		}
		if cred.Subject != "" {
			credentials["account_id"] = cred.Subject
			email = cred.Subject
		}
	default:
		return nil, "", fmt.Errorf("auth_kind 必须是 oauth 或 api_key")
	}

	models := auth.NormalizeAccountModels(req.Models)
	for _, model := range models {
		if err := security.ValidateModelName(model); err != nil {
			return nil, "", fmt.Errorf("模型名称无效: %s", model)
		}
	}
	if len(models) > 0 {
		credentials["models"] = models
	}
	modelMapping, err := normalizeAccountModelMapping(req.ModelMapping)
	if err != nil {
		return nil, "", err
	}
	if modelMapping != "" {
		credentials["model_mapping"] = modelMapping
	}
	if email != "" {
		credentials["email"] = email
	}
	return credentials, email, nil
}

// AddGrokAccount 新增一个 Grok 上游账号（POST /api/admin/accounts/grok）。
func (h *Handler) AddGrokAccount(c *gin.Context) {
	var req addGrokAccountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	req.Name = security.SanitizeInput(req.Name)
	req.ProxyURL = security.SanitizeInput(req.ProxyURL)

	if security.ContainsXSS(req.Name) || security.ContainsSQLInjection(req.Name) {
		writeError(c, http.StatusBadRequest, "名称包含非法字符")
		return
	}
	if utf8.RuneCountInString(req.Name) > 100 {
		writeError(c, http.StatusBadRequest, "名称长度不能超过100字符")
		return
	}
	if err := security.ValidateProxyURL(req.ProxyURL); err != nil {
		writeError(c, http.StatusBadRequest, "代理URL无效")
		return
	}

	credentials, email, err := grokCredentialsFromRequest(&req)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}

	name := req.Name
	if name == "" {
		name = "grok"
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	id, err := h.db.InsertAccountWithUpstream(ctx, name, "xai", auth.UpstreamGrok, credentials, req.ProxyURL)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	h.db.InsertAccountEventAsync(id, "added", "manual_grok")

	models := auth.NormalizeAccountModels(req.Models)
	acc := &auth.Account{
		DBID:              id,
		ProxyURL:          req.ProxyURL,
		HealthTier:        auth.HealthTierHealthy,
		UpstreamType:      auth.UpstreamGrok,
		BaseURL:           strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"),
		Models:            models,
		ModelMapping:      strings.TrimSpace(req.ModelMapping),
		Email:             email,
		PlanType:          "api",
		GrokClientID:      credentialStringValue(credentials, "grok_client_id"),
		GrokTokenEndpoint: credentialStringValue(credentials, "grok_token_endpoint"),
		GrokOIDCIssuer:    credentialStringValue(credentials, "grok_oidc_issuer"),
		GrokPrincipalType: credentialStringValue(credentials, "grok_principal_type"),
		GrokPrincipalID:   credentialStringValue(credentials, "grok_principal_id"),
		AccountID:         credentialStringValue(credentials, "account_id"),
	}
	acc.APIKey = credentialStringValue(credentials, "api_key")
	acc.AccessToken = credentialStringValue(credentials, "access_token")
	acc.RefreshToken = credentialStringValue(credentials, "refresh_token")
	h.store.AddAccount(acc)

	security.SecurityAuditLog("GROK_ACCOUNT_ADDED", fmt.Sprintf("account_id=%d auth_kind=%s models=%d ip=%s", id, acc.GrokAuthKind(), len(models), c.ClientIP()))
	c.JSON(http.StatusOK, gin.H{"message": "成功添加 Grok 账号", "id": id})
}

// UpdateGrokAccount 更新 Grok 账号的可编辑配置（PATCH /api/admin/accounts/:id/grok）。
// 仅更新 base_url / models / model_mapping / proxy / api_key（凭据留空则不改）。
func (h *Handler) UpdateGrokAccount(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "无效的账号 ID")
		return
	}
	var req addGrokAccountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	req.Name = security.SanitizeInput(req.Name)
	req.ProxyURL = security.SanitizeInput(req.ProxyURL)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	row, err := h.db.GetAccountByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(c, http.StatusNotFound, "账号不存在")
			return
		}
		writeInternalError(c, err)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(row.GetCredential("upstream_type")), auth.UpstreamGrok) {
		writeError(c, http.StatusBadRequest, "仅 Grok 账号支持该设置")
		return
	}

	baseURL, err := auth.NormalizeGrokBaseURL(req.BaseURL)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := security.ValidateProxyURL(req.ProxyURL); err != nil {
		writeError(c, http.StatusBadRequest, "代理URL无效")
		return
	}
	models := auth.NormalizeAccountModels(req.Models)
	for _, model := range models {
		if err := security.ValidateModelName(model); err != nil {
			writeError(c, http.StatusBadRequest, fmt.Sprintf("模型名称无效: %s", model))
			return
		}
	}
	modelMapping, err := normalizeAccountModelMapping(req.ModelMapping)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	apiKey := strings.TrimSpace(req.APIKey)

	credentials := map[string]interface{}{
		"upstream_type": auth.UpstreamGrok,
		"base_url":      baseURL,
		"models":        models,
		"model_mapping": modelMapping,
	}
	if apiKey != "" {
		credentials["api_key"] = apiKey
	}
	if err := h.db.UpdateCredentials(ctx, id, credentials); err != nil {
		writeInternalError(c, err)
		return
	}
	if req.Name != "" {
		_ = h.db.UpdateAccountName(ctx, id, req.Name)
	}
	if h.store != nil {
		h.store.ApplyGrokConfig(id, baseURL, apiKey, models, modelMapping, req.ProxyURL)
	}
	h.db.InsertAccountEventAsync(id, "updated", "manual_grok")
	writeMessage(c, http.StatusOK, "Grok 账号设置已更新")
}

// FetchGrokModels 用请求内凭据或已保存账号凭据探测 Grok 上游模型目录
// （POST /api/admin/accounts/grok/models）。
func (h *Handler) FetchGrokModels(c *gin.Context) {
	var req addGrokAccountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式错误")
		return
	}

	credentials, _, err := grokCredentialsFromRequest(&req)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	// 构造临时账号做探测，不入库、不入池
	probe := &auth.Account{
		UpstreamType:      auth.UpstreamGrok,
		BaseURL:           strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"),
		ProxyURL:          security.SanitizeInput(req.ProxyURL),
		APIKey:            credentialStringValue(credentials, "api_key"),
		AccessToken:       credentialStringValue(credentials, "access_token"),
		RefreshToken:      credentialStringValue(credentials, "refresh_token"),
		GrokClientID:      credentialStringValue(credentials, "grok_client_id"),
		GrokTokenEndpoint: credentialStringValue(credentials, "grok_token_endpoint"),
		GrokOIDCIssuer:    credentialStringValue(credentials, "grok_oidc_issuer"),
		GrokPrincipalType: credentialStringValue(credentials, "grok_principal_type"),
		GrokPrincipalID:   credentialStringValue(credentials, "grok_principal_id"),
		AccountID:         credentialStringValue(credentials, "account_id"),
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	// OAuth 凭据无 AT 时先刷一次
	if probe.APIKey == "" && probe.AccessToken == "" && probe.RefreshToken != "" {
		td, refreshErr := auth.RefreshGrokAccessToken(ctx, auth.GrokRefreshParams{
			RefreshToken:  probe.RefreshToken,
			ClientID:      probe.GrokClientID,
			TokenEndpoint: probe.GrokTokenEndpoint,
			OIDCIssuer:    probe.GrokOIDCIssuer,
			PrincipalType: probe.GrokPrincipalType,
			PrincipalID:   probe.GrokPrincipalID,
			ProxyURL:      probe.ProxyURL,
		})
		if refreshErr != nil {
			writeError(c, http.StatusBadGateway, fmt.Sprintf("Grok 凭据刷新失败: %s", refreshErr.Error()))
			return
		}
		probe.AccessToken = td.AccessToken
	}

	models, err := proxy.FetchGrokModelIDs(ctx, probe)
	if err != nil {
		writeError(c, http.StatusBadGateway, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"models": models})
}

func credentialStringValue(credentials map[string]interface{}, key string) string {
	if credentials == nil {
		return ""
	}
	if value, ok := credentials[key].(string); ok {
		return value
	}
	return ""
}
