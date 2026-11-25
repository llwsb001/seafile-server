package main

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestPBKDF2Vector(t *testing.T) {
	// RFC 7914 / 常见测试向量：PBKDF2-HMAC-SHA256("password","salt",1,32)
	got := hex.EncodeToString(PBKDF2([]byte("password"), []byte("salt"), 1, 32))
	want := "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"
	if got != want {
		t.Fatalf("PBKDF2 不符: %s", got)
	}
}

func TestDecryptCBCRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	iv := make([]byte, 16)
	for i := range key {
		key[i] = byte(i)
	}
	for i := range iv {
		iv[i] = byte(0x10 + i)
	}
	// 手工造一个 PKCS7 填充后的密文，再解回来
	plain := []byte("seafile fixture")
	enc, err := encryptCBCForTest(key, iv, plain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecryptCBC(key, iv, enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("解密结果不符: %q", got)
	}
}

// TestCommitID 用 fixtures v1 里的真实 commit 做的固定向量。
func TestCommitID(t *testing.T) {
	c := Commit{
		RootID:      "03f0ea2e462d9e38a3a53b49fb311f47c4f0496f",
		Creator:     "0000000000000000000000000000000000000000",
		Description: "Added \"secret.txt\"",
		Ctime:       1795604198,
	}
	name := "fixture@local"
	c.CreatorName = &name
	if got := ComputeCommitID(c); got != "b2ed381319d495fccf787cc40124eb9598496c8a" {
		t.Fatalf("commit id 不符: %s", got)
	}
}

// TestCanonicalDirObject 用 fixtures v1 里一个真实目录对象验证 jansson 口径。
func TestCanonicalDirObject(t *testing.T) {
	raw := []byte(`{"dirents": [{"id": "c65318f38c91fbf142126ad780bec8779137bb6b", "mode": 16384, "mtime": 1795604198, "name": "secret"}], "type": 3, "version": 1}`)
	canon, err := CanonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(canon) != `{"dirents": [{"id": "c65318f38c91fbf142126ad780bec8779137bb6b", "mode": 16384, "mtime": 1795604198, "name": "secret"}], "type": 3, "version": 1}` {
		t.Fatalf("规范化输出不符:\n%s", canon)
	}
	if got := Sha1Hex(canon); got != "03f0ea2e462d9e38a3a53b49fb311f47c4f0496f" {
		t.Fatalf("目录对象 id 不符: %s", got)
	}
}

func TestObjectIDFromPath(t *testing.T) {
	if got := ObjectID("/x/storage/fs/abc/03/f0ea2e"); got != "03f0ea2e" {
		t.Fatalf("对象 id 拼接不符: %s", got)
	}
}

func TestIsDirMode(t *testing.T) {
	if !IsDirMode(16384) || IsDirMode(33188) {
		t.Fatal("mode 判断不符")
	}
}
