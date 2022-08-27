package main

import (
	"database/sql"
	"fmt"
	"math/rand"
	"net/http"

	"github.com/labstack/echo/v4"
)

var localGachaMasters = LocalGachaMasters{GachaMaster: map[int]LocalGachaMaster{}}

type LocalGachaMasters struct {
	GachaMaster map[int]LocalGachaMaster
}
type LocalGachaMaster struct {
	ID    int64
	Name  string
	Items LocalGachaItem
}
type LocalGachaItem struct {
	ItemType int
	ItemID   int
	Amount   int
	Weight   int
}

func (l *LocalGachaMasters) Refresh() error {
	return nil
}

func (l *LocalGachaMasters) List(c echo.Context, h *Handler, requestAt int64) ([]*GachaData, error) {
	ctx := c.Request().Context()

	gachaMasterList := []*GachaMaster{}
	query := "SELECT * FROM gacha_masters WHERE start_at <= ? AND end_at >= ? ORDER BY display_order ASC"
	err := h.DB.SelectContext(ctx, &gachaMasterList, query, requestAt, requestAt)
	if err != nil {
		return nil, errorResponse(c, http.StatusInternalServerError, err)
	}

	if len(gachaMasterList) == 0 {
		return []*GachaData{}, nil
	}

	// ガチャ排出アイテム取得
	gachaDataList := make([]*GachaData, 0)
	query = "SELECT * FROM gacha_item_masters WHERE gacha_id=? ORDER BY id ASC"
	for _, v := range gachaMasterList {
		var gachaItem []*GachaItemMaster
		err = h.DB.SelectContext(ctx, &gachaItem, query, v.ID)
		if err != nil {
			return nil, errorResponse(c, http.StatusInternalServerError, err)
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
	err := localGachaMasters.Refresh()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, ErrGetRequestTime)
	}
	return successResponse(c, "ok")
}
