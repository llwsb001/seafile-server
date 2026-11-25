package main

// 加密库（enc_version=2）读路径。全部常量取自 common/seafile-crypt.c：
//
//	KEYGEN_ITERATION2 = 1000
//	password -> PBKDF2-HMAC-SHA256(passwd, salt8, 1000, 32)      => key
//	          -> PBKDF2-HMAC-SHA256(key32, salt8, 10, 16)        => iv
//	random_key(48 字节，十六进制存于 commit 的 key 字段) 用 AES-256-CBC 解出 32 字节 secret
//	secret -> 同上两步派生出 enc_key / enc_iv
//	block 用 AES-256-CBC 加密（带 PKCS7 填充），block ID = SHA1(密文)
//
// salt 是源码里的固定数组（v2 专用；v3+ 才改成每库随机 salt）。

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

const KeygenIteration2 = 1000

var fixedSalt = []byte{0xda, 0x90, 0x45, 0xc3, 0x06, 0xc7, 0xcc, 0x26}

// PBKDF2 是 RFC 2898 的标准实现（只支持 HMAC-SHA256）。
func PBKDF2(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hLen := prf.Size()
	blocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, blocks*hLen)

	var buf [4]byte
	for i := 1; i <= blocks; i++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(buf[:], uint32(i))
		prf.Write(buf[:])
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for n := 1; n < iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for k := range t {
				t[k] ^= u[k]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

// DeriveKeyV2 派生 enc_version=2 的 key/iv。
func DeriveKeyV2(secret []byte) (key, iv []byte) {
	key = PBKDF2(secret, fixedSalt, KeygenIteration2, 32)
	iv = PBKDF2(key, fixedSalt, 10, 16)
	return key, iv
}

// DecryptRandomKey 用口令解出 32 字节 secret。
func DecryptRandomKey(passwd, randomKeyHex string) ([]byte, error) {
	if randomKeyHex == "" {
		return nil, errors.New("commit 里没有 key 字段")
	}
	enc, err := hex.DecodeString(randomKeyHex)
	if err != nil {
		return nil, fmt.Errorf("random_key 非十六进制: %w", err)
	}
	if len(enc) != 48 {
		return nil, fmt.Errorf("random_key 长度应为 48 字节，实际 %d", len(enc))
	}
	key, iv := DeriveKeyV2([]byte(passwd))
	return DecryptCBC(key, iv, enc)
}

// DecryptCBC 做 AES-256-CBC 解密并去掉 PKCS7 填充。
func DecryptCBC(key, iv, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("密文长度 %d 不是 %d 的整数倍", len(ciphertext), aes.BlockSize)
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)

	n := int(plain[len(plain)-1])
	if n == 0 || n > aes.BlockSize || n > len(plain) {
		return nil, fmt.Errorf("PKCS7 填充非法: %d", n)
	}
	for _, b := range plain[len(plain)-n:] {
		if int(b) != n {
			return nil, errors.New("PKCS7 填充字节不一致")
		}
	}
	return plain[:len(plain)-n], nil
}

// RepoKey 是解密后的仓库密钥材料。
type RepoKey struct {
	Key    []byte
	IV     []byte
	Secret []byte
}

// UnlockRepo 从 commit 的 enc_version/magic/key 解出仓库密钥。
func UnlockRepo(passwd string, encVersion int, randomKeyHex string) (*RepoKey, error) {
	if encVersion != 2 {
		return nil, fmt.Errorf("只实现了 enc_version=2，当前 %d", encVersion)
	}
	secret, err := DecryptRandomKey(passwd, randomKeyHex)
	if err != nil {
		return nil, err
	}
	if len(secret) != 32 {
		return nil, fmt.Errorf("secret 长度应为 32，实际 %d", len(secret))
	}
	key, iv := DeriveKeyV2(secret)
	return &RepoKey{Key: key, IV: iv, Secret: secret}, nil
}

// DecryptBlock 解密一个块。returnPlain=false 时只做校验用。
func (k *RepoKey) DecryptBlock(ciphertext []byte) ([]byte, error) {
	return DecryptCBC(k.Key, k.IV, ciphertext)
}
