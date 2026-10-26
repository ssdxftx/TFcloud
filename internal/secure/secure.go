package secure

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/chacha20poly1305"
)

var ErrInvalidKey = errors.New("APP_KEY 必须是 32 字节密钥")

// DecodeKey 兼容 PHP 版写入的 "base64:..." 形式。
func DecodeKey(appKey string) ([]byte, error) {
	raw := strings.TrimSpace(appKey)
	if strings.HasPrefix(raw, "base64:") {
		decoded, err := base64.StdEncoding.DecodeString(raw[7:])
		if err != nil {
			return nil, ErrInvalidKey
		}
		raw = string(decoded)
	}
	if len(raw) != chacha20poly1305.KeySize {
		return nil, ErrInvalidKey
	}
	return []byte(raw), nil
}

// Seal 使用 XChaCha20-Poly1305，与 PHP sodium_crypto_aead_xchacha20poly1305_ietf 字节级兼容。
func Seal(key, plaintext, aad []byte) (cipher, nonce []byte, err error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return aead.Seal(nil, nonce, plaintext, aad), nonce, nil
}

func Open(key, cipher, nonce, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, cipher, aad)
	if err != nil {
		return nil, errors.New("凭据无法解密")
	}
	return plain, nil
}

func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("系统随机源不可用: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func SHA256Hex(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// Fingerprint 复刻 PHP 侧逻辑：递归键排序 -> JSON -> HMAC-SHA256。
func Fingerprint(secret string, payload map[string]any) (string, error) {
	normalized := sortValue(payload)
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(normalized); err != nil {
		return "", err
	}
	body := bytes.TrimRight(buf.Bytes(), "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func sortValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(t))
		for _, k := range keys {
			out[k] = sortValue(t[k])
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = sortValue(item)
		}
		return out
	default:
		return v
	}
}

const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

// HashPassword 使用 argon2id，输出 PHC 字符串。
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword 同时支持存量 bcrypt（含 $2y$）与新的 argon2id。
func VerifyPassword(encoded, password string) bool {
	switch {
	case strings.HasPrefix(encoded, "$argon2id$"):
		return verifyArgon2id(encoded, password)
	case strings.HasPrefix(encoded, "$2y$"):
		// Go 的 bcrypt 不识别 $2y$，算法一致，仅版本标记不同。
		return bcrypt.CompareHashAndPassword([]byte("$2a$"+encoded[4:]), []byte(password)) == nil
	case strings.HasPrefix(encoded, "$2a$"), strings.HasPrefix(encoded, "$2b$"):
		return bcrypt.CompareHashAndPassword([]byte(encoded), []byte(password)) == nil
	default:
		return false
	}
}

func verifyArgon2id(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		return false
	}
	var memory uint32
	var timeCost uint32
	var threads uint8
	for _, field := range strings.Split(parts[3], ",") {
		kv := strings.SplitN(field, "=", 2)
		if len(kv) != 2 {
			return false
		}
		n, err := strconv.Atoi(kv[1])
		if err != nil {
			return false
		}
		switch kv[0] {
		case "m":
			memory = uint32(n)
		case "t":
			timeCost = uint32(n)
		case "p":
			threads = uint8(n)
		}
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, timeCost, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NeedsRehash 用于登录成功后透明升级旧哈希。
func NeedsRehash(encoded string) bool {
	return !strings.HasPrefix(encoded, "$argon2id$")
}

// RandomBytes 返回 n 字节密码学安全随机数。
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("系统随机源不可用: " + err.Error())
	}
	return b
}
