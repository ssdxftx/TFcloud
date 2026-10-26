package plugins

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strconv"
	"strings"
	"time"

	"tfcloud/internal/sign"
)

func firstText(values ...any) string {
	for _, v := range values {
		if s := strings.TrimSpace(sign.ValueString(v)); s != "" {
			return s
		}
	}
	return ""
}

func itoa(n int) string { return strconv.Itoa(n) }

func truncate(v string, max int) string {
	runes := []rune(v)
	if len(runes) <= max {
		return v
	}
	return string(runes[:max])
}

func timeNowISO() string { return time.Now().Format(time.RFC3339) }

func dateToday() string { return time.Now().Format("2006-01-02") }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nowMillis() int64 { return time.Now().UnixNano() / int64(time.Millisecond) }

func marshalJSON(v any) (string, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return string(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

func uuidV4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// aesCBCEncryptBase64 复刻 PHP openssl_encrypt($data, 'aes-128-cbc', $key, 0, $iv)：
// PKCS#7 填充 + 输出 Base64。
func aesCBCEncryptBase64(plain, key, iv []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	padded := pkcs7Pad(plain, block.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return base64.StdEncoding.EncodeToString(out), nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	return append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
}

// rsaEncryptHex 复刻 PHP openssl_public_encrypt(OPENSSL_PKCS1_PADDING) + bin2hex。
func rsaEncryptHex(publicKeyB64, value string) (string, error) {
	clean := strings.Join(strings.Fields(publicKeyB64), "")
	der, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return "", fmt.Errorf("公钥解析失败: %w", err)
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return "", fmt.Errorf("公钥解析失败: %w", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("公钥类型不正确")
	}
	enc, err := rsa.EncryptPKCS1v15(rand.Reader, rsaPub, []byte(value))
	if err != nil {
		return "", fmt.Errorf("加密失败: %w", err)
	}
	return hex.EncodeToString(enc), nil
}

func pemPublicKey(b64 string) string {
	var b strings.Builder
	b.WriteString("-----BEGIN PUBLIC KEY-----\n")
	clean := strings.Join(strings.Fields(b64), "")
	for i := 0; i < len(clean); i += 64 {
		end := i + 64
		if end > len(clean) {
			end = len(clean)
		}
		b.WriteString(clean[i:end])
		b.WriteString("\n")
	}
	b.WriteString("-----END PUBLIC KEY-----\n")
	return b.String()
}

var _ = pem.Decode
var _ = sha256.Sum256

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func cipherNewCBC(block cipher.Block, iv []byte) cipher.BlockMode {
	return cipher.NewCBCEncrypter(block, iv)
}
