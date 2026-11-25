package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const errCCNetInternal = 500

// RPCHandlers exposes the read-only ccnet identity RPCs.
func (s *Identity) RPCHandlers() map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"get_emailuser":     s.getEmailUserRPC,
		"get_group":         s.getGroupRPC,
		"get_groups":        s.getGroupsRPC,
		"get_group_members": s.getGroupMembersRPC,
		"is_group_user":     s.isGroupUserRPC,
		"get_org_by_id":     s.getOrgByIDRPC,
		"org_user_exists":   s.orgUserExistsRPC,
	}
}

func intArgument(args []json.RawMessage, index int) int {
	var value int64
	if index < len(args) {
		// Searpc's Jansson marshaller gives missing and noninteger values zero.
		if err := json.Unmarshal(args[index], &value); err != nil {
			return 0
		}
	}
	return int(int32(value))
}

func (s *Identity) getEmailUserRPC(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	email := stringArgument(args, 0)
	if email == nil {
		return nil, rpcError(errCCNetInternal, "Email can not be NULL")
	}
	user, err := s.emailUser(ctx, *email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("get_emailuser: %v", err)
		return nil, rpcError(errCCNetInternal, "Database error")
	}
	return user, nil
}

func (s *Identity) getGroupRPC(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	group, err := s.group(ctx, intArgument(args, 0))
	return identityResult("get_group", group, err), nil
}

func (s *Identity) getGroupsRPC(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	user := stringArgument(args, 0)
	if user == nil {
		return nil, rpcError(errCCNetInternal, "User name can not be NULL")
	}
	groups, err := s.groupsByUser(ctx, *user, intArgument(args, 1) != 0)
	return identityResult("get_groups", groups, err), nil
}

func (s *Identity) getGroupMembersRPC(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	members, err := s.groupMembers(ctx, intArgument(args, 0), intArgument(args, 1), intArgument(args, 2))
	return identityResult("get_group_members", members, err), nil
}

func (s *Identity) isGroupUserRPC(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	groupID, user := intArgument(args, 0), stringArgument(args, 1)
	if groupID < 0 || user == nil {
		return 0, rpcError(errCCNetInternal, "Bad arguments")
	}
	match, err := s.isGroupUser(ctx, groupID, *user, intArgument(args, 2) != 0)
	if err != nil {
		log.Printf("is_group_user: %v", err)
		return 0, nil
	}
	return match, nil
}

func (s *Identity) getOrgByIDRPC(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	orgID := intArgument(args, 0)
	if orgID <= 0 {
		return nil, rpcError(errCCNetInternal, "Bad arguments")
	}
	org, err := s.organization(ctx, orgID)
	return identityResult("get_org_by_id", org, err), nil
}

func (s *Identity) orgUserExistsRPC(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	orgID, email := intArgument(args, 0), stringArgument(args, 1)
	if orgID < 0 || email == nil {
		return -1, rpcError(errCCNetInternal, "Bad arguments")
	}
	match, err := s.orgUserExists(ctx, orgID, *email)
	if err != nil {
		log.Printf("org_user_exists: %v", err)
		return 0, nil
	}
	return match, nil
}

// C returns null for unavailable object and list results.
func identityResult(method string, value any, err error) any {
	if err == nil {
		return value
	}
	if !errors.Is(err, sql.ErrNoRows) {
		log.Printf("%s: %v", method, err)
	}
	return nil
}
