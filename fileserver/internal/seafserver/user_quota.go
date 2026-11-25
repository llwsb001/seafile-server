package seafserver

import (
	"context"
	"database/sql"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/option"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
)

const infiniteQuota = -2

// UserQuota reads user quotas and usage recorded in the SQLite database.
type UserQuota struct {
	db     *sql.DB
	config *ServerConfig
}

// NewUserQuota uses the existing database and server configuration.
func NewUserQuota(db *sql.DB, config *ServerConfig) *UserQuota {
	return &UserQuota{db: db, config: config}
}

func (q *UserQuota) get(ctx context.Context, user string) int64 {
	var quota sql.NullInt64
	const query = "SELECT CAST(quota AS INTEGER) FROM UserQuota WHERE user = ?"
	if err := q.db.QueryRowContext(ctx, query, user).Scan(&quota); err == nil && quota.Valid && quota.Int64 > 0 {
		return quota.Int64
	}
	value, ok := q.config.String(ctx, "quota", "default")
	if !ok {
		return infiniteQuota
	}
	return option.ParseSQLiteDefaultQuota(value)
}

// SQLiteDefaultQuotaOverride reads the management setting used by upload checks.
func SQLiteDefaultQuotaOverride(ctx context.Context, db *sql.DB) (int64, bool) {
	value, ok := databaseConfigValue(ctx, db, "quota", "default").string()
	if !ok {
		return 0, false
	}
	return option.ParseSQLiteDefaultQuota(value), true
}

const quotaUsageJoin = " FROM RepoOwner o LEFT JOIN VirtualRepo v ON o.repo_id = v.repo_id, RepoSize" +
	" WHERE o.repo_id = RepoSize.repo_id AND v.repo_id IS NULL"

func (q *UserQuota) usage(ctx context.Context, user string) (int64, error) {
	var usage int64
	const query = "SELECT COALESCE(CAST(SUM(size) AS INTEGER), 0)" + quotaUsageJoin + " AND owner_id = ?"
	err := q.db.QueryRowContext(ctx, query, user).Scan(&usage)
	return usage, err
}

type userQuotaUsage struct {
	User  string `json:"user"`
	Usage int64  `json:"usage"`
}

func (q *UserQuota) listUsage(ctx context.Context) ([]userQuotaUsage, error) {
	const query = "SELECT owner_id, COALESCE(CAST(SUM(size) AS INTEGER), 0)" + quotaUsageJoin +
		" AND owner_id IS NOT NULL GROUP BY owner_id"
	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []userQuotaUsage
	for rows.Next() {
		var usage userQuotaUsage
		if err := rows.Scan(&usage.User, &usage.Usage); err != nil {
			return nil, err
		}
		users = append(users, usage)
	}
	return users, rows.Err()
}

func (q *UserQuota) check(ctx context.Context, repoID string, delta int64) int {
	// A virtual library's upload consumes the quota of the origin's owner.
	origin := repoID
	var upstream string
	const findOrigin = "SELECT origin_repo FROM VirtualRepo WHERE repo_id = ?"
	if err := q.db.QueryRowContext(ctx, findOrigin, repoID).Scan(&upstream); err == nil {
		origin = upstream
	}
	owner, err := repomgr.LoadRepoOwner(ctx, q.db, origin)
	if err != nil {
		return -1
	}
	owner = asciiLower(owner)
	if strings.Contains(owner, "dtable@seafile") {
		return 0
	}
	quota := q.get(ctx, owner)
	if quota == infiniteQuota {
		return 0
	}
	usage, err := q.usage(ctx, owner)
	if err != nil || usage < 0 || usage+delta >= quota {
		return -1
	}
	return 0
}
