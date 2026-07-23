package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/catalogue"
	"github.com/pashagolub/pgxmock/v3"
)

// 入参校验是这个写钱接口的第一道防线。非法输入必须在触库之前被 400 挡下 ——
// 尤其是 0 值(0 价=免费、0 汇率=除零)和非十进制串(会污染 ::numeric 强转)。
func TestAdminPricing_SetPrice_InputValidation(t *testing.T) {
	// writer 用 nil pool:合法输入会走到写库并因 nil pool 报错(500),但本用例只关心
	// 400 分支——所有断言都在触库之前返回,不会真的用到 pool。
	h := NewAdminPricingHandler(catalogue.NewPricingWriter(nil), discardLogger())

	cases := []struct {
		name string
		body string
		want int
	}{
		{"缺 model_id", `{"model_id":"","input_cny_per_million":"4","output_cny_per_million":"8","fx_usd_cny":"7.2"}`, 400},
		{"输入价为0", `{"model_id":"m","input_cny_per_million":"0","output_cny_per_million":"8","fx_usd_cny":"7.2"}`, 400},
		{"输出价为0.00", `{"model_id":"m","input_cny_per_million":"4","output_cny_per_million":"0.00","fx_usd_cny":"7.2"}`, 400},
		{"汇率为0", `{"model_id":"m","input_cny_per_million":"4","output_cny_per_million":"8","fx_usd_cny":"0"}`, 400},
		{"负数", `{"model_id":"m","input_cny_per_million":"-4","output_cny_per_million":"8","fx_usd_cny":"7.2"}`, 400},
		{"科学计数法", `{"model_id":"m","input_cny_per_million":"4e2","output_cny_per_million":"8","fx_usd_cny":"7.2"}`, 400},
		{"非数字", `{"model_id":"m","input_cny_per_million":"abc","output_cny_per_million":"8","fx_usd_cny":"7.2"}`, 400},
		{"未知字段", `{"model_id":"m","input_cny_per_million":"4","output_cny_per_million":"8","fx_usd_cny":"7.2","x":1}`, 400},
		{"多个JSON对象", `{"model_id":"m","input_cny_per_million":"4","output_cny_per_million":"8","fx_usd_cny":"7.2"}{}`, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/admin/models/pricing", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			h.SetPrice(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("body=%s → %d, want %d (%s)", tc.body, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// 合法输入 + 模型不存在(写库影响 0 行)→ 404,不是 200。
func TestAdminPricing_SetPrice_UnknownModel_404(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	// INSERT ... SELECT 未匹配到 active 模型 → 0 行受影响。
	mock.ExpectExec("INSERT INTO he_api.model_pricing").
		WithArgs("ghost", "4", "8", "7.2").
		WillReturnResult(pgxmock.NewResult("INSERT", 0))

	h := NewAdminPricingHandler(catalogue.NewPricingWriter(mock), discardLogger())
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/models/pricing",
		strings.NewReader(`{"model_id":"ghost","input_cny_per_million":"4","output_cny_per_million":"8","fx_usd_cny":"7.2"}`))
	rec := httptest.NewRecorder()
	h.SetPrice(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown model → %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

// 合法输入 + 写入成功 → 200。断言 SQL 收到的正是原始「元/百万」字符串与汇率
// (换算在 SQL 内做,Go 不预先计算)。
func TestAdminPricing_SetPrice_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectExec("INSERT INTO he_api.model_pricing").
		WithArgs("qwen3.7-plus", "5.76", "14.4", "7.2").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	h := NewAdminPricingHandler(catalogue.NewPricingWriter(mock), discardLogger())
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/models/pricing",
		strings.NewReader(`{"model_id":"qwen3.7-plus","input_cny_per_million":"5.76","output_cny_per_million":"14.4","fx_usd_cny":"7.2"}`))
	rec := httptest.NewRecorder()
	h.SetPrice(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("success → %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL 未按预期收到原始字符串参数: %v", err)
	}
}

// Defaults:fx_rates 有行 → 返回该汇率;无行 → 返回空串(前端据此要求手填)。
func TestAdminPricing_Defaults(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT rate::text FROM he_api.fx_rates").
		WillReturnRows(pgxmock.NewRows([]string{"rate"}).AddRow("7.19000000"))

	h := NewAdminPricingHandler(catalogue.NewPricingWriter(mock), discardLogger())
	rec := httptest.NewRecorder()
	h.Defaults(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/models/pricing/defaults", nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "7.19000000") {
		t.Fatalf("defaults → %d %s", rec.Code, rec.Body.String())
	}
}
