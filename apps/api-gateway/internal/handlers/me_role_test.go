package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v3"
)

func meRoleReq() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/me/role", nil)
	return req.WithContext(middleware.WithUserID(req.Context(), "u-1"))
}

func TestMeRole(t *testing.T) {
	cases := []struct {
		name     string
		dbRole   string
		queryErr error
		wantRole string
	}{
		{"管理员", "admin", nil, "admin"},
		{"普通用户", "user", nil, "user"},
		{"用户不存在→user", "", pgx.ErrNoRows, "user"},
		{"读库出错→user(fail-safe)", "", errors.New("db down"), "user"},
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
				eq.WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow(tc.dbRole))
			}

			h := NewMeRoleHandler(mock, discardLogger())
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, meRoleReq())

			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, want 200", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), `"role":"`+tc.wantRole+`"`) {
				t.Fatalf("body = %s, want role %q", rec.Body.String(), tc.wantRole)
			}
		})
	}
}

// nil pool → fail-safe "user"(数据库不可用时,绝不把任何人当管理员显示入口)。
func TestMeRole_NilPool_DefaultsUser(t *testing.T) {
	h := NewMeRoleHandler(nil, discardLogger())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, meRoleReq())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"role":"user"`) {
		t.Fatalf("nil pool → %d %s, want 200 role=user", rec.Code, rec.Body.String())
	}
}

// 无 JWT 上下文 → 401(未包 RequireJWT 的接线 bug)。
func TestMeRole_NoJWT_401(t *testing.T) {
	h := NewMeRoleHandler(nil, discardLogger())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/role", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no jwt → %d, want 401", rec.Code)
	}
}
