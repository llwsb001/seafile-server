package seafserver

import (
	"context"
	"database/sql"
	"errors"
)

func (q *UserQuota) shareUsage(ctx context.Context, identity *Identity, user string) (int64, error) {
	const owned = "SELECT repo_id FROM RepoOwner WHERE owner_id = ?"
	repos, err := quotaRows[string](ctx, q.db, owned, user)
	if err != nil {
		// The C owner lookup returns an empty list when its query fails.
		return 0, nil
	}
	var total int64
	for _, repoID := range repos {
		count, err := q.shareRecipients(ctx, identity, user, repoID)
		if err != nil {
			return -1, err
		}
		if count == 0 {
			continue
		}
		var size int64
		const query = "SELECT COALESCE(CAST(size AS INTEGER), 0) FROM RepoSize WHERE repo_id = ?"
		if err := q.db.QueryRowContext(ctx, query, repoID).Scan(&size); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return -1, err
		}
		usage := size * int64(count)
		if size < 0 || usage < 0 {
			return -1, errors.New("invalid repository share usage")
		}
		total += usage
	}
	return total, nil
}

func (q *UserQuota) shareRecipients(ctx context.Context, identity *Identity, user, repoID string) (int, error) {
	recipients := make(map[string]struct{})
	const personal = "SELECT to_email FROM SharedRepo WHERE from_email = ? AND repo_id = ?"
	if users, err := quotaRows[string](ctx, q.db, personal, user, repoID); err == nil {
		for _, email := range users {
			recipients[asciiLower(email)] = struct{}{}
		}
	}
	const groupIDs = "SELECT COALESCE(CAST(group_id AS INTEGER), 0) FROM RepoGroup WHERE repo_id = ?"
	if groups, err := quotaRows[int64](ctx, q.db, groupIDs, repoID); err == nil {
		for _, id := range groups {
			members, err := identity.groupMembers(ctx, int(int32(id)), -1, -1)
			if err != nil {
				return 0, err
			}
			if len(members) == 0 {
				return 0, errors.New("shared group has no members")
			}
			for _, member := range members {
				if member.User != nil {
					recipients[*member.User] = struct{}{}
				}
			}
		}
	}
	delete(recipients, user)
	return len(recipients), nil
}

func quotaRows[T string | int64](ctx context.Context, db *sql.DB, query string, args ...any) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []T
	for rows.Next() {
		var value T
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
