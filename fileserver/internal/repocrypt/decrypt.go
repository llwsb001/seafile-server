// Package repocrypt implements the existing Seafile repository encryption formats.
package repocrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
)

func newBlock(version int, key []byte) (cipher.Block, error) {
	if version == 3 {
		var aes128 [16]byte
		copy(aes128[:], key)
		key = aes128[:]
	}
	return aes.NewCipher(key)
}

// Encrypt encodes a Seafile block with the repository's key and PKCS#7 padding.
func Encrypt(version int, key, iv, input []byte) ([]byte, error) {
	block, err := newBlock(version, key)
	if err != nil {
		return nil, err
	}
	size := block.BlockSize()
	padding := size - len(input)%size
	out := make([]byte, len(input)+padding)
	copy(out, input)
	for index := len(input); index < len(out); index++ {
		out[index] = byte(padding)
	}
	if version == 3 {
		for offset := 0; offset < len(out); offset += size {
			block.Encrypt(out[offset:offset+size], out[offset:offset+size])
		}
	} else {
		if len(iv) != size {
			return nil, errors.New("invalid IV size")
		}
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, out)
	}
	return out, nil
}

// Decrypt removes Seafile block encryption and its PKCS#7 padding.
func Decrypt(version int, key, iv, input []byte) ([]byte, error) {
	block, err := newBlock(version, key)
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
