package main

import (
	"database/sql"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

var localGachaMasters = LocalGachaMasters{
	VersionMaster:          &VersionMaster{},
	GachaMasterByID:        map[int64]*GachaMaster{},
	GachaMasters:           []*GachaMaster{},
	GachaItemListByGachaID: map[int64][]*GachaItemMaster{},
	GachaItemList:          []*GachaItemMaster{},
	GachaItemWeightSum:     map[int64]int64{},
	Items:                  []*ItemMaster{},
	ItemByID:               map[int64]*ItemMaster{},
	LoginBonuses:           []*LoginBonusMaster{},
	LoginBonusRewards:      []*LoginBonusRewardMaster{},
	PresentAll:             []*PresentAllMaster{},
	AtInitOnVersion1:       time.Time{},
}

type LocalGachaMasters struct {
	sync.RWMutex
	VersionMaster          *VersionMaster
	GachaMasterByID        map[int64]*GachaMaster
	GachaMasters           []*GachaMaster
	GachaItemListByGachaID map[int64][]*GachaItemMaster
	GachaItemList          []*GachaItemMaster
	GachaItemWeightSum     map[int64]int64
	Items                  []*ItemMaster
	ItemByID               map[int64]*ItemMaster
	LoginBonuses           []*LoginBonusMaster
	LoginBonusRewards      []*LoginBonusRewardMaster
	PresentAll             []*PresentAllMaster
	AtInitOnVersion1       time.Time
}

// ガチャのマスターデータのキャッシュを更新する
func (l *LocalGachaMasters) Refresh(c echo.Context, h *Handler) error {
	c.Logger().Printf("[Gacha] Refresh Started")

	ctx := c.Request().Context()

	query := "SELECT * FROM version_masters WHERE status=1"
	var masterVersion VersionMaster
	if err := h.DB.GetContext(ctx, &masterVersion, query); err != nil {
		if err == sql.ErrNoRows {
			//return errorResponse(c, http.StatusNotFound, fmt.Errorf("active master version is not found"))
			// DBのInitializeにミスってたときは、とりあえず諦める。
			c.Logger().Errorf("[Gacha] Skip Refresh. version_masters is missing\n")
			return nil
		}
		c.Logger().Errorf("[Gacha] Skip Refresh. version_masters error %v\n", err)
		return nil
	}
	c.Logger().Printf("[Gacha] Version found")

	gachaMasterList := []*GachaMaster{}
	query = "SELECT * FROM gacha_masters ORDER BY display_order ASC"
	err := h.DB.SelectContext(ctx, &gachaMasterList, query)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	gachaMasterByID := map[int64]*GachaMaster{}
	for _, gachaMaster := range gachaMasterList {
		gachaMasterByID[gachaMaster.ID] = gachaMaster
	}
	c.Logger().Printf("[Gacha] gachaListAll = %d", len(gachaMasterList))

	var gachaItemList []*GachaItemMaster
	query = "SELECT * FROM gacha_item_masters ORDER BY id ASC"
	err = h.DB.SelectContext(ctx, &gachaItemList, query)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	gachaItemListByGachaID := map[int64][]*GachaItemMaster{}
	for _, gachaItem := range gachaItemList {
		gachaItemListByGachaID[gachaItem.GachaID] = append(gachaItemListByGachaID[gachaItem.GachaID], gachaItem)
	}
	gachaItemWeightSum := map[int64]int64{}
	for gachaID, gachaItems := range gachaItemListByGachaID {
		for _, gachaItem := range gachaItems {
			gachaItemWeightSum[gachaID] += int64(gachaItem.Weight)
		}
	}

	// item_masters
	var items []*ItemMaster
	query = "SELECT * FROM item_masters ORDER BY id ASC"
	err = h.DB.SelectContext(ctx, &items, query)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	itemByID := map[int64]*ItemMaster{}
	for _, item := range items {
		itemByID[item.ID] = item
	}

	// login_bonus_masters
	var loginBonuses []*LoginBonusMaster
	query = "SELECT * FROM login_bonus_masters"
	if err := h.DB.SelectContext(ctx, &loginBonuses, query); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// login_bonus_reward_masters
	var loginBonusRewardMasters []*LoginBonusRewardMaster
	err = h.DB.SelectContext(ctx, &loginBonusRewardMasters, "SELECT * FROM login_bonus_reward_masters")
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// present_all_masters
	var normalPresents []*PresentAllMaster
	query = "SELECT * FROM present_all_masters"
	if err := h.DB.SelectContext(ctx, &normalPresents, query); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// 一括更新
	l.Lock()
	defer l.Unlock()

	l.VersionMaster = &masterVersion
	l.GachaMasters = gachaMasterList
	l.GachaMasterByID = gachaMasterByID
	l.GachaItemList = gachaItemList
	l.GachaItemListByGachaID = gachaItemListByGachaID
	l.GachaItemWeightSum = gachaItemWeightSum
	l.Items = items
	l.ItemByID = itemByID
	l.LoginBonuses = loginBonuses
	l.LoginBonusRewards = loginBonusRewardMasters
	l.PresentAll = normalPresents
	if l.VersionMaster.MasterVersion == "1" {
		l.AtInitOnVersion1 = time.Now()
	}

	c.Logger().Printf("[Gacha] Refreshed: Version = %+v", l.VersionMaster)

	return nil
}

func (l *LocalGachaMasters) GetVersionMaster() *VersionMaster {
	l.RLock()
	defer l.RUnlock()
	return l.VersionMaster
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

	// WORKAROUND: 37のガチャは開始直後以外引ける
	if gachaID == 37 {
		if l.AtInitOnVersion1.Add(1 * time.Second).After(time.Now()) {
			return "", nil, errorResponse(c, http.StatusNotFound, fmt.Errorf("gacha not found gacha %d", gachaID))
		}
	} else if !ok || !(gachaInfo.StartAt <= requestAt && gachaInfo.EndAt >= requestAt) {
		return "", nil, errorResponse(c, http.StatusNotFound, fmt.Errorf("gacha not found gacha %d", gachaID))
	}

	// gachaItemMasterからアイテムリスト取得
	gachaItemList, ok := l.GachaItemListByGachaID[gachaID]
	if !ok {
		return "", nil, errorResponse(c, http.StatusNotFound, fmt.Errorf("gacha itemnot found gacha item %d", gachaID))
	}
	if len(gachaItemList) == 0 {
		return "", nil, errorResponse(c, http.StatusNotFound, fmt.Errorf("gacha item not found gacha item %d", gachaID))
	}

	// weightの合計値を算出
	sum, ok := l.GachaItemWeightSum[gachaID]
	if !ok {
		return "", nil, errorResponse(c, http.StatusNotFound, fmt.Errorf("gacha weight not found gacha item sumi %d", gachaID))
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

func (l *LocalGachaMasters) ItemsByIDs(ids []int64) []*ItemMaster {
	l.RLock()
	defer l.RUnlock()

	res := []*ItemMaster{}
	for _, id := range ids {
		res = append(res, l.ItemByID[id])
	}
	return res
}

func (l *LocalGachaMasters) JoinUserCardData(userCardData *TargetUserCardData) error {
	l.RLock()
	defer l.RUnlock()

	item, ok := l.ItemByID[userCardData.CardID]
	if !ok {
		return fmt.Errorf("missing userCard id = %d", userCardData.CardID)
	}
	userCardData.BaseAmountPerSec = *item.AmountPerSec
	userCardData.MaxLevel = *item.MaxLevel
	userCardData.MaxAmountPerSec = *item.MaxAmountPerSec
	userCardData.BaseExpPerLevel = *item.BaseExpPerLevel
	return nil
}

func (l *LocalGachaMasters) JoinConsumerUserItemData(c *ConsumeUserItemData) error {
	l.RLock()
	defer l.RUnlock()

	item, ok := l.ItemByID[c.ItemID]
	if !ok {
		return fmt.Errorf("missing consumerUserItem id = %d", c.ItemID)
	}
	c.GainedExp = *item.GainedExp
	return nil
}

func (l *LocalGachaMasters) ActiveLoginBonuses(requestAt int64) []*LoginBonusMaster {
	l.RLock()
	defer l.RUnlock()

	res := []*LoginBonusMaster{}
	for _, bonus := range l.LoginBonuses {
		//WORKAROUND: 9月になってログインボーナスが終わってしまった
		// if !(bonus.StartAt <= requestAt && requestAt <= bonus.EndAt) {
		if !(bonus.StartAt <= requestAt) || bonus.ID == 3 {
			continue
		}
		res = append(res, bonus)
	}
	return res
}

func (l *LocalGachaMasters) AllLoginBonusRewards() []*LoginBonusRewardMaster {
	l.RLock()
	defer l.RUnlock()
	return l.LoginBonusRewards
}

func (l *LocalGachaMasters) ActivePresentAll(requestAt int64) []*PresentAllMaster {
	l.RLock()
	defer l.RUnlock()
	res := []*PresentAllMaster{}
	for _, present := range l.PresentAll {
		if !(present.RegisteredStartAt <= requestAt && requestAt <= present.RegisteredEndAt) {
			continue
		}
		res = append(res, present)
	}
	return res
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
