// AD-004 —— Provider API key 静态加密(secrets/aesgcm.go)。
//
// 用于加密 he_api.provider_configs.api_key_encrypted 列。算法 AES-256-GCM
// (NIST SP 800-38D),与 auth-svc internal/kms/local.go 同型 ——
//
//	ciphertext = [12B nonce] || [N bytes body] || [16B GCM tag]
//
// 密钥 32 字节,启动时从 /opt/he-api/secrets/provider-encryption.key 读入(env 不放密钥,
// 避免泄漏到 /proc/<pid>/environ)。生成:
//
//	openssl rand 32 > /opt/he-api/secrets/provider-encryption.key
//	chmod 0600 /opt/he-api/secrets/provider-encryption.key
//	chown he-api:he-api /opt/he-api/secrets/provider-encryption.key
//
// 并发安全:cipher.AEAD.Seal / Open 内部无共享可变状态。
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
)

// KeyBytes 是 AES-256 主密钥的字节长度(NIST SP 800-38D)。
const KeyBytes = 32

// NonceLen 是 GCM 推荐的 96-bit nonce 长度。
const nonceLen = 12

// ErrKeyUnavailable 启动期不可恢复错误(文件缺失/权限/长度错),fail-closed。
var ErrKeyUnavailable = errors.New("provider encryption key unavailable")

// ErrCiphertextInvalid 密文长度异常(被截断或篡改)。
var ErrCiphertextInvalid = errors.New("ciphertext invalid")

// ErrAuthFailed GCM tag 校验失败(密钥错或被篡改);统一不区分错误以免泄漏信息。
var ErrAuthFailed = errors.New("ciphertext authentication failed")

// Key 是不可导出的 32 字节主密钥。导出 Encrypted / Decrypt 两个方法即可。
type Key struct {
	gcm cipher.AEAD
}

// LoadKey 从 path 读取 32 字节二进制密钥,返回 *Key 或 ErrKeyUnavailable。
// 设计选择:故意只支持原始 32 字节文件(base64 编码易被误读);文档里给出生成命令。
func LoadKey(path string) (*Key, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", ErrKeyUnavailable, path, err)
	}
	if len(raw) != KeyBytes {
		return nil, fmt.Errorf("%w: key file must be exactly %d bytes, got %d", ErrKeyUnavailable, KeyBytes, len(raw))
	}
	return NewKey(raw)
}

// NewKey 直接接受 32 字节切片(便于测试与未来从 Vault 注入)。
func NewKey(raw []byte) (*Key, error) {
	if len(raw) != KeyBytes {
		return nil, fmt.Errorf("%w: must be %d bytes, got %d", ErrKeyUnavailable, KeyBytes, len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: aes: %v", ErrKeyUnavailable, err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: gcm: %v", ErrKeyUnavailable, err)
	}
	if gcm.NonceSize() != nonceLen {
		return nil, fmt.Errorf("%w: nonce size mismatch", ErrKeyUnavailable)
	}
	return &Key{gcm: gcm}, nil
}

// Encrypt 返回 nonce || ciphertext+tag(N+28 字节)。每次调用产生新随机 nonce。
func (k *Key) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("%w: nonce gen: %v", ErrKeyUnavailable, err)
	}
	out := make([]byte, nonceLen, nonceLen+len(plaintext)+k.gcm.Overhead())
	copy(out, nonce)
	out = k.gcm.Seal(out, nonce, plaintext, nil)
	return out, nil
}

// Decrypt 反向操作;tag 校验失败一律 ErrAuthFailed(不区分,以免泄漏区分信息)。
func (k *Key) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < nonceLen+k.gcm.Overhead() {
		return nil, ErrCiphertextInvalid
	}
	nonce := ciphertext[:nonceLen]
	body := ciphertext[nonceLen:]
	pt, err := k.gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, ErrAuthFailed
	}
	return pt, nil
}
