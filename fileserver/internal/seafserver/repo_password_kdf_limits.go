package seafserver

import "strings"

// canCreateRepoPasswordHash bounds work requested by new encryption metadata.
// Existing repositories continue to use the legacy derivation rules.
func canCreateRepoPasswordHash(algorithm, params string) bool {
	const maxPBKDF2Rounds = 10_000_000
	const maxArgonMemory = 256 * 1024 // KiB
	const maxArgonWork = 1024 * 1024  // KiB-passes

	switch algorithm {
	case "pbkdf2_sha256":
		rounds := int32(positiveDecimal(params, 1000))
		return rounds <= maxPBKDF2Rounds
	case "argon2id":
		passes, memory, lanes := int64(2), int64(102400), int64(8)
		if fields := strings.SplitN(params, ",", 3); len(fields) == 3 {
			passes = positiveDecimal(fields[0], passes)
			memory = positiveDecimal(fields[1], memory)
			lanes = positiveDecimal(fields[2], lanes)
		}
		return lanes <= 255 && memory >= 8*lanes && memory <= maxArgonMemory && passes <= maxArgonWork/memory
	default:
		return false
	}
}
