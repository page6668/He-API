// AD-004 —— 管理员 LLM Provider 配置接口(handlers/admin_providers.go)。
//
// GET  /v1/admin/providers            —— 列表(密钥 masked)
// PUT  /v1/admin/providers/{name}     —— 更新 key / base_url / enabled
//
// 两个端点都必须在 RequireJWT + AdminGuard.Require 之内(鉴权在中间件)。本 handler
// 只做入参校验 + 调 store。user_id 从 JWT 上下文取(store.Update 的审计列)。
package handlers

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
	"github.com/he-api/he-api/apps/api-gateway/internal/providers"
)

// AdminProvidersHandler 承载 provider 配置的读与写。
type AdminProvidersHandler struct {
	store  *providers.Store
	logger *slog.Logger
}

func NewAdminProvidersHandler(store *providers.Store, logger *slog.Logger) *AdminProvidersHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AdminProvidersHandler{store: store, logger: logger}
}

type updateProviderBody struct {
	APIKey  *string `json:"api_key"`  // nil/空 → 保留现有 key
	BaseURL *string `json:"base_url"` // nil/空 → 保留 / 平台默认
	Enabled *bool   `json:"enabled"`  // nil → 保留现有
}

// List 实现 GET /v1/admin/providers。
func (h *AdminProvidersHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := h.store.List(ctx)
	if err != nil {
		h.logger.Error("admin providers list", slog.String("error", err.Error()))
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_internal_error", "failed to load providers", nil)
		return
	}
	writeJSONOK(w, http.StatusOK, map[string]any{"object": "list", "data": list})
}

// Update 实现 PUT /v1/admin/providers/{name}。name 从路径末段取。
func (h *AdminProvidersHandler) Update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := lastPathSegment(r.URL.Path)
	if name == "" {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request", "provider name required", nil)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_body", "request body too large or unreadable", nil)
		return
	}
	var body updateProviderBody
	dec := json.NewDecoder(strings.NewReader(string(bodyBytes)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_unknown_field", err.Error(), nil)
		return
	}

	apiKey := ""
	if body.APIKey != nil {
		apiKey = strings.TrimSpace(*body.APIKey)
		// 防御:前端误把 masked 值(sk-***abcd)传回来当新 key
		if strings.Contains(apiKey, "*") {
			_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request",
				"api_key must be a real key, not a masked value", nil)
			return
		}
	}
	baseURL := ""
	if body.BaseURL != nil {
		baseURL = strings.TrimSpace(*body.BaseURL)
	}

	userID, _ := middleware.UserIDFromContext(ctx)
	if err := h.store.Update(ctx, name, apiKey, baseURL, body.Enabled, &userID); err != nil {
		h.logger.Error("admin providers update", slog.String("provider", name), slog.String("error", err.Error()))
		if strings.Contains(err.Error(), "unknown provider") {
			_ = openaierr.Write(w, ctx, http.StatusNotFound, "404_not_found", err.Error(), nil)
			return
		}
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_internal_error", "failed to update provider", nil)
		return
	}
	writeJSONOK(w, http.StatusOK, map[string]any{"ok": true})
}

// lastPathSegment 取 /v1/admin/providers/{name} 的末段。空路径返回 ""。
func lastPathSegment(path string) string {
	path = strings.TrimRight(path, "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return ""
}

// writeJSONOK 通用 JSON 响应(避免与 auth.go 的 writeJSON 冲突)。
func writeJSONOK(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
