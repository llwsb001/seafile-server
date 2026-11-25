package seafserver

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// GLib's ASCII whitespace excludes vertical tab. strtol also skips vertical tab.
const keyFileSpace = " \t\r\n\f"
const decimalSpace = keyFileSpace + "\v"

type configKey struct {
	group string
	key   string
}

// ServerConfig reads SeafileConf overrides and falls back to seafile.conf.
type ServerConfig struct {
	db   *sql.DB
	file map[configKey]string
}

// NewServerConfig uses the startup contents of seafile.conf for file fallbacks.
func NewServerConfig(db *sql.DB, data []byte) *ServerConfig {
	// Keep the raw values. GKeyFile treats trailing whitespace differently
	// for integers, int64s and booleans, and decodes escapes only for strings.
	values := make(map[configKey]string)
	var group string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimLeft(strings.TrimSuffix(line, "\r"), keyFileSpace)
		if line == "" || line[0] == '#' {
			continue
		}
		if header := strings.TrimRight(line, keyFileSpace); header[0] == '[' && header[len(header)-1] == ']' {
			group = header[1 : len(header)-1]
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok && group != "" {
			key = strings.TrimRight(key, keyFileSpace)
			values[configKey{group, key}] = strings.TrimLeft(cString(value), keyFileSpace)
		}
	}
	return &ServerConfig{db: db, file: values}
}

type configValue struct {
	text         string
	found        bool
	fromDatabase bool
}

func (c *ServerConfig) value(ctx context.Context, group, key string) configValue {
	group, key = cString(group), cString(key)
	if value := databaseConfigValue(ctx, c.db, group, key); value.found {
		return value
	}
	text, ok := c.file[configKey{group, key}]
	return configValue{text, ok, false}
}

func databaseConfigValue(ctx context.Context, db *sql.DB, group, key string) configValue {
	const query = "SELECT value FROM SeafileConf WHERE cfg_group = ? AND cfg_key = ?"
	var value sql.NullString
	err := db.QueryRowContext(ctx, query, group, key).Scan(&value)
	if err == nil && value.Valid {
		return configValue{strings.Trim(cString(value.String), keyFileSpace), true, true}
	}
	return configValue{}
}

func cString(value string) string {
	text, _, _ := strings.Cut(value, "\x00")
	return text
}

// String reads a setting as a string. An empty override is a present value.
func (c *ServerConfig) String(ctx context.Context, group, key string) (string, bool) {
	return c.value(ctx, group, key).string()
}

func (v configValue) string() (string, bool) {
	if v.fromDatabase {
		return v.text, true
	}
	if !v.found || !utf8.ValidString(v.text) {
		return "", false
	}
	return strings.Trim(keyFileString(v.text), keyFileSpace), true
}

func keyFileString(value string) string {
	var decoded strings.Builder
	for len(value) > 0 {
		before, rest, escaped := strings.Cut(value, "\\")
		decoded.WriteString(before)
		if !escaped || rest == "" {
			break
		}
		switch rest[0] {
		case 's':
			decoded.WriteByte(' ')
		case 'n':
			decoded.WriteByte('\n')
		case 'r':
			decoded.WriteByte('\r')
		case 't':
			decoded.WriteByte('\t')
		case '\\':
			decoded.WriteByte('\\')
		default:
			decoded.WriteByte('\\')
			decoded.WriteByte(rest[0])
		}
		value = rest[1:]
	}
	return decoded.String()
}

func (v configValue) int() int {
	if v.fromDatabase {
		return int(int32(v.int64()))
	}
	// g_key_file_get_integer accepts suffixes beginning with GLib whitespace.
	token := strings.TrimLeft(v.text, decimalSpace)
	if i := strings.IndexAny(token, keyFileSpace); i >= 0 {
		token = token[:i]
	}
	value, err := strconv.ParseInt(token, 10, 32)
	if !v.found || err != nil {
		return -1
	}
	return int(value)
}

func (v configValue) int64() int64 {
	if !v.found {
		return -1
	}
	// For an empty DB override, strtoll performs no conversion and leaves
	// the end pointer at the empty string. An empty GKeyFile value is invalid.
	if v.fromDatabase && v.text == "" {
		return 0
	}
	token := strings.TrimLeft(v.text, decimalSpace)
	value, err := strconv.ParseInt(token, 10, 64)
	if err == nil {
		return value
	}
	// strtoll saturates on overflow. The C configuration manager ignores errno.
	if !errors.Is(err, strconv.ErrRange) {
		return -1
	}
	// ParseInt reports a range error before examining trailing bytes.
	if token[0] == '-' || token[0] == '+' {
		token = token[1:]
	}
	if strings.Trim(token, "0123456789") != "" {
		return -1
	}
	return value
}

func (v configValue) boolean() int {
	if v.fromDatabase {
		if v.text == "true" {
			return 1
		}
		return 0
	}
	if text := strings.TrimRight(v.text, keyFileSpace); text == "true" || text == "1" {
		return 1
	}
	return 0
}
