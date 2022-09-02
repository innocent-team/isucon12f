package main

import (
	"context"
	"encoding/json"
	"fmt"
)

func userPresentHisotryKey(userID int64) string {
	return fmt.Sprintf("user_present_history:%d", userID)
}
func userPresentHisotryDeletedKey(userID int64) string {
	return fmt.Sprintf("user_present_history_deleted:%d", userID)
}
func userPresentHistoryHashKey(presentID int64) string {
	return fmt.Sprintf("user_present_history_hash:%d", presentID)
}
func userPresentHistoryValue(present *UserPresentAllReceivedHistory) ([]byte, error) {
	bytes, err := json.Marshal(present)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}
func parseUserPresentHistory(str string) (*UserPresentAllReceivedHistory, error) {
	var present UserPresentAllReceivedHistory
	err := json.Unmarshal([]byte(str), &present)
	if err != nil {
		return nil, err
	}
	return &present, nil
}
func (h *Handler) setUserPresentHistories(c context.Context, userID int64, presents []*UserPresentAllReceivedHistory) error {

	if len(presents) == 0 {
		return nil
	}
	var values []interface{}
	for _, present := range presents {
		bytes, err := userPresentHistoryValue(present)
		if err != nil {
			return err
		}
		// 1つだけ元データにあるDeletedを保持する
		if present.DeletedAt != nil {
			_, err := h.Redis.HMSet(c, userPresentHisotryDeletedKey(userID), userPresentHisotryDeletedKey(present.PresentAllID), bytes).Result()
			if err != nil {
				return err
			}
			continue
		}
		values = append(values, userPresentHistoryHashKey(present.PresentAllID), bytes)
	}

	_, err := h.Redis.HSet(c, userPresentHisotryKey(userID), values...).Result()

	if err != nil {
		return err
	}
	return nil
}

func (h *Handler) getUserPresentHistory(c context.Context, userID int64, presentIDs []int64) ([]*UserPresentAllReceivedHistory, error) {
	err := h.initUserPresentHisotry(c, userID)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for _, presentID := range presentIDs {
		keys = append(keys, userPresentHistoryHashKey(presentID))
	}
	histories, err := h.Redis.HMGet(c, userPresentHisotryKey(userID), keys...).Result()
	if err != nil {
		return nil, err
	}
	var presents []*UserPresentAllReceivedHistory
	for _, history := range histories {
		if history == nil {
			continue
		}
		present, err := parseUserPresentHistory(history.(string))
		if err != nil {
			return nil, err
		}
		presents = append(presents, present)
	}
	return presents, nil
}

func (h *Handler) getAllUserPresentHistory(c context.Context, userID int64) ([]*UserPresentAllReceivedHistory, error) {
	err := h.initUserPresentHisotry(c, userID)
	if err != nil {
		return nil, err
	}
	histories, err := h.Redis.HGetAll(c, userPresentHisotryKey(userID)).Result()
	if err != nil {
		return nil, err
	}
	// Deletedもadminのために入れてあげる
	deleted, err := h.Redis.HGetAll(c, userPresentHisotryDeletedKey(userID)).Result()
	if err != nil {
		return nil, err
	}
	for _, d := range deleted {
		histories[userPresentHisotryDeletedKey(userID)] = d
	}
	var presents []*UserPresentAllReceivedHistory
	for _, history := range histories {
		present, err := parseUserPresentHistory(history)
		if err != nil {
			return nil, err
		}
		presents = append(presents, present)
	}
	return presents, nil
}

func (h *Handler) initUserPresentHisotry(c context.Context, userID int64) error {
	l, err := h.Redis.HLen(c, userPresentHisotryKey(userID)).Result()
	if err != nil {
		return err
	}
	if l == 0 {
		var histories []*UserPresentAllReceivedHistory
		query := "SELECT * FROM user_present_all_received_history WHERE user_id=?"
		err = h.chooseUserDB(userID).SelectContext(c, &histories, query, userID)
		if err != nil {
			return err
		}
		err = h.setUserPresentHistories(c, userID, histories)
		if err != nil {
			return err
		}
	}
	return nil
}
