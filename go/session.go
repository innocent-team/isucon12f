package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-redis/redis/v8"
	"github.com/labstack/echo/v4"
)

func userSessionKey(userID int64) string {
	return fmt.Sprintf("user_session:%d", userID)
}
func sessionValue(sessionID string, expired_at int64) string {
	return fmt.Sprintf("%s:%d", sessionID, expired_at)
}
func parseSessionValue(redisValue string) (string, int64, error) {
	s := strings.Split(redisValue, ":")
	i, err := strconv.ParseInt(s[1], 10, 64)
	if err != nil {
		return "", -1, err
	}
	return s[0], i, nil
}
func (h *Handler) newUserSession(c echo.Context, userID int64, sessionID string, expired_at int64) error {
	ctx := c.Request().Context()
	_, err := h.Redis.Set(ctx, userSessionKey(userID), sessionValue(sessionID, expired_at), 0).Result()
	if err != nil {
		return err
	}
	return nil
}

var ErrMissingSession = fmt.Errorf("missing session")

func (h *Handler) getUserSession(c echo.Context, userID int64) (string, int64, error) {
	ctx := c.Request().Context()
	r, err := h.Redis.Get(ctx, userSessionKey(userID)).Result()
	if err != nil {
		if err == redis.Nil {
			return "", -1, ErrMissingSession
		}
		return "", -1, err
	}
	return parseSessionValue(r)
}

func (h *Handler) deleteUserSessoin(c echo.Context, userID int64) error {
	ctx := c.Request().Context()
	_, err := h.Redis.Del(ctx, userSessionKey(userID)).Result()
	if err != nil {
		return err
	}
	return nil
}
