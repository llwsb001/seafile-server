package seafserver

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"math"
	"strconv"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/internal/repocrypt"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/pbkdf2"
)

var legacyRepoSalt = [8]byte{0xda, 0x90, 0x45, 0xc3, 0x06, 0xc7, 0xcc, 0x26}

func unlockRepo(repo *repomgr.Repo, password string) (key, iv []byte, ok bool) {
	version := repo.EncVersion
	salt := legacyRepoSalt[:]
	if version >= 3 {
		var err error
		salt, err = hex.DecodeString(repo.Salt)
		if err != nil || len(salt) != 32 {
			return nil, nil, false
		}
	}

	credential := []byte(repo.ID + password)
	if repo.PwdHashAlgo == "" {
		magic, _ := deriveRepoKey(version, salt, credential)
		if !matchesHex(magic, repo.Magic) {
			return nil, nil, false
		}
	} else {
		// The legacy password hash pads its eight-byte salt with zero bytes.
		var hashSalt [32]byte
		copy(hashSalt[:], salt)
		hash := deriveRepoPasswordHash(repo.PwdHashAlgo, repo.PwdHashParams, hashSalt[:], credential)
		if hash == nil || !matchesHex(hash, repo.PwdHash) {
			return nil, nil, false
		}
	}

	key, iv = deriveRepoKey(version, salt, []byte(password))
	if version == 1 {
		return key, iv, true
	}
	wrapped, err := hex.DecodeString(repo.RandomKey)
	if err != nil || len(wrapped) != 48 {
		return nil, nil, false
	}
	secret, err := repocrypt.Decrypt(version, key, iv, wrapped)
	if err != nil || len(secret) != 32 {
		return nil, nil, false
	}
	defer clear(secret)
	key, iv = deriveRepoKey(version, salt, secret)
	return key, iv, true
}

func matchesHex(raw []byte, hexValue string) bool {
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(raw)), []byte(hexValue)) == 1
}

func deriveRepoKey(version int, salt, data []byte) (key, iv []byte) {
	if version == 1 {
		return deriveLegacyRepoKey(data, salt)
	}
	key = pbkdf2.Key(data, salt, 1000, 32, sha256.New)
	iv = pbkdf2.Key(key, salt, 10, 16, sha256.New)
	return key, iv
}

func deriveLegacyRepoKey(data, salt []byte) ([]byte, []byte) {
	// OpenSSL EVP_BytesToKey concatenates successive SHA-1 digests to
	// produce the AES-128 key and IV used by version 1 repositories.
	input := make([]byte, sha1.Size+len(data)+len(salt))
	copy(input[sha1.Size:], data)
	copy(input[sha1.Size+len(data):], salt)
	first := legacyRepoDigest(input[sha1.Size:])
	copy(input, first[:])
	second := legacyRepoDigest(input)
	var key, iv [16]byte
	copy(key[:], first[:16])
	copy(iv[:4], first[16:])
	copy(iv[4:], second[:])
	return key[:], iv[:]
}

func legacyRepoDigest(input []byte) [sha1.Size]byte {
	const rounds = 1 << 19
	digest := sha1.Sum(input)
	for i := 1; i < rounds; i++ {
		digest = sha1.Sum(digest[:])
	}
	return digest
}

func deriveRepoPasswordHash(algo, params string, salt, data []byte) []byte {
	if algo != "argon2id" {
		rounds := int32(positiveDecimal(params, 1000))
		if rounds <= 0 {
			rounds = 1000
		}
		return pbkdf2.Key(data, salt, int(rounds), 32, sha256.New)
	}
	timeCost, memory, lanes := int64(2), int64(102400), int64(8)
	if fields := strings.SplitN(params, ",", 3); len(fields) == 3 {
		timeCost = positiveDecimal(fields[0], timeCost)
		memory = positiveDecimal(fields[1], memory)
		lanes = positiveDecimal(fields[2], lanes)
	}
	if timeCost > math.MaxUint32 || memory > math.MaxUint32 || lanes > math.MaxUint8 || memory < 8*lanes {
		return nil
	}
	return argon2.IDKey(data, salt, uint32(timeCost), uint32(memory), uint8(lanes), 32)
}

// positiveDecimal follows the C configuration reader, which accepts leading
// whitespace and a decimal prefix and uses defaults for non-positive values.
func positiveDecimal(value string, fallback int64) int64 {
	value = strings.TrimLeft(value, " \t\n\v\f\r")
	value = strings.TrimPrefix(value, "+")
	end := 0
	for end < len(value) && '0' <= value[end] && value[end] <= '9' {
		end++
	}
	number, err := strconv.ParseInt(value[:end], 10, 64)
	if err != nil || number <= 0 {
		return fallback
	}
	return number
}
