package secure

import (
	"encoding/base64"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestDecodeKey(t *testing.T) {
	raw := "0123456789abcdef0123456789abcdef"
	if _, err := DecodeKey(raw); err != nil {
		t.Fatalf("32 字节明文密钥应可用: %v", err)
	}
	if _, err := DecodeKey("base64:" + base64Of(raw)); err != nil {
		t.Fatalf("base64: 前缀密钥应可用: %v", err)
	}
	if _, err := DecodeKey("short"); err == nil {
		t.Fatal("短密钥必须报错")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	key, _ := DecodeKey("0123456789abcdef0123456789abcdef")
	cipherText, nonce, err := Seal(key, []byte(`{"bduss":"secret"}`), []byte("tieba:1:cookie"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Open(key, cipherText, nonce, []byte("tieba:1:cookie"))
	if err != nil {
		t.Fatalf("正常解密失败: %v", err)
	}
	if string(plain) != `{"bduss":"secret"}` {
		t.Fatalf("明文不一致: %s", plain)
	}
	if _, err := Open(key, cipherText, nonce, []byte("tieba:2:cookie")); err == nil {
		t.Fatal("AAD 不匹配必须解密失败")
	}
	if len(nonce) != 24 {
		t.Fatalf("XChaCha20 nonce 应为 24 字节，实际 %d", len(nonce))
	}
}

func TestVerifyPasswordBCryptPHPFormat(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("passw0rd!"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	phpStyle := "$2y$" + string(hash)[4:]
	if !VerifyPassword(phpStyle, "passw0rd!") {
		t.Fatal("PHP 的 $2y$ 前缀哈希必须能校验通过")
	}
	if VerifyPassword(phpStyle, "wrong") {
		t.Fatal("错误密码不应通过")
	}
}

func TestArgon2idRoundTrip(t *testing.T) {
	hash, err := HashPassword("s3cret-pass")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("新哈希必须是 argon2id: %s", hash)
	}
	if !VerifyPassword(hash, "s3cret-pass") {
		t.Fatal("argon2id 校验失败")
	}
	if VerifyPassword(hash, "nope") {
		t.Fatal("错误密码不应通过")
	}
	if !NeedsRehash("$2y$10$abcdefghijklmnopqrstuv") {
		t.Fatal("bcrypt 哈希应标记为需要重算")
	}
	if NeedsRehash(hash) {
		t.Fatal("argon2id 不应重算")
	}
}

func base64Of(v string) string { return base64.StdEncoding.EncodeToString([]byte(v)) }
