// Package credcrypto 提供应用层凭证加密（AES-256-GCM），用于把交易所 API 密钥
// 以密文形式落库，避免 DB 被拖库即等于泄露真实账户密钥。
//
// 密文格式：前缀 "enc:v1:" + base64(nonce || ciphertext||tag)。
// 未配置主密钥时 Cipher 为 nil，Encrypt/Decrypt 退化为明文透传（仅供本地开发），
// 且 Decrypt 对无前缀的历史明文原样返回，便于灰度迁移。
package credcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const prefix = "enc:v1:"

// Cipher 封装一个 AES-256-GCM AEAD。零值不可用，请用 New/NewFromEnv 构造。
type Cipher struct {
	aead cipher.AEAD
}

// New 用一段 32 字节密钥构造 Cipher。
func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("credential encryption key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// FromString 解析一段密钥字符串：接受 64 位 hex 或 44 字节 base64（标准/URL 均可），
// 二者都解码为 32 字节。空串返回 (nil, nil) 表示「未配置」。
func FromString(s string) (*Cipher, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if key, err := hex.DecodeString(s); err == nil && len(key) == 32 {
		return New(key)
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if key, err := enc.DecodeString(s); err == nil && len(key) == 32 {
			return New(key)
		}
	}
	return nil, errors.New("CREDENTIAL_ENC_KEY must decode to 32 bytes (64-char hex or base64)")
}

// Encrypt 加密明文，返回带前缀的密文。c 为 nil 时原样返回明文（开发模式）。
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	if c == nil {
		return plaintext, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密带前缀的密文。无前缀（历史明文）原样返回；c 为 nil 时也原样返回。
func (c *Cipher) Decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, prefix) {
		return value, nil // 历史明文 / 开发模式写入
	}
	if c == nil {
		return "", errors.New("encrypted credential present but CREDENTIAL_ENC_KEY not configured")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		return "", err
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	plain, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
