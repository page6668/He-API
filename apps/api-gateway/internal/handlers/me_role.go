// AD-003 —— GET /v1/me/role(specs/admin-pricing-arch.md)。
//
// 让前端知道「当前用户是不是管理员」,从而决定是否显示管理入口。只需登录
// (RequireJWT),不需要管理员权限 —— 每个人都可以查自己的角色。
//
// role 不在 JWT 里(那要改 auth-svc),故在网关读库返回。与 AdminGuard 同源:
// 前端隐藏入口只是体验,真正的写钱拦截仍在 AdminGuard。
//
// fail-safe:读库出错 / 用户不存在 → 返回 "user"。不确定时按普通用户处理,
// 宁可少显示一个管理入口,也不误给非管理员看到。
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
	"github.com/jackc/pgx/v5"
)

// roleQuerier 是本 handler 需要的最小 pgx 面(*pgxpool.Pool / pgxmock 都满足)。
type roleQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// MeRoleHandler 返回当前 JWT 用户的角色。
type MeRoleHandler struct {
	pool   roleQuerier
	logger *slog.Logger
}

func NewMeRoleHandler(pool roleQuerier, logger *slog.Logger) *MeRoleHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &MeRoleHandler{pool: pool, logger: logger}
}

// ServeHTTP 实现 GET /v1/me/role → {"role":"admin"|"user"}。
func (h *MeRoleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, "401_unauthorized", "missing access token", nil)
		return
	}

	role := "user" // fail-safe 默认
	if h.pool != nil {
		var got string
		err := h.pool.QueryRow(r.Context(),
			`SELECT role FROM he_api.users WHERE id = $1`, userID).Scan(&got)
		switch {
		case err == nil:
			if got == "admin" {
				role = "admin"
			}
		case errors.Is(err, pgx.ErrNoRows):
			// 用户不存在 —— 保持 "user"。
		default:
			h.logger.WarnContext(r.Context(), "me_role_lookup_failed",
				slog.String("event", "me_role_lookup_failed"),
				slog.String("error", err.Error()))
			// 读库错也保持 "user"(fail-safe)。
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"role": role})
}
