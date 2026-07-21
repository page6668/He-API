package handlers

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/catalogue"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeCatalogue is a mutable stand-in for catalogue.Snapshot.
type fakeCatalogue struct{ models []catalogue.Model }

func (f *fakeCatalogue) Models() []catalogue.Model { return f.models }

func body(t *testing.T, h http.Handler, req *http.Request) ModelsResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got ModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return got
}

func bearerRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	return req.WithContext(middleware.WithAPIKeyID(req.Context(), modelsTestAPIKeyID))
}

// AD-002's whole purpose: a model priced in the database becomes purchasable
// without a deploy. A snapshot captured at construction cannot do that, so this
// asserts the handler re-reads the source on every request.
func TestModelsHandler_ReflectsCatalogueRefresh(t *testing.T) {
	cat := &fakeCatalogue{models: []catalogue.Model{{ID: "qwen3.7-plus", Vendor: "alibaba"}}}
	h := NewModelsHandler(discardLogger(), WithModelsSource(CatalogueSource(cat)))

	if got := body(t, h, bearerRequest()); len(got.Data) != 1 || got.Data[0].ID != "qwen3.7-plus" {
		t.Fatalf("first read = %+v", got.Data)
	}

	// A background refresh lands — a newly priced model appears.
	cat.models = append(cat.models, catalogue.Model{ID: "deepseek-v4-pro", Vendor: "deepseek"})

	got := body(t, h, bearerRequest())
	if len(got.Data) != 2 || got.Data[1].ID != "deepseek-v4-pro" {
		t.Fatalf("handler did not see the refresh: %+v", got.Data)
	}
}

// 4.7-INT-001: the public mirror must stay byte-identical to the bearer-gated
// endpoint. Moving one of them to the database while the other kept reading the
// compiled-in table would break this silently, so it is pinned on the DB path.
func TestModelsEndpoints_ByteIdenticalOnCatalogueSource(t *testing.T) {
	cat := &fakeCatalogue{models: []catalogue.Model{
		{ID: "qwen3.7-plus", Vendor: "alibaba", Capabilities: catalogue.Capabilities{
			Chat: true, Streaming: true, ContextWindowTokens: 131072}},
		{ID: "kimi-k2.6", Vendor: "moonshot", Capabilities: catalogue.Capabilities{Chat: true}},
	}}
	src := CatalogueSource(cat)

	bearer := NewModelsHandler(discardLogger(), WithModelsSource(src))
	public := NewLivePublicModelsHandler(discardLogger(), src, bearer.StartedAt())

	bearerRec := httptest.NewRecorder()
	bearer.ServeHTTP(bearerRec, bearerRequest())
	publicRec := httptest.NewRecorder()
	public.ServeHTTP(publicRec, httptest.NewRequest(http.MethodGet, "/public/models", nil))

	if bearerRec.Body.String() != publicRec.Body.String() {
		t.Fatalf("bodies diverged:\n bearer=%s\n public=%s",
			bearerRec.Body.String(), publicRec.Body.String())
	}
}

// The compiled-in registry must survive as a cold-start fallback (AD-002
// failure mode 2) — an empty fallback would hand customers a blank catalogue
// the first time the database is unreachable at boot.
func TestCatalogueFallback_IsNotEmpty(t *testing.T) {
	fb := CatalogueFallback()
	if len(fb) == 0 {
		t.Fatal("CatalogueFallback() is empty")
	}
	if len(fb) != len(modelsCatalogue) {
		t.Errorf("fallback has %d models, compiled-in catalogue has %d", len(fb), len(modelsCatalogue))
	}
}

// AD-002 — capability gates must follow the database. Before this, an operator
// marking a model vision-capable in he_api.models still got a 400 from the 9.5
// gate because it read the compiled-in table: exactly the silent-rot failure
// AD-002 exists to remove.
func TestCatalogueCapabilities_TracksDatabase(t *testing.T) {
	cat := &fakeCatalogue{models: []catalogue.Model{
		{ID: "qwen3.7-vl", Capabilities: catalogue.Capabilities{Chat: true, Vision: false}},
	}}
	lookup := CatalogueCapabilities(cat)

	if caps, ok := lookup("qwen3.7-vl"); !ok || caps.Vision {
		t.Fatalf("initial lookup = %+v ok=%v, want vision false", caps, ok)
	}
	// An operator enables vision in the database; a refresh lands.
	cat.models[0].Capabilities.Vision = true
	if caps, _ := lookup("qwen3.7-vl"); !caps.Vision {
		t.Error("lookup did not follow the database")
	}
	// Unknown ids report not-found so the gates keep passing them through
	// (operator-supplied routes still work while their rows are being priced).
	if _, ok := lookup("not-in-catalogue"); ok {
		t.Error("unknown model reported as known")
	}
}

// AD-002 —— 价格是加法变更:追加在 capabilities 之后(BR-1.4「He-API 扩展在
// 末尾」),金额是精确十进制字符串而非 JSON number(M-1:客户据以决策的钱不
// 走 float),币种显式标注。
func TestModelEntry_PricingIsAppendedLastAsDecimalStrings(t *testing.T) {
	cat := &fakeCatalogue{models: []catalogue.Model{{
		ID: "qwen3.7-plus", Vendor: "alibaba",
		Capabilities:     catalogue.Capabilities{Chat: true},
		InputPricePer1K:  "0.000880",
		OutputPricePer1K: "0.002200",
	}}}
	entries := EntriesFromCatalogue(cat.Models(), 1700000000)
	buf, err := json.Marshal(entries[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(buf)

	// 追加在末尾,且金额带引号(字符串)。
	want := `,"pricing":{"input_per_1k_tokens":"0.000880","output_per_1k_tokens":"0.002200","currency":"USD"}}`
	if !strings.HasSuffix(got, want) {
		t.Errorf("pricing not appended last as strings\n  got: %s", got)
	}
	// capabilities 仍在 pricing 之前 —— OpenAI 规范字段在前的顺序没有被破坏。
	if strings.Index(got, `"capabilities"`) > strings.Index(got, `"pricing"`) {
		t.Error("pricing must follow capabilities (BR-1.4)")
	}
}

// 冷启动兜底没有价格。此时 pricing 字段必须整个消失 —— 输出 0 会被读成「免费」。
func TestModelEntry_PricingOmittedWhenUnknown(t *testing.T) {
	entries := EntriesFromCatalogue(CatalogueFallback(), 1700000000)
	if len(entries) == 0 {
		t.Fatal("fallback produced no entries")
	}
	buf, err := json.Marshal(entries[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(buf), "pricing") {
		t.Errorf("unpriced entry must omit pricing entirely\n  got: %s", buf)
	}
}

// 半价行(只有一侧有价)同样不展示 —— 宁可不显示,不可显示一半的价格。
func TestModelEntry_PricingOmittedWhenHalfPriced(t *testing.T) {
	models := []catalogue.Model{{ID: "half", Vendor: "x", InputPricePer1K: "0.001000"}}
	buf, _ := json.Marshal(EntriesFromCatalogue(models, 1)[0])
	if strings.Contains(string(buf), "pricing") {
		t.Errorf("half-priced entry must omit pricing\n  got: %s", buf)
	}
}
