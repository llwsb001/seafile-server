// Package repocrypt reads the existing Seafile repository encryption formats.
package repocrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
)

// Decrypt removes Seafile block encryption and its PKCS#7 padding.
func Decrypt(version int, key, iv, input []byte) ([]byte, error) {
	if version == 3 {
		var aes128 [16]byte
		copy(aes128[:], key)
		key = aes128[:]
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	size := block.BlockSize()
	if len(input) == 0 || len(input)%size != 0 {
		return nil, errors.New("invalid encrypted data size")
	}
	out := make([]byte, len(input))
	if version == 3 {
		// Version 3 uses AES-128 ECB, including when unwrapping a repository key.
		for offset := 0; offset < len(input); offset += size {
			block.Decrypt(out[offset:offset+size], input[offset:offset+size])
		}
	} else {
		if len(iv) != size {
			return nil, errors.New("invalid IV size")
		}
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, input)
	}

	padding := int(out[len(out)-1])
	if padding == 0 || padding > size {
		return nil, errors.New("invalid padding size")
	}
	for _, b := range out[len(out)-padding:] {
		if int(b) != padding {
			return nil, errors.New("invalid padding bytes")
		}
	}
	return out[:len(out)-padding], nil
}
