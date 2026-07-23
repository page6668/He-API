// AD-003 —— 管理员定价接口(specs/admin-pricing-arch.md)。
//
// POST /v1/admin/models/pricing —— 必须包在 RequireJWT + AdminGuard.Require 之内
// (鉴权在中间件,本 handler 只管入参校验 + 写库)。管理员在 Console 后台填「元/百万
// tokens」,换算成 USD/1K 的算术在 catalogue.PricingWriter 的 SQL 里以 NUMERIC 完成。
//
// GET /v1/admin/models/pricing/defaults —— 返回后台预填用的默认汇率(fx_rates 最新
// USD→CNY),让管理员不必手抄汇率。
package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/he-api/he-api/apps/api-gateway/internal/catalogue"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// decimalRe 限定十进制正数字面量:最多一个小数点,只含数字。空/负/科学计数法/
// 非数字一律拒绝 —— 这些值最终进 ::numeric 强转,先在这里挡掉可给出清晰报错。
var decimalRe = regexp.MustCompile(`^\d+(\.\d+)?$`)

// AdminPricingHandler 承载定价写入与默认值读取。
type AdminPricingHandler struct {
	writer *catalogue.PricingWriter
	logger *slog.Logger
}

func NewAdminPricingHandler(writer *catalogue.PricingWriter, logger *slog.Logger) *AdminPricingHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AdminPricingHandler{writer: writer, logger: logger}
}

type setPriceBody struct {
	ModelID         string `json:"model_id"`
	InputCNYPerMil  string `json:"input_cny_per_million"`
	OutputCNYPerMil string `json:"output_cny_per_million"`
	FXUSDToCNY      string `json:"fx_usd_cny"`
}

// SetPrice 实现 POST /v1/admin/models/pricing。
func (h *AdminPricingHandler) SetPrice(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_body", "request body too large or unreadable", nil)
		return
	}
	dec := json.NewDecoder(bytes.NewReader(bodyBytes))
	dec.DisallowUnknownFields()
	var body setPriceBody
	if err := dec.Decode(&body); err != nil {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_unknown_field", err.Error(), nil)
		return
	}
	if dec.More() {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_unknown_field", "request body must contain a single JSON object", nil)
		return
	}

	// 校验四个字段:model_id 非空,三个数值是正十进制字符串。
	if body.ModelID == "" {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request", "model_id is required", nil)
		return
	}
	for name, v := range map[string]string{
		"input_cny_per_million":  body.InputCNYPerMil,
		"output_cny_per_million": body.OutputCNYPerMil,
		"fx_usd_cny":             body.FXUSDToCNY,
	} {
		if !decimalRe.MatchString(v) || isZeroDecimal(v) {
			_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request",
				name+" must be a positive decimal string", nil)
			return
		}
	}

	written, err := h.writer.SetPrice(r.Context(), catalogue.PriceInput{
		ModelID:         body.ModelID,
		InputCNYPerMil:  body.InputCNYPerMil,
		OutputCNYPerMil: body.OutputCNYPerMil,
		FXUSDToCNY:      body.FXUSDToCNY,
	})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "admin_set_price_failed",
			slog.String("event", "admin_set_price_failed"),
			slog.String("model_id", body.ModelID),
			slog.String("error", err.Error()))
		_ = openaierr.Write(w, r.Context(), http.StatusInternalServerError, "500_internal_error", "failed to write pricing", nil)
		return
	}
	if !written {
		// 子查询没匹配到 active 模型:model_id 不存在或已 deprecated。
		_ = openaierr.Write(w, r.Context(), http.StatusNotFound, "404_model_not_found",
			"no active model with that id", nil)
		return
	}

	// 审计:记下换算入参(元/百万 + 汇率),便于日后核对价格来源。非 PII。
	h.logger.InfoContext(r.Context(), "admin_set_price",
		slog.String("event", "admin_set_price"),
		slog.String("model_id", body.ModelID),
		slog.String("input_cny_per_million", body.InputCNYPerMil),
		slog.String("output_cny_per_million", body.OutputCNYPerMil),
		slog.String("fx_usd_cny", body.FXUSDToCNY))

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// Defaults 实现 GET /v1/admin/models/pricing/defaults —— 返回预填汇率。
func (h *AdminPricingHandler) Defaults(w http.ResponseWriter, r *http.Request) {
	fx, err := h.writer.LatestFXUSDToCNY(r.Context())
	if err != nil {
		h.logger.ErrorContext(r.Context(), "admin_pricing_defaults_failed",
			slog.String("event", "admin_pricing_defaults_failed"),
			slog.String("error", err.Error()))
		_ = openaierr.Write(w, r.Context(), http.StatusInternalServerError, "500_internal_error", "failed to read fx default", nil)
		return
	}
	// fx 可能为空字符串(fx_rates 无行)——照实返回,前端据此要求手填。
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"fx_usd_cny": fx})
}

// isZeroDecimal 判断一个已通过 decimalRe 的字符串数值上是否为 0(如 "0"、"0.00")。
// 价格与汇率都不能为 0:0 价会被读成免费,0 汇率会导致除零。
func isZeroDecimal(s string) bool {
	for _, c := range s {
		if c >= '1' && c <= '9' {
			return false
		}
	}
	return true
}
