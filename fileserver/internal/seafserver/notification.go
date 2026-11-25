package seafserver

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/haiwen/seafile-server/fileserver/option"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

// CheckNotificationConfig validates enabled notification settings at startup.
func CheckNotificationConfig() error {
	_, _, err := notificationConfig()
	return err
}

func notificationConfig() (url, key string, err error) {
	url = option.NotificationURLFromEnv()
	if url == "" {
		return "", "", nil
	}
	key = os.Getenv("JWT_PRIVATE_KEY")
	if key == "" {
		return "", "", errors.New("notification requires JWT_PRIVATE_KEY")
	}
	return url, key, nil
}

// NotifyRepoUpdate is the external notification hook for a committed branch update.
// Call after commit and handle any delivery failure separately from the write.
func NotifyRepoUpdate(ctx context.Context, repoID, commitID string) error {
	url, key, err := notificationConfig()
	if err != nil || url == "" {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return utils.SendRepoUpdate(ctx, url, key, repoID, commitID)
}
