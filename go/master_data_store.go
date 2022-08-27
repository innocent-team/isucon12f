package main

import (
	"database/sql"
	"fmt"
	"math/rand"
	"net/http"

	"github.com/labstack/echo/v4"
)

var localGachaMasters = LocalGachaMasters{
	GachaMasterByID:        map[int64]*GachaMaster{},
	GachaMasters:           []*GachaMaster{},
	GachaItemListByGachaID: map[int64][]*GachaItemMaster{},
	GachaItemList:          []*GachaItemMaster{},
}

type LocalGachaMasters struct {
	GachaMasterByID        map[int64]*GachaMaster
	GachaMasters           []*GachaMaster
	GachaItemListByGachaID map[int64][]*GachaItemMaster
	GachaItemList          []*GachaItemMaster
}

// ガチャのマスターデータのキャッシュを更新する
func (l *LocalGachaMasters) Refresh(c echo.Context, h *Handler) error {
	ctx := c.Request().Context()

	gachaMasterList := []*GachaMaster{}
	query := "SELECT * FROM gacha_masters ORDER BY display_order ASC"
	err := h.DB.SelectContext(ctx, &gachaMasterList, query)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	l.GachaMasters = gachaMasterList
	for _, gachaMaster := range gachaMasterList {
		l.GachaMasterByID[gachaMaster.ID] = gachaMaster
	}

	var gachaItemList []*GachaItemMaster
	query = "SELECT * FROM gacha_item_masters ORDER BY id ASC"
	err = h.DB.SelectContext(ctx, &gachaItemList, query)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	l.GachaItemList = gachaItemList
	for _, gachaItem := range gachaItemList {
		l.GachaItemListByGachaID[gachaItem.ID] = append(l.GachaItemListByGachaID[gachaItem.ID], gachaItem)
	}

	return nil
}

func (l *LocalGachaMasters) List(c echo.Context, h *Handler, requestAt int64) ([]*GachaData, error) {
	var gachaMasterList []*GachaMaster
	for _, gachaMaster := range l.GachaMasters {
		if !(gachaMaster.StartAt <= requestAt && gachaMaster.EndAt >= requestAt) {
			continue
		}
		gachaMasterList = append(gachaMasterList, gachaMaster)
	}

	if len(gachaMasterList) == 0 {
		return []*GachaData{}, nil
	}

	// ガチャ排出アイテム取得
	gachaDataList := make([]*GachaData, 0)
	for _, v := range gachaMasterList {
		var gachaItem []*GachaItemMaster
		gachaItem, ok := l.GachaItemListByGachaID[v.ID]
		if !ok {
			return nil, errorResponse(c, http.StatusInternalServerError, fmt.Errorf("invalid gacha item (v.ID = %d)", v.ID))
		}

		if len(gachaItem) == 0 {
			return nil, errorResponse(c, http.StatusNotFound, fmt.Errorf("not found gacha item"))
		}

		gachaDataList = append(gachaDataList, &GachaData{
			Gacha:     v,
			GachaItem: gachaItem,
		})
	}
	return gachaDataList, nil
}

// 返り値1つ目はガチャ名
func (l *LocalGachaMasters) Pick(c echo.Context, h *Handler, gachaID string, gachaCount int64, requestAt int64) (string, []*GachaItemMaster, error) {
	ctx := c.Request().Context()

	// gachaIDからガチャマスタの取得
	query := "SELECT * FROM gacha_masters WHERE id=? AND start_at <= ? AND end_at >= ?"
	gachaInfo := new(GachaMaster)
	if err := h.DB.GetContext(ctx, gachaInfo, query, gachaID, requestAt, requestAt); err != nil {
		if sql.ErrNoRows == err {
			return "", nil, errorResponse(c, http.StatusNotFound, fmt.Errorf("not found gacha"))
		}
		return "", nil, errorResponse(c, http.StatusInternalServerError, err)
	}

	// gachaItemMasterからアイテムリスト取得
	gachaItemList := make([]*GachaItemMaster, 0)
	err := h.DB.SelectContext(ctx, &gachaItemList, "SELECT * FROM gacha_item_masters WHERE gacha_id=? ORDER BY id ASC", gachaID)
	if err != nil {
		return "", nil, errorResponse(c, http.StatusInternalServerError, err)
	}
	if len(gachaItemList) == 0 {
		return "", nil, errorResponse(c, http.StatusNotFound, fmt.Errorf("not found gacha item"))
	}

	// weightの合計値を算出
	var sum int64
	err = h.DB.GetContext(ctx, &sum, "SELECT SUM(weight) FROM gacha_item_masters WHERE gacha_id=?", gachaID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil, errorResponse(c, http.StatusNotFound, err)
		}
		return "", nil, errorResponse(c, http.StatusInternalServerError, err)
	}

	// random値の導出 & 抽選
	result := make([]*GachaItemMaster, 0, gachaCount)
	for i := 0; i < int(gachaCount); i++ {
		random := rand.Int63n(sum)
		boundary := 0
		for _, v := range gachaItemList {
			boundary += v.Weight
			if random < int64(boundary) {
				result = append(result, v)
				break
			}
		}
	}
	return gachaInfo.Name, result, nil
}

// POST /gacha/refresh
func (h *Handler) refreshGacha(c echo.Context) error {
	//TODO for update master
	err := localGachaMasters.Refresh(c, h)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, ErrGetRequestTime)
	}
	return successResponse(c, "ok")
}