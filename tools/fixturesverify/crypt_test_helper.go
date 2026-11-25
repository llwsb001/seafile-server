package main

// 仅测试用：AES-256-CBC + PKCS7 加密，用来验证解密路径往返。
// 生产代码只有解密需求，故不导出。

import (
	"crypto/aes"
	"crypto/cipher"
)

func encryptCBCForTest(key, iv, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	n := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytesRepeat(byte(n), n)...)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out, nil
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
