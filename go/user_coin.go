package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

func userIsucoinKey(userID int64) string {
	return fmt.Sprintf("user_isucoin:%d", userID)
}

func (h *Handler) getUser(ctx context.Context, userID int64) (*User, error) {
	fmt.Printf("[User] getUser: %d\n", userID)
	var user User
	query := "SELECT * FROM users WHERE id=?"
	if err := h.chooseUserDB(userID).GetContext(ctx, &user, query, userID); err != nil {
		if err == sql.ErrNoRows {
			fmt.Printf("[User] getUser: %d Not Found\n", userID)
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	h.initUserIsucoin(ctx, userID)
	coin, err := h.getUserIsucoin(ctx, userID)
	fmt.Printf("[User] getUser: %d Coin=%d\n", userID, coin)
	if err != nil {
		return nil, err
	}
	user.IsuCoin = coin
	fmt.Printf("[User] getUser: %d User=%v\n", userID, user)
	return &user, nil
}

func (h *Handler) getUserIsucoin(ctx context.Context, userID int64) (int64, error) {
	h.initUserIsucoin(ctx, userID)
	coin, err := h.Redis.Get(ctx, userIsucoinKey(userID)).Result()
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(coin, 10, 64)
}

func (h *Handler) giveUserIsucoin(ctx context.Context, userID int64, amount int64) error {
	h.initUserIsucoin(ctx, userID)
	_, err := h.Redis.IncrBy(ctx, userIsucoinKey(userID), amount).Result()
	if err != nil {
		return err
	}
	return nil
}

func (h *Handler) initUserIsucoin(ctx context.Context, userID int64) error {
	fmt.Printf("[Coin] initUserIsucoin: %d\n", userID)
	ok, err := h.Redis.Exists(ctx, userIsucoinKey(userID)).Result()
	if err != nil {
		return err
	}
	if ok == 1 {
		fmt.Printf("[Coin] initUserIsucoin: %d Hit\n", userID)
		return nil
	}

	var coin int64
	query := "SELECT isu_coin FROM users WHERE id=?"
	if err := h.chooseUserDB(userID).GetContext(ctx, &coin, query, userID); err != nil {
		if err == sql.ErrNoRows {
			fmt.Printf("[Coin] initUserIsucoin: %d Not Found\n", userID)
			return ErrUserNotFound
		}
		return err
	}
	fmt.Printf("[Coin] initUserIsucoin: %d Found %+v\n", userID, coin)

	_, err = h.Redis.Set(ctx, userIsucoinKey(userID), coin, 0).Result()
	if err != nil {
		return err
	}
	fmt.Printf("[Coin] initUserIsucoin: %d Set %+v\n", userID, coin)
	return nil
}
