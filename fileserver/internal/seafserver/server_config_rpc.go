package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"strconv"
	"unicode/utf8"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

type configType uint8

const (
	configString configType = iota
	configInt
	configInt64
	configBoolean
)

// RPCHandlers exposes the configuration management RPCs for an enabled server.
func (c *ServerConfig) RPCHandlers(db *sql.DB) map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"get_server_config_string":  c.getRPC(configString),
		"get_server_config_int":     c.getRPC(configInt),
		"get_server_config_int64":   c.getRPC(configInt64),
		"get_server_config_boolean": c.getRPC(configBoolean),
		"set_server_config_string":  setConfigRPC(db, configString),
		"set_server_config_int":     setConfigRPC(db, configInt),
		"set_server_config_int64":   setConfigRPC(db, configInt64),
		"set_server_config_boolean": setConfigRPC(db, configBoolean),
	}
}

func (c *ServerConfig) getRPC(kind configType) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		group, key := stringArgument(args, 0), stringArgument(args, 1)
		if group == nil || key == nil {
			err := rpcError(errBadArgs, "Argument should not be null")
			if kind == configString {
				return nil, err
			}
			return -1, err
		}
		value := c.value(ctx, *group, *key)
		switch kind {
		case configString:
			if text, ok := value.string(); ok {
				if !utf8.ValidString(text) {
					return searpc.NoReturn{}, nil
				}
				return text, nil
			}
			return nil, nil
		case configInt:
			return value.int(), nil
		case configInt64:
			return value.int64(), nil
		default:
			return value.boolean(), nil
		}
	}
}

func setConfigRPC(db *sql.DB, kind configType) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		group, key := stringArgument(args, 0), stringArgument(args, 1)
		if group == nil || key == nil {
			return -1, rpcError(errBadArgs, "Argument should not be null")
		}
		var value string
		switch kind {
		case configString:
			arg := stringArgument(args, 2)
			if arg == nil {
				return -1, rpcError(errBadArgs, "Argument should not be null")
			}
			value = cString(*arg)
			// The C setter stores at most 255 bytes in its fixed-size buffer.
			value = value[:min(len(value), 255)]
		case configInt:
			value = strconv.Itoa(intArgument(args, 2))
		case configInt64:
			value = strconv.FormatInt(int64Argument(args, 2), 10)
		case configBoolean:
			value = strconv.FormatBool(intArgument(args, 2) != 0)
		}
		if err := writeConfig(ctx, db, cString(*group), cString(*key), value); err != nil {
			log.Printf("SQLite configuration write: %v", err)
			return -1, nil
		}
		return 0, nil
	}
}

func writeConfig(ctx context.Context, db *sql.DB, group, key, value string) error {
	_, err := withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		const lookup = "SELECT EXISTS (SELECT 1 FROM SeafileConf WHERE cfg_group = ? AND cfg_key = ?)"
		var exists bool
		if err := conn.QueryRowContext(ctx, lookup, group, key).Scan(&exists); err != nil {
			return false, err
		}
		query := "INSERT INTO SeafileConf (value, cfg_group, cfg_key, property) VALUES (?, ?, ?, 0)"
		if exists {
			query = "UPDATE SeafileConf SET value = ? WHERE cfg_group = ? AND cfg_key = ?"
		}
		_, err := conn.ExecContext(ctx, query, value, group, key)
		return err == nil, err
	})
	return err
}
