// AD-003 —— 管理员授权门(specs/admin-pricing-arch.md)。
//
// 组合在 RequireJWT **之后**:RequireJWT 已把 user_id 放进 context,本门用它读库
// 查 role。role 不放在 JWT 里(那要改 auth-svc 令牌铸造),而是在网关按需读库 ——
// 网关已有 DB 连接池,加管理员只需一条 UPDATE、无需重启、无需等令牌过期。
//
// fail-closed:池为 nil、读库出错、role 非 'admin'、user_id 缺失 —— 一律 403。
// 写钱的接口宁可错拒,不可错放。
package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
	"github.com/jackc/pgx/v5"
)

// RoleQuerier 是本门需要的最小 pgx 面 —— *pgxpool.Pool 与 pgxmock 都满足。
type RoleQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// AdminGuard 校验当前 JWT 用户在 he_api.users 中的 role 是否为 'admin'。
type AdminGuard struct {
	pool   RoleQuerier
	logger *slog.Logger
}

// NewAdminGuard 构造门。pool 为 nil 时门永远拒绝(fail-closed)——没有数据库就
// 无法确认任何人是管理员。
//
// 注意:调用方若持有 typed-nil 的 *pgxpool.Pool,必须先判空再传(传 nil 接口),
// 否则接口非 nil、下面的 g.pool == nil 会失效。main.go 已按此处理。
func NewAdminGuard(pool RoleQuerier, logger *slog.Logger) *AdminGuard {
	if logger == nil {
		logger = slog.Default()
	}
	return &AdminGuard{pool: pool, logger: logger}
}

// isAdmin 读库判定。任何异常都返回 false —— 判定不了就当作不是管理员。
func (g *AdminGuard) isAdmin(ctx context.Context, userID string) bool {
	if g == nil || g.pool == nil || userID == "" {
		return false
	}
	var role string
	err := g.pool.QueryRow(ctx,
		`SELECT role FROM he_api.users WHERE id = $1`, userID).Scan(&role)
	if err != nil {
		// pgx.ErrNoRows(用户不存在)与真正的库错误都归为非管理员;后者额外记日志。
		if !errors.Is(err, pgx.ErrNoRows) {
			g.logger.WarnContext(ctx, "admin_guard_role_lookup_failed",
				slog.String("event", "admin_guard_role_lookup_failed"),
				slog.String("error", err.Error()))
		}
		return false
	}
	return role == "admin"
}

// Require 是中间件:仅当当前 JWT 用户是管理员时放行,否则 403。
// 必须包在 RequireJWT 之内(依赖 UserIDFromContext)。
func (g *AdminGuard) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := UserIDFromContext(r.Context())
		if !ok {
			// RequireJWT 未先运行 —— 接线 bug,按 500 暴露而非静默放行。
			_ = openaierr.Write(w, r.Context(), http.StatusInternalServerError,
				"500_gateway_misconfigured", "admin guard not wrapped by RequireJWT", nil)
			return
		}
		if !g.isAdmin(r.Context(), userID) {
			// 不区分「非管理员」与「读库失败」——都回 403,不向调用方泄露内部状态。
			_ = openaierr.Write(w, r.Context(), http.StatusForbidden,
				"403_admin_required", "administrator role required", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}
