package main

import (
	"database/sql"
	"fmt"
	"math/rand"
	"net/http"
	"sync"

	"github.com/labstack/echo/v4"
)

var localGachaMasters = LocalGachaMasters{
	VersionMaster:           &VersionMaster{},
	GachaMasterByID:         map[int64]*GachaMaster{},
	GachaMasters:            []*GachaMaster{},
	GachaItemListByGachaID:  map[int64][]*GachaItemMaster{},
	GachaItemList:           []*GachaItemMaster{},
	GachaItemWeightSum:      map[int64]int64{},
	loginBonusRewardMasters: []*LoginBonusRewardMaster{},
}

type LocalGachaMasters struct {
	sync.RWMutex
	VersionMaster           *VersionMaster
	GachaMasterByID         map[int64]*GachaMaster
	GachaMasters            []*GachaMaster
	GachaItemListByGachaID  map[int64][]*GachaItemMaster
	GachaItemList           []*GachaItemMaster
	GachaItemWeightSum      map[int64]int64
	loginBonusRewardMasters []*LoginBonusRewardMaster
}

func (l *LocalGachaMasters) ListLoginBonusRewardMasters() []*LoginBonusRewardMaster {
	l.RLock()
	defer l.RUnlock()
	return l.loginBonusRewardMasters
}

// ガチャのマスターデータのキャッシュを更新する
func (l *LocalGachaMasters) Refresh(c echo.Context, h *Handler) error {
	l.Lock()
	defer l.Unlock()

	ctx := c.Request().Context()

	query := "SELECT * FROM version_masters WHERE status=1"
	masterVersion := new(VersionMaster)
	_db := h.DB
	if err := _db.GetContext(ctx, masterVersion, query); err != nil {
		if err == sql.ErrNoRows {
			//return errorResponse(c, http.StatusNotFound, fmt.Errorf("active master version is not found"))
			// DBのInitializeにミスってたときは、とりあえず諦める。
			fmt.Printf("[Gacha] Skip Refresh. version_masters is missing\n")
			return nil
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	l.VersionMaster = masterVersion

	if err := _db.SelectContext(ctx, &l.loginBonusRewardMasters, "SELECT * FROM login_bonus_reward_masters"); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// mapを初期化
	l.GachaMasterByID = map[int64]*GachaMaster{}
	l.GachaItemListByGachaID = map[int64][]*GachaItemMaster{}
	l.GachaItemWeightSum = map[int64]int64{}

	gachaMasterList := []*GachaMaster{}
	query = "SELECT * FROM gacha_masters ORDER BY display_order ASC"
	err := h.DB.SelectContext(ctx, &gachaMasterList, query)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	l.GachaMasters = gachaMasterList
	for _, gachaMaster := range gachaMasterList {
		l.GachaMasterByID[gachaMaster.ID] = gachaMaster
	}
	c.Logger().Printf("[Gacha] gachaListAll = %d", len(l.GachaMasters))

	var gachaItemList []*GachaItemMaster
	query = "SELECT * FROM gacha_item_masters ORDER BY id ASC"
	err = h.DB.SelectContext(ctx, &gachaItemList, query)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	l.GachaItemList = gachaItemList
	for _, gachaItem := range gachaItemList {
		l.GachaItemListByGachaID[gachaItem.GachaID] = append(l.GachaItemListByGachaID[gachaItem.GachaID], gachaItem)
	}

	for gachaID, gachaItems := range l.GachaItemListByGachaID {
		for _, gachaItem := range gachaItems {
			l.GachaItemWeightSum[gachaID] += int64(gachaItem.Weight)
		}
	}

	c.Logger().Printf("[Gacha] Updated: Version = %+v", l.VersionMaster)

	return nil
}

func (l *LocalGachaMasters) List(c echo.Context, h *Handler, requestAt int64) ([]*GachaData, error) {
	l.RLock()
	defer l.RUnlock()

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
func (l *LocalGachaMasters) Pick(c echo.Context, h *Handler, gachaID int64, gachaCount int64, requestAt int64) (string, []*GachaItemMaster, error) {
	l.RLock()
	defer l.RUnlock()

	// gachaIDからガチャマスタの取得
	gachaInfo, ok := l.GachaMasterByID[gachaID]
	if !ok || !(gachaInfo.StartAt <= requestAt && gachaInfo.EndAt >= requestAt) {
		return "", nil, errorResponse(c, http.StatusNotFound, fmt.Errorf("not found gacha"))
	}

	// gachaItemMasterからアイテムリスト取得
	gachaItemList, ok := l.GachaItemListByGachaID[gachaID]
	if !ok {
		return "", nil, errorResponse(c, http.StatusNotFound, fmt.Errorf("not found gacha item"))
	}
	if len(gachaItemList) == 0 {
		return "", nil, errorResponse(c, http.StatusNotFound, fmt.Errorf("not found gacha item"))
	}

	// weightの合計値を算出
	sum, ok := l.GachaItemWeightSum[gachaID]
	if !ok {
		return "", nil, errorResponse(c, http.StatusNotFound, fmt.Errorf("not found gacha item sum"))
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
	err := localGachaMasters.Refresh(c, h)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, ErrGetRequestTime)
	}
	return successResponse(c, "ok")
}

// 全てのアプリケーションのキャッシュ更新をフックする
func hookRefreshGacha() error {
	resp, err := http.Post("http://isucon1:8080/gacha/refresh", "application/x-www-form-urlencoded", nil)
	if err != nil {
		return fmt.Errorf("isucon1 refresh error: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("isucon1 refresh error: %s", resp.Status)
	}
	defer resp.Body.Close()
	resp, err = http.Post("http://isucon5:8080/gacha/refresh", "application/x-www-form-urlencoded", nil)
	if err != nil {
		return fmt.Errorf("isucon5 refresh error: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("isucon1 refresh error: %s", resp.Status)
	}
	defer resp.Body.Close()
	return nil
}
