package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-redis/redis/v8"
	"github.com/labstack/echo/v4"
)

func userCardKey(userID int64) string {
	return fmt.Sprintf("user_card:%d", userID)
}
func userCardHashKey(cardID int64) string {
	return fmt.Sprintf("user_card_hash:%d", cardID)
}
func userCardValue(card *UserCard) ([]byte, error) {
	bytes, err := json.Marshal(card)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}
func parseUserCardValue(bytes []byte) (*UserCard, error) {
	var card UserCard
	err := json.Unmarshal(bytes, &card)
	if err != nil {
		return nil, err
	}
	return &card, nil
}

func (h *Handler) setUserCard(ctx context.Context, card *UserCard) error {
	bytes, err := userCardValue(card)
	if err != nil {
		return nil
	}

	_, err = h.Redis.HSet(ctx, userCardKey(card.UserID), userCardHashKey(card.ID), bytes).Result()

	if err != nil {
		return err
	}
	return nil
}

var ErrRedisMissing = fmt.Errorf("redis missing")

func (h *Handler) getUserCards(ctx echo.Context, userID int64) ([]*UserCard, error) {
	c := ctx.Request().Context()
	err := h.initRedisCard(ctx, userID)
	if err != nil {
		return nil, err
	}
	cards, err := h.Redis.HGetAll(c, userCardKey(userID)).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, ErrRedisMissing
		}
		return nil, err
	}
	res := []*UserCard{}
	for _, card := range cards {
		c, err := parseUserCardValue([]byte(card))
		if err != nil {
			return nil, err
		}
		res = append(res, c)
	}
	return res, nil
}

func (h *Handler) getUserCardByIDs(ctx echo.Context, userID int64, id int64) (*UserCard, error) {
	c := ctx.Request().Context()
	err := h.initRedisCard(ctx, userID)
	if err != nil {
		return nil, err
	}
	card, err := h.Redis.HGet(c, userCardKey(userID), userCardHashKey(id)).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, ErrRedisMissing
		}
		return nil, err
	}
	ca, err := parseUserCardValue([]byte(card))
	if err != nil {
		return nil, err
	}
	return ca, nil
}

func (h *Handler) initRedisCard(ctx echo.Context, userID int64) error {
	c := ctx.Request().Context()
	l, err := h.Redis.HLen(c, userCardKey(userID)).Result()
	if err != nil {
		return err
	}
	if l > 0 {
		return nil
	}
	db := h.chooseUserDB(userID)
	var cards []*UserCard
	err = db.SelectContext(c, &cards, `SELECT * FROM user_cards WHERE user_id = ?`, userID)
	if err != nil {
		return err
	}
	for _, card := range cards {
		err = h.setUserCard(c, card)
		if err != nil {
			return err
		}
	}
	return nil
}
