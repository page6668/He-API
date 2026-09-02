// AD-004 —— Adapter 拉取运行时配置的**内部**端点(handlers/internal_providers.go)。
//
// GET /internal/providers/active —— 返回明文 key + base_url(enabled=true 的)。
//
// 安全:仅允许来自本机(127.0.0.1 / ::1)的请求;其他来源一律 403。生产还应配合
// 防火墙(裸机单 VM 上 adapter 与 gateway 同机,天然只走 loopback)。无鉴权:
// 内部端点不暴露公网,且 key 走 loopback,不落地到日志(只记 provider 名)。
package handlers

import (
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/he-api/he-api/apps/api-gateway/internal/providers"
)

// InternalProvidersHandler 给 adapter 拉配置用。
type InternalProvidersHandler struct {
	store  *providers.Store
	logger *slog.Logger
}

func NewInternalProvidersHandler(store *providers.Store, logger *slog.Logger) *InternalProvidersHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &InternalProvidersHandler{store: store, logger: logger}
}

// Active 实现 GET /internal/providers/active(仅 loopback)。
func (h *InternalProvidersHandler) Active(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		h.logger.Warn("internal providers endpoint accessed from non-loopback",
			slog.String("remote", r.RemoteAddr))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	list, err := h.store.GetActive(ctx)
	if err != nil {
		h.logger.Error("internal providers active", slog.String("error", err.Error()))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// isLoopback 判断 RemoteAddr(host:port) 是否来自 127.0.0.0/8 或 ::1。
func isLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote // 无端口时直接用
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// 反向代理场景:X-Forwarded-For / X-Real-IP 不带协议栈层保障,
		// 本端点不依赖它们(裸机 adapter 直连 gateway,无代理)。
		return strings.HasPrefix(host, "127.") || host == "localhost" || host == "[::1]"
	}
	return ip.IsLoopback()
}
