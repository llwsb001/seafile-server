package seafserver

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

const accountPasswordIterations = 10000

func newAccountPassword(password string) (string, error) {
	if password == "!" {
		return password, nil
	}
	var salt [sha256.Size]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	hash := pbkdf2.Key([]byte(password), salt[:], accountPasswordIterations, sha256.Size, sha256.New)
	return fmt.Sprintf("PBKDF2SHA256$%d$%x$%x", accountPasswordIterations, salt, hash), nil
}

func checkAccountPassword(password, stored string) (match, upgrade bool) {
	if password == "" || password == "!" || stored == "!" {
		return false, false
	}
	switch len(stored) {
	case sha1.Size * 2:
		hash := sha1.Sum([]byte(password))
		return hmac.Equal([]byte(stored), []byte(hex.EncodeToString(hash[:]))), true
	case sha256.Size * 2:
		legacySalt := [...]byte{0xdb, 0x91, 0x45, 0xc3, 0x06, 0xc7, 0xcc, 0x26}
		hash := sha256.Sum256(append([]byte(password), legacySalt[:]...))
		return hmac.Equal([]byte(stored), []byte(hex.EncodeToString(hash[:]))), true
	}
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "PBKDF2SHA256" {
		return false, false
	}
	iterations, err := strconv.ParseInt(parts[1], 10, 32)
	if err != nil || iterations <= 0 {
		return false, false
	}
	salt, err := hex.DecodeString(parts[2])
	if err != nil || len(salt) != sha256.Size {
		return false, false
	}
	hash := pbkdf2.Key([]byte(password), salt, int(iterations), sha256.Size, sha256.New)
	return hmac.Equal([]byte(parts[3]), []byte(hex.EncodeToString(hash))), false
}
