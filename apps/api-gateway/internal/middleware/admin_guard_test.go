package middleware

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v3"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func okNext() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("passed"))
	})
}

// 写钱的门必须 fail-closed:没有数据库池 → 任何人都不是管理员 → 403。
func TestAdminGuard_NilPool_Forbids(t *testing.T) {
	g := NewAdminGuard(nil, quietLog())
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/models/pricing", nil)
	req = req.WithContext(WithUserID(req.Context(), "u-1"))
	rec := httptest.NewRecorder()

	g.Require(okNext()).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("nil pool = %d, want 403", rec.Code)
	}
}

// RequireJWT 未先运行(context 无 user_id)→ 500,而不是静默放行。
func TestAdminGuard_NoJWTContext_500(t *testing.T) {
	g := NewAdminGuard(nil, quietLog())
	rec := httptest.NewRecorder()
	g.Require(okNext()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("missing jwt ctx = %d, want 500", rec.Code)
	}
}

func TestAdminGuard_RoleLookup(t *testing.T) {
	cases := []struct {
		name     string
		role     string
		queryErr error
		want     int
	}{
		{"admin 放行", "admin", nil, http.StatusOK},
		{"普通用户 403", "user", nil, http.StatusForbidden},
		{"用户不存在 403", "", pgx.ErrNoRows, http.StatusForbidden},
		{"读库出错 403", "", errors.New("db down"), http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()

			eq := mock.ExpectQuery("SELECT role FROM he_api.users").WithArgs("u-1")
			if tc.queryErr != nil {
				eq.WillReturnError(tc.queryErr)
			} else {
				eq.WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow(tc.role))
			}

			g := NewAdminGuard(mock, quietLog())
			req := httptest.NewRequest(http.MethodPost, "/x", nil)
			req = req.WithContext(WithUserID(req.Context(), "u-1"))
			rec := httptest.NewRecorder()
			g.Require(okNext()).ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("role=%q err=%v → %d, want %d", tc.role, tc.queryErr, rec.Code, tc.want)
			}
		})
	}
}

// pgxmock 满足 RoleQuerier(QueryRow)—— 编译期确认接口契合。
var _ RoleQuerier = (pgxmock.PgxPoolIface)(nil)

func TestAdminGuard_isAdmin_NilReceiver(t *testing.T) {
	var g *AdminGuard
	if g.isAdmin(context.Background(), "u-1") {
		t.Error("nil guard reported admin")
	}
}
