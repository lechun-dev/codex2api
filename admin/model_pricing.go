package admin

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

// modelPricingRow 是定价管理页每个规范模型的一行：当前生效价 + 来源。
type modelPricingRow struct {
	Model   string                        `json:"model"`
	Source  string                        `json:"source"` // custom / synced / default
	Pricing database.ModelPricingOverride `json:"pricing"`
}

// ListModelPricing 返回各规范模型的当前生效定价与来源，供设置页定价表展示。
func (h *Handler) ListModelPricing(c *gin.Context) {
	ctx := c.Request.Context()

	// 取当前对外暴露的模型，映射到规范定价键去重（退役模型自然被排除）。
	seen := map[string]struct{}{}
	keys := make([]string, 0, 16)
	for _, id := range proxy.SupportedModelIDs(ctx, h.db) {
		key := database.CanonicalBillingModelKey(id)
		if key == "" || strings.Contains(key, "(") { // 跳过思考强度别名
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	rows := make([]modelPricingRow, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, modelPricingRow{
			Model:   key,
			Source:  database.ModelPricingSourceFor(key),
			Pricing: database.ModelPricingOverrideFromPricing(database.GetModelPricing(key), database.ModelPricingSourceFor(key)),
		})
	}

	syncURL := ""
	if s, err := h.db.GetSystemSettings(ctx); err == nil && s != nil {
		syncURL = strings.TrimSpace(s.ModelPricingSyncURL)
	}
	c.JSON(http.StatusOK, gin.H{
		"models":           rows,
		"sync_url":         syncURL,
		"default_sync_url": proxy.DefaultModelPricingSyncURL,
	})
}

// UpdateModelPricingRequest 设置/清除某模型的 custom 定价覆盖。
type UpdateModelPricingRequest struct {
	Model   string                         `json:"model"`
	Reset   bool                           `json:"reset"`   // true 时清除该模型覆盖，回退代码默认
	Pricing *database.ModelPricingOverride `json:"pricing"` // reset=false 时必填
}

// UpdateModelPricing 写入/清除某模型的 custom 定价覆盖。
func (h *Handler) UpdateModelPricing(c *gin.Context) {
	var req UpdateModelPricingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	key := database.CanonicalBillingModelKey(strings.TrimSpace(req.Model))
	if key == "" {
		writeError(c, http.StatusBadRequest, "model 不能为空")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	settings, err := h.db.GetSystemSettings(ctx)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if settings == nil {
		settings = &database.SystemSettings{}
	}
	overrides, err := database.ParseModelPricingOverridesJSON(settings.ModelPricingOverrides)
	if err != nil {
		overrides = map[string]database.ModelPricingOverride{}
	}

	if req.Reset {
		delete(overrides, key)
	} else {
		if req.Pricing == nil || req.Pricing.IsEmpty() {
			writeError(c, http.StatusBadRequest, "pricing 不能为空（或用 reset 清除）")
			return
		}
		ov := *req.Pricing
		ov.Source = database.ModelPricingSourceCustom
		overrides[key] = ov
	}

	blob, err := database.MarshalModelPricingOverridesJSON(overrides)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	settings.ModelPricingOverrides = blob
	if err := h.db.UpdateSystemSettings(ctx, settings); err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	database.SetModelPricingOverrides(overrides)
	c.JSON(http.StatusOK, gin.H{"model": key, "reset": req.Reset})
}

// SyncModelPricingRequest 可选携带一次性同步 URL（同时保存为默认来源）。
type SyncModelPricingRequest struct {
	URL string `json:"url"`
}

// SyncModelPricing 从 JSON URL 同步定价（synced 覆盖，不动 custom）。
func (h *Handler) SyncModelPricing(c *gin.Context) {
	var req SyncModelPricingRequest
	_ = c.ShouldBindJSON(&req)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()

	proxyURL := ""
	if h.store != nil {
		proxyURL = h.store.GetProxyURL()
	}
	// 字段即准：直接传设置页 URL 字段的当前值（空 → 用内置默认并清空存储来源）。
	result, err := proxy.SyncModelPricingFromURL(ctx, h.db, req.URL, proxyURL)
	if err != nil {
		writeError(c, http.StatusBadGateway, err.Error())
		return
	}
	c.JSON(http.StatusOK, result)
}
