package seafserver

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const errGeneral = 500

// SyncTokenRPCHandlers manages persistent repository credentials in SQLite.
func (r *Reader) SyncTokenRPCHandlers(db *sql.DB) map[string]searpc.Handler {
	tokens := syncTokens{reader: r, db: db}
	return map[string]searpc.Handler{
		"seafile_generate_repo_token":           tokens.generate,
		"seafile_delete_repo_token":             tokens.deleteOne,
		"seafile_list_repo_tokens":              tokens.listRepo,
		"seafile_list_repo_tokens_by_email":     tokens.listEmail,
		"seafile_delete_repo_tokens_by_peer_id": tokens.deletePeer,
		"delete_repo_tokens_by_email":           tokens.deleteEmail,
	}
}

type syncTokens struct {
	reader *Reader
	db     *sql.DB
}

type syncTokenInfo struct {
	RepoID   string  `json:"repo-id"`
	RepoName string  `json:"repo-name"`
	Owner    *string `json:"repo-owner"`
	Email    *string `json:"email"`
	Token    *string `json:"token"`
	PeerID   *string `json:"peer-id"`
	PeerIP   *string `json:"peer-ip"`
	PeerName *string `json:"peer-name"`
	SyncTime int64   `json:"sync-time"`
	Version  *string `json:"client-ver"`
}

func missingTokenArguments() *searpc.RPCError {
	return rpcError(errBadArgs, "Arguments should not be empty")
}

func (s syncTokens) generate(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repo, email := stringArgument(args, 0), stringArgument(args, 1)
	if repo == nil || email == nil {
		return nil, missingTokenArguments()
	}
	if !validRepoID(*repo) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	var bytes [20]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return nil, rpcError(errGeneral, "Token generation failed")
	}
	token := hex.EncodeToString(bytes[:])
	if _, err := s.db.ExecContext(ctx,
		"INSERT INTO RepoUserToken (repo_id, email, token) VALUES (?, ?, ?)", *repo, *email, token); err != nil {
		return nil, rpcError(errGeneral, "DB error")
	}
	return token, nil
}

func (s syncTokens) listRepo(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repo := stringArgument(args, 0)
	if repo == nil {
		return nil, missingTokenArguments()
	}
	if !validRepoID(*repo) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	var exists bool
	if err := s.reader.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM Repo WHERE repo_id = ?)", *repo).Scan(&exists); err != nil {
		return nil, rpcError(errGeneral, "DB error")
	}
	if !exists {
		return nil, nil
	}
	return s.list(ctx, "u.repo_id = ? AND o.repo_id = ?", *repo, *repo)
}

func (s syncTokens) listEmail(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	email := stringArgument(args, 0)
	if email == nil {
		return nil, missingTokenArguments()
	}
	return s.list(ctx, "u.email = ? AND u.repo_id = o.repo_id", *email)
}

func (s syncTokens) list(ctx context.Context, where string, args ...any) (any, *searpc.RPCError) {
	rows, err := s.reader.db.QueryContext(ctx, `SELECT u.repo_id, o.owner_id, u.email, u.token,
		p.peer_id, p.peer_ip, p.peer_name, p.sync_time, p.client_ver
		FROM RepoUserToken u LEFT JOIN RepoTokenPeerInfo p ON u.token = p.token, RepoOwner o
		WHERE `+where, args...)
	if err != nil {
		return nil, rpcError(errGeneral, "DB error")
	}
	defer rows.Close()

	var items []syncTokenInfo
	for rows.Next() {
		var item syncTokenInfo
		var syncTime sql.NullInt64
		if err := rows.Scan(&item.RepoID, &item.Owner, &item.Email, &item.Token,
			&item.PeerID, &item.PeerIP, &item.PeerName, &syncTime, &item.Version); err != nil {
			return nil, rpcError(errGeneral, "DB error")
		}
		if item.Owner != nil {
			*item.Owner = asciiLower(*item.Owner)
		}
		if item.Email != nil {
			*item.Email = asciiLower(*item.Email)
		}
		item.SyncTime = syncTime.Int64
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, rpcError(errGeneral, "DB error")
	}
	for index := range items {
		item := &items[index]
		item.RepoName = "Unknown"
		if repo, err := s.reader.Repo(ctx, item.RepoID); err == nil {
			item.RepoName = repo.Name
		}
	}
	return items, nil
}

func (s syncTokens) deleteOne(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repo, token, email := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
	if repo == nil || token == nil || email == nil {
		return -1, missingTokenArguments()
	}
	if !validRepoID(*repo) {
		return -1, rpcError(errBadArgs, "Invalid repo id")
	}
	deleted, err := s.revoke(ctx, "repo_id = ? AND token = ? AND lower(email) = ?", *repo, *token, *email)
	if err != nil {
		return -1, rpcError(errGeneral, "DB error")
	}
	if !deleted {
		return -1, rpcError(errGeneral, "Permission denied")
	}
	return 0, nil
}

func (s syncTokens) deletePeer(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	email, peer := stringArgument(args, 0), stringArgument(args, 1)
	if email == nil || peer == nil {
		return -1, missingTokenArguments()
	}
	if len(*peer) != 40 || strings.ContainsFunc(*peer, func(c rune) bool {
		return !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z')
	}) {
		return -1, rpcError(errBadArgs, "invalid peer id")
	}
	_, err := s.revoke(ctx, "email = ? AND token IN (SELECT token FROM RepoTokenPeerInfo WHERE peer_id = ?)", *email, *peer)
	if err != nil {
		return -1, rpcError(errInternal, "DB error")
	}
	return 0, nil
}

func (s syncTokens) deleteEmail(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	email := stringArgument(args, 0)
	if email == nil {
		return -1, missingTokenArguments()
	}
	if _, err := s.revoke(ctx, "email = ?", *email); err != nil {
		return -1, rpcError(errInternal, "DB error")
	}
	return 0, nil
}

func (s syncTokens) revoke(ctx context.Context, where string, args ...any) (bool, error) {
	return withSQLiteWrite(ctx, s.db, func(conn *sql.Conn) (bool, error) {
		rows, err := conn.QueryContext(ctx, "DELETE FROM RepoUserToken WHERE "+where+" RETURNING token", args...)
		if err != nil {
			return false, err
		}
		defer rows.Close()

		var deleted []sql.NullString
		for rows.Next() {
			var token sql.NullString
			if err := rows.Scan(&token); err != nil {
				return false, err
			}
			deleted = append(deleted, token)
		}
		if err := rows.Err(); err != nil {
			return false, err
		}
		for _, token := range deleted {
			if _, err := conn.ExecContext(ctx, `DELETE FROM RepoTokenPeerInfo WHERE token = ?
				AND NOT EXISTS (SELECT 1 FROM RepoUserToken WHERE token = ?)`, token, token); err != nil {
				return false, err
			}
		}
		return len(deleted) != 0, nil
	})
}
