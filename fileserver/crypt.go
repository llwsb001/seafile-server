package main

import "github.com/haiwen/seafile-server/fileserver/internal/repocrypt"

type seafileCrypt struct {
	key     []byte
	iv      []byte
	version int
}

func (crypt *seafileCrypt) encrypt(input []byte) ([]byte, error) {
	return repocrypt.Encrypt(crypt.version, crypt.key, crypt.iv, input)
}

func (crypt *seafileCrypt) decrypt(input []byte) ([]byte, error) {
	return repocrypt.Decrypt(crypt.version, crypt.key, crypt.iv, input)
}
