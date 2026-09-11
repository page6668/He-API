// AD-006 — 管理员模型管理接口。
//
// GET  /v1/admin/models          — 列表（含 deprecated/pending，不含 key）
// POST /v1/admin/models          — 新增模型（默认 status=pending，不可直接调用）
// POST /v1/admin/models/{id}/deprecate — 下架模型
//
// 三个端点都必须在 RequireJWT + AdminGuard.Require 之内（鉴权在中间件）。
package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/he-api/he-api/apps/api-gateway/internal/catalogue"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// AdminModelsHandler 承载模型列表 / 新增 / 下架。
type AdminModelsHandler struct {
	writer *catalogue.Writer
	logger *slog.Logger
}

func NewAdminModelsHandler(writer *catalogue.Writer, logger *slog.Logger) *AdminModelsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AdminModelsHandler{writer: writer, logger: logger}
}

// List 实现 GET /v1/admin/models。
func (h *AdminModelsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	models, err := h.writer.ListAllModels(ctx)
	if err != nil {
		h.logger.ErrorContext(ctx, "admin_models_list", slog.String("error", err.Error()))
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_internal_error", "failed to load models", nil)
		return
	}
	// 序列化时保留 capabilities JSON 原文（前端直接透传）。
	type CapsModel struct {
		ID              string  `json:"id"`
		DisplayName     string  `json:"display_name"`
		Vendor          string  `json:"vendor"`
		Capabilities    any     `json:"capabilities"` // 反序列化后
		UpstreamModelID string  `json:"upstream_model_id"`
		Status          string  `json:"status"`
		CreatedAt       string  `json:"created_at"`
		UpdatedAt       *string `json:"updated_at,omitempty"`
	}
	data := make([]CapsModel, 0, len(models))
	for _, m := range models {
		var caps any
		_ = json.Unmarshal([]byte(m.CapabilitiesJSON), &caps)
		data = append(data, CapsModel{
			ID:              m.ID,
			DisplayName:     m.DisplayName,
			Vendor:          m.Vendor,
			Capabilities:    caps,
			UpstreamModelID: m.UpstreamModelID,
			Status:          m.Status,
			CreatedAt:       m.CreatedAt,
			UpdatedAt:       m.UpdatedAt,
		})
	}
	writeJSONOK(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

type createModelBody struct {
	ID              string `json:"id"`
	DisplayName     string `json:"display_name"`
	Vendor          string `json:"vendor"`
	Capabilities    any    `json:"capabilities"` // object 或 string
	UpstreamModelID string `json:"upstream_model_id,omitempty"`
}

// Create 实现 POST /v1/admin/models。新增模型默认 status=pending。
func (h *AdminModelsHandler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_body", "request body too large or unreadable", nil)
		return
	}
	var body createModelBody
	dec := json.NewDecoder(bytes.NewReader(bodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_unknown_field", err.Error(), nil)
		return
	}

	// 校验必填字段。
	if strings.TrimSpace(body.ID) == "" {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request", "id is required", nil)
		return
	}
	if strings.TrimSpace(body.DisplayName) == "" {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request", "display_name is required", nil)
		return
	}
	validVendors := map[string]bool{"alibaba": true, "deepseek": true, "moonshot": true, "zhipu": true, "bytedance": true, "baidu": true}
	if !validVendors[strings.ToLower(body.Vendor)] {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request",
			"vendor must be one of: alibaba, deepseek, moonshot, zhipu, bytedance, baidu", nil)
		return
	}

	// capabilities 必须是 object。
	capsBytes, err := json.Marshal(body.Capabilities)
	if err != nil || string(capsBytes) == "null" {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request",
			"capabilities must be a JSON object (e.g. {\"chat\":true,\"streaming\":true})", nil)
		return
	}

	ok, err := h.writer.CreateModel(ctx,
		strings.TrimSpace(body.ID),
		strings.TrimSpace(body.DisplayName),
		strings.ToLower(strings.TrimSpace(body.Vendor)),
		string(capsBytes),
		strings.TrimSpace(body.UpstreamModelID),
	)
	if err != nil {
		h.logger.ErrorContext(ctx, "admin_create_model_failed",
			slog.String("event", "admin_create_model_failed"),
			slog.String("model_id", body.ID),
			slog.String("error", err.Error()))
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_internal_error", "failed to create model", nil)
		return
	}
	if !ok {
		_ = openaierr.Write(w, ctx, http.StatusConflict, "409_conflict",
			"a model with this id already exists", nil)
		return
	}
	h.logger.InfoContext(ctx, "admin_create_model",
		slog.String("event", "admin_create_model"),
		slog.String("model_id", body.ID),
		slog.String("vendor", body.Vendor))

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{"status":"created","model_id":"` + body.ID + `"}`))
}

// Deprecate 实现 POST /v1/admin/models/{id}/deprecate。
func (h *AdminModelsHandler) Deprecate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// lastPathSegment 返回末段（如 /v1/admin/models/{id}/deprecate → "deprecate"），
	// 我们需要倒数第二段 {id}。
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	modelID := ""
	if len(parts) >= 2 {
		modelID = parts[len(parts)-2]
	}
	if modelID == "" {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request", "model id required", nil)
		return
	}

	ok, err := h.writer.DeprecateModel(ctx, modelID)
	if err != nil {
		h.logger.ErrorContext(ctx, "admin_deprecate_model_failed",
			slog.String("event", "admin_deprecate_model_failed"),
			slog.String("model_id", modelID),
			slog.String("error", err.Error()))
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_internal_error", "failed to deprecate model", nil)
		return
	}
	if !ok {
		_ = openaierr.Write(w, ctx, http.StatusNotFound, "404_not_found",
			"model not found or already deprecated", nil)
		return
	}
	h.logger.InfoContext(ctx, "admin_deprecate_model",
		slog.String("event", "admin_deprecate_model"),
		slog.String("model_id", modelID))

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"deprecated","model_id":"` + modelID + `"}`))
}
