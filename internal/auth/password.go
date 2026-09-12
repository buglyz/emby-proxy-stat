// Package auth 提供登录密码校验（PBKDF2）、会话管理与登录限流。
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// 哈希格式: pbkdf2-sha256$<iterations>$<salt-b64>$<digest-b64>
const (
	HashAlgorithm  = "pbkdf2-sha256"
	HashIterations = 210000
)

// GeneratePasswordHash 为明文密码生成可存储的 PBKDF2-SHA256 哈希串。
func GeneratePasswordHash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	digest, err := pbkdf2.Key(sha256.New, password, salt, HashIterations, sha256.Size)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s$%d$%s$%s", HashAlgorithm, HashIterations,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(digest)), nil
}

// ParsePasswordHash 解析并校验哈希串的各组成部分。
func ParsePasswordHash(encoded string) (iterations int, salt, digest []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != HashAlgorithm {
		return 0, nil, nil, fmt.Errorf("unsupported password hash format")
	}
	iterations, err = strconv.Atoi(parts[1])
	if err != nil || iterations < 100000 || iterations > 1000000 {
		return 0, nil, nil, fmt.Errorf("invalid PBKDF2 iteration count")
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) < 16 {
		return 0, nil, nil, fmt.Errorf("invalid PBKDF2 salt")
	}
	digest, err = base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(digest) < sha256.Size {
		return 0, nil, nil, fmt.Errorf("invalid PBKDF2 digest")
	}
	return iterations, salt, digest, nil
}

// ValidatePasswordHash 用于配置加载时的快速失败校验。
func ValidatePasswordHash(encoded string) error {
	_, _, _, err := ParsePasswordHash(encoded)
	return err
}

// VerifyPassword 常数时间校验候选密码：优先使用哈希，未配置哈希时回退明文比较。
// storedHash 与 plainPassword 二选一（由配置校验保证）。
func VerifyPassword(candidate, storedHash, plainPassword string) bool {
	if storedHash != "" {
		iterations, salt, digest, err := ParsePasswordHash(storedHash)
		if err != nil {
			return false
		}
		actual, err := pbkdf2.Key(sha256.New, candidate, salt, iterations, len(digest))
		if err != nil {
			return false
		}
		return subtle.ConstantTimeCompare(actual, digest) == 1
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(plainPassword)) == 1
}

// GenerateToken 生成随机会话令牌。
func GenerateToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
