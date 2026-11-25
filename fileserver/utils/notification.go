package utils

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
)

type notificationEvent struct {
	Type    string     `json:"type"`
	Content repoUpdate `json:"content"`
}

type repoUpdate struct {
	RepoID   string `json:"repo_id"`
	CommitID string `json:"commit_id"`
}

// SendRepoUpdate sends the committed branch update to notification-server.
func SendRepoUpdate(ctx context.Context, url, key, repoID, commitID string) error {
	if key == "" {
		return errors.New("notification requires JWT_PRIVATE_KEY")
	}
	event := notificationEvent{Type: "repo-update", Content: repoUpdate{RepoID: repoID, CommitID: commitID}}
	msg, err := json.Marshal(event)
	if err != nil {
		return err
	}
	claims := jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(300 * time.Second))}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(key))
	if err != nil {
		return fmt.Errorf("sign repository update token: %w", err)
	}
	header := map[string][]string{"Authorization": {"Token " + token}}
	_, _, err = httpCommon(ctx, http.MethodPost, url+"/events", header, bytes.NewReader(msg))
	return err
}
