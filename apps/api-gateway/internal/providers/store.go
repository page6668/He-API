// AD-004 —— LLM Provider 配置存储层(providers/store.go)。
//
// 封装 he_api.provider_configs 的 CRUD。密钥在入库前用 secrets.Key(AES-256-GCM)
// 加密;出库的 admin 视图只给 masked,internal 端点才给明文(限 127.0.0.1)。
//
// 六个已知 provider(deepseek/doubao/ernie/glm/kimi/qwen)是平台契约,即使表里
// 还没行,List 也要返回占位条目,让 UI 始终能渲染 6 张卡片。
package providers

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/secrets"
	"github.com/jackc/pgx/v5/pgxpool"
)

// KnownProviders 是平台与 adapter 代码约定的 6 个上游枚举。
var KnownProviders = []string{"deepseek", "doubao", "ernie", "glm", "kimi", "qwen"}

// ProviderListItem —— admin 列表视图(无明文 key)。
type ProviderListItem struct {
	Name       string  `json:"provider_name"`
	BaseURL    string  `json:"base_url"`
	Enabled    bool    `json:"enabled"`
	MaskedKey  string  `json:"masked_key"` // 形如 sk-***abcd;无 key 时为空
	HasKey     bool    `json:"has_key"`
	UpdatedAt  *string `json:"updated_at,omitempty"`
	VerifiedAt *string `json:"last_verified_at,omitempty"`
	LastError  *string `json:"last_error,omitempty"`
}

// ProviderActive —— adapter 内部端点(含明文 key,限 127.0.0.1)。
type ProviderActive struct {
	Name    string `json:"provider_name"`
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url"`
	Enabled bool   `json:"enabled"`
}

// Store 持有连接池与解密密钥。pool 为 nil 时所有方法返回错误(fail-closed)。
type Store struct {
	pool   *pgxpool.Pool
	key    *secrets.Key
	logger *slog.Logger
}

// NewStore 构造存储层。key 可为 nil(纯读场景不让步,写时再报错)。
func NewStore(pool *pgxpool.Pool, key *secrets.Key, logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{pool: pool, key: key, logger: logger}
}

// maskKey 把明文 key 收成 sk-***abcd(保留前缀 3 + 末 4)。长度不足时尽量保真。
func maskKey(plain string) string {
	if plain == "" {
		return ""
	}
	if len(plain) <= 6 {
		return strings.Repeat("*", len(plain))
	}
	return plain[:3] + strings.Repeat("*", len(plain)-7) + plain[len(plain)-4:]
}

// List 返回 6 个 provider 的当前配置(未配置的行以占位条目呈现)。
func (s *Store) List(ctx context.Context) ([]ProviderListItem, error) {
	if s.pool == nil {
		return nil, fmt.Errorf("providers store: no db pool")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT provider_name, api_key_encrypted, base_url, enabled,
		       updated_at, last_verified_at, last_error
		FROM he_api.provider_configs`)
	if err != nil {
		return nil, fmt.Errorf("providers store: query: %w", err)
	}
	defer rows.Close()

	byName := make(map[string]ProviderListItem, len(KnownProviders))
	for rows.Next() {
		var name, baseURL string
		var enc []byte
		var enabled bool
		var updatedAt, verifiedAt *time.Time
		var lastErr *string
		if err := rows.Scan(&name, &enc, &baseURL, &enabled, &updatedAt, &verifiedAt, &lastErr); err != nil {
			return nil, fmt.Errorf("providers store: scan: %w", err)
		}
		item := ProviderListItem{
			Name:      name,
			BaseURL:   baseURL,
			Enabled:   enabled,
			HasKey:    len(enc) > 0,
			VerifiedAt: isoOrNil(verifiedAt),
			UpdatedAt: isoOrNil(updatedAt),
			LastError: lastErr,
		}
		if len(enc) > 0 && s.key != nil {
			if pt, derr := s.key.Decrypt(enc); derr == nil {
				item.MaskedKey = maskKey(string(pt))
			}
		}
		byName[name] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("providers store: rows: %w", err)
	}

	// 补齐占位条目
	out := make([]ProviderListItem, 0, len(KnownProviders))
	for _, name := range KnownProviders {
		if it, ok := byName[name]; ok {
			out = append(out, it)
			continue
		}
		out = append(out, ProviderListItem{
			Name:    name,
			BaseURL: defaultBaseURL(name),
			Enabled: false,
			HasKey:  false,
		})
	}
	return out, nil
}

// Update 写入/更新一个 provider 的配置。
//
//	apiKey: 非空 → 加密入库(替换);空 → 保留已有 key(不覆盖)
//	baseURL: 非空 → 覆盖;空 → 用平台默认
//	enabled: 显式传(指针区分"未传"与 false)
func (s *Store) Update(ctx context.Context, name, apiKey, baseURL string, enabled *bool, userID *string) error {
	if s.pool == nil {
		return fmt.Errorf("providers store: no db pool")
	}
	if !validProvider(name) {
		return fmt.Errorf("providers store: unknown provider %q", name)
	}
	if s.key == nil {
		return fmt.Errorf("providers store: encryption key unavailable")
	}

	// 解析已有行(拿当前 key + base_url 作为 fallback)
	var curEnc []byte
	var curURL string
	err := s.pool.QueryRow(ctx,
		`SELECT api_key_encrypted, base_url FROM he_api.provider_configs WHERE provider_name=$1`,
		name).Scan(&curEnc, &curURL)
	exists := err == nil
	if err != nil && !strings.Contains(err.Error(), "no rows") {
		return fmt.Errorf("providers store: read current: %w", err)
	}

	finalEnc := curEnc
	if apiKey != "" {
		ct, eerr := s.key.Encrypt([]byte(apiKey))
		if eerr != nil {
			return fmt.Errorf("providers store: encrypt: %w", eerr)
		}
		finalEnc = ct
	}
	finalURL := baseURL
	if finalURL == "" {
		finalURL = curURL
	}
	if finalURL == "" {
		finalURL = defaultBaseURL(name)
	}

	if exists {
		if _, derr := s.pool.Exec(ctx, `
			UPDATE he_api.provider_configs
			SET api_key_encrypted=$2, base_url=$3, enabled=COALESCE($4, enabled),
			    updated_by_user_id=$5
			WHERE provider_name=$1`,
			name, finalEnc, finalURL, enabled, userID); derr != nil {
			return fmt.Errorf("providers store: update: %w", derr)
		}
		return nil
	}

	if _, ierr := s.pool.Exec(ctx, `
		INSERT INTO he_api.provider_configs
		    (provider_name, api_key_encrypted, base_url, enabled, updated_by_user_id)
		VALUES ($1, $2, $3, COALESCE($4, true), $5)`,
		name, finalEnc, finalURL, enabled, userID); ierr != nil {
		return fmt.Errorf("providers store: insert: %w", ierr)
	}
	return nil
}

// GetActive 返回明文配置给 adapter 内部端点(仅 127.0.0.1 绑定)。
func (s *Store) GetActive(ctx context.Context) ([]ProviderActive, error) {
	if s.pool == nil {
		return nil, fmt.Errorf("providers store: no db pool")
	}
	if s.key == nil {
		return nil, fmt.Errorf("providers store: encryption key unavailable")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT provider_name, api_key_encrypted, base_url, enabled
		FROM he_api.provider_configs
		WHERE enabled = true`)
	if err != nil {
		return nil, fmt.Errorf("providers store: query active: %w", err)
	}
	defer rows.Close()

	out := make([]ProviderActive, 0, 6)
	for rows.Next() {
		var name, baseURL string
		var enc []byte
		var enabled bool
		if err := rows.Scan(&name, &enc, &baseURL, &enabled); err != nil {
			return nil, fmt.Errorf("providers store: scan active: %w", err)
		}
		pt, derr := s.key.Decrypt(enc)
		if derr != nil {
			s.logger.Warn("decrypt provider key", slog.String("provider", name), slog.String("error", derr.Error()))
			continue
		}
		out = append(out, ProviderActive{
			Name:    name,
			APIKey:  string(pt),
			BaseURL: baseURL,
			Enabled: enabled,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("providers store: rows: %w", err)
	}
	return out, nil
}

// MarkVerified 写回连通性测试结果(UI "测试连接" 用)。
func (s *Store) MarkVerified(ctx context.Context, name, lastError string) error {
	if s.pool == nil {
		return fmt.Errorf("providers store: no db pool")
	}
	now := time.Now()
	if lastError != "" {
		now = time.Time{} // SQL 里用 NULL 表示未通过
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE he_api.provider_configs
		SET last_verified_at = CASE WHEN $2 = '' THEN NOW() ELSE NULL END,
		    last_error = NULLIF($2, '')
		WHERE provider_name = $1`,
		name, lastError); err != nil {
		return fmt.Errorf("providers store: mark verified: %w", err)
	}
	_ = now
	return nil
}

func isoOrNil(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func validProvider(name string) bool {
	for _, p := range KnownProviders {
		if p == name {
			return true
		}
	}
	return false
}

// defaultBaseURL 返回各 provider 的平台默认上游地址(与 adapter 的 *_UPSTREAM_BASE_URL 一致)。
func defaultBaseURL(name string) string {
	switch name {
	case "deepseek":
		return "https://api.deepseek.com"
	case "doubao":
		return "https://ark.cn-beijing.volces.com/api/v3"
	case "ernie":
		return "https://aip.baidubce.com"
	case "glm":
		return "https://open.bigmodel.cn/api/paas/v4"
	case "kimi":
		return "https://api.moonshot.cn/v1"
	case "qwen":
		return "https://dashscope.aliyuncs.com/compatible-mode/v1"
	default:
		return ""
	}
}
