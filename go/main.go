package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/pkg/errors"
)

var (
	ErrInvalidRequestBody       error = fmt.Errorf("invalid request body")
	ErrInvalidMasterVersion     error = fmt.Errorf("invalid master version")
	ErrInvalidItemType          error = fmt.Errorf("invalid item type")
	ErrInvalidToken             error = fmt.Errorf("invalid token")
	ErrGetRequestTime           error = fmt.Errorf("failed to get request time")
	ErrExpiredSession           error = fmt.Errorf("session expired")
	ErrUserNotFound             error = fmt.Errorf("not found user")
	ErrUserDeviceNotFound       error = fmt.Errorf("not found user device")
	ErrItemNotFound             error = fmt.Errorf("not found item")
	ErrLoginBonusRewardNotFound error = fmt.Errorf("not found login bonus reward")
	ErrNoFormFile               error = fmt.Errorf("no such file")
	ErrUnauthorized             error = fmt.Errorf("unauthorized user")
	ErrForbidden                error = fmt.Errorf("forbidden")
	ErrGeneratePassword         error = fmt.Errorf("failed to password hash") //nolint:deadcode
)

const (
	DeckCardNumber      int = 3
	PresentCountPerPage int = 100

	SQLDirectory string = "../sql/"
)

type Handler struct {
	AppID   int64
	DB      *sqlx.DB
	UserDBs []*sqlx.DB
	Redis   *redis.Client
}

func main() {
	rand.Seed(time.Now().UnixNano())
	time.Local = time.FixedZone("Local", 9*60*60)

	e := echo.New()
	e.Use(middleware.Recover())
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{http.MethodGet, http.MethodPost},
		AllowHeaders: []string{"Content-Type", "x-master-version", "x-session"},
	}))

	// connect db
	dbx, err := connectDB(false)
	if err != nil {
		e.Logger.Fatalf("failed to connect to db: %v", err)
	}
	defer dbx.Close()

	userDBs, err := connectUserDB(false)
	if err != nil {
		e.Logger.Fatalf("failed to connect to user db: %v", err)
	}

	appID, err := strconv.ParseInt(os.Getenv("ISUCON_APP_NUMBER"), 10, 64)
	if err != nil {
		e.Logger.Fatalf("Need env WEBID=1,2...")
	}

	// connect redis
	redis := redis.NewClient(&redis.Options{
		Addr:     getEnv("REDIS_HOST", "localhost:6379"),
		Password: "",
		DB:       0,
	})

	// setting server
	e.Server.Addr = fmt.Sprintf(":%v", "8080")
	h := &Handler{
		AppID:   appID,
		DB:      dbx,
		UserDBs: userDBs,
		Redis:   redis,
	}

	// e.Use(middleware.CORS())
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{}))

	// utility
	e.POST("/initialize", h.initialize)
	e.GET("/health", h.health)
	e.POST("/gacha/refresh", h.refreshGacha)

	// feature
	API := e.Group("", h.apiMiddleware)
	API.POST("/user", h.createUser)
	API.POST("/login", h.login)
	sessCheckAPI := API.Group("", h.checkSessionMiddleware)
	sessCheckAPI.GET("/user/:userID/gacha/index", h.listGacha)
	sessCheckAPI.POST("/user/:userID/gacha/draw/:gachaID/:n", h.drawGacha)
	sessCheckAPI.GET("/user/:userID/present/index/:n", h.listPresent)
	sessCheckAPI.POST("/user/:userID/present/receive", h.receivePresent)
	sessCheckAPI.GET("/user/:userID/item", h.listItem)
	sessCheckAPI.POST("/user/:userID/card/addexp/:cardID", h.addExpToCard)
	sessCheckAPI.POST("/user/:userID/card", h.updateDeck)
	sessCheckAPI.POST("/user/:userID/reward", h.reward)
	sessCheckAPI.GET("/user/:userID/home", h.home)

	// admin
	adminAPI := e.Group("", h.adminMiddleware)
	adminAPI.POST("/admin/login", h.adminLogin)
	adminAuthAPI := adminAPI.Group("", h.adminSessionCheckMiddleware)
	adminAuthAPI.DELETE("/admin/logout", h.adminLogout)
	adminAuthAPI.GET("/admin/master", h.adminListMaster)
	adminAuthAPI.PUT("/admin/master", h.adminUpdateMaster)
	adminAuthAPI.GET("/admin/user/:userID", h.adminUser)
	adminAuthAPI.POST("/admin/user/:userID/ban", h.adminBanUser)

	// ガチャのマスターデータのキャッシュを更新
	dummyReq, err := http.NewRequest("GET", "/", nil)
	if err != nil {
		e.Logger.Fatalf("failed to initialize master cache: %v", err)
	}
	localGachaMasters.Refresh(e.NewContext(dummyReq, nil), h)

	e.Logger.Infof("Start server: address=%s", e.Server.Addr)
	e.Logger.Error(e.StartServer(e.Server))
}

func connectDB(batch bool) (*sqlx.DB, error) {
	dsn := fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&loc=%s&multiStatements=%t&interpolateParams=true",
		getEnv("ISUCON_DB_USER", "isucon"),
		getEnv("ISUCON_DB_PASSWORD", "isucon"),
		getEnv("ISUCON_DB_HOST", "127.0.0.1"),
		getEnv("ISUCON_DB_PORT", "3306"),
		getEnv("ISUCON_DB_NAME", "isucon"),
		"Asia%2FTokyo",
		batch,
	)
	db, err := sqlx.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2048)
	// デフォルトは2
	db.SetMaxIdleConns(1024)
	return db, nil
}

// ユーザーIDに応じたuser DBのコネクションを返す
func (h *Handler) chooseUserDB(userID int64) *sqlx.DB {
	return h.UserDBs[userID%3]
}

func connectUserDB(batch bool) ([]*sqlx.DB, error) {
	var conns []*sqlx.DB
	for i := 0; i < 3; i++ {
		// ISUCON_DB_HOST1, ISUCON_DB_HOST2, ISUCON_DB_HOST3
		hostname := fmt.Sprintf("ISUCON_DB_HOST%d", i+1)
		dsn := fmt.Sprintf(
			"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&loc=%s&multiStatements=%t&interpolateParams=true",
			getEnv("ISUCON_DB_USER", "isucon"),
			getEnv("ISUCON_DB_PASSWORD", "isucon"),
			// ISUCON_DB_HOST{1,2,3} がsetされていなかったらISUCON_DB_HOSTにフォールバック
			getEnv(hostname, getEnv("ISUCON_DB_HOST", "127.0.0.1")),
			getEnv("ISUCON_DB_PORT", "3306"),
			getEnv("ISUCON_DB_NAME", "isucon"),
			"Asia%2FTokyo",
			batch,
		)
		db, err := sqlx.Open("mysql", dsn)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(2048)
		// デフォルトは2
		db.SetMaxIdleConns(1024)
		conns = append(conns, db)
	}
	return conns, nil
}

// adminMiddleware
func (h *Handler) adminMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		requestAt := time.Now()
		c.Set("requestTime", requestAt.Unix())

		// next
		if err := next(c); err != nil {
			c.Error(err)
		}
		return nil
	}
}

// apiMiddleware
func (h *Handler) apiMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx := c.Request().Context()
		requestAt, err := time.Parse(time.RFC1123, c.Request().Header.Get("x-isu-date"))
		if err != nil {
			requestAt = time.Now()
		}
		c.Set("requestTime", requestAt.Unix())

		// マスタ確認
		query := "SELECT * FROM version_masters WHERE status=1"
		masterVersion := new(VersionMaster)
		_db := h.DB
		if err := _db.GetContext(ctx, masterVersion, query); err != nil {
			if err == sql.ErrNoRows {
				return errorResponse(c, http.StatusNotFound, fmt.Errorf("active master version is not found"))
			}
			return errorResponse(c, http.StatusInternalServerError, err)
		}

		if masterVersion.MasterVersion != c.Request().Header.Get("x-master-version") {
			return errorResponse(c, http.StatusUnprocessableEntity, ErrInvalidMasterVersion)
		}

		// check ban
		userID, err := getUserID(c)
		if err == nil && userID != 0 {
			isBan, err := h.checkBan(ctx, userID)
			if err != nil {
				return errorResponse(c, http.StatusInternalServerError, err)
			}
			if isBan {
				return errorResponse(c, http.StatusForbidden, ErrForbidden)
			}
		}

		// next
		if err := next(c); err != nil {
			c.Error(err)
		}
		return nil
	}
}

// checkSessionMiddleware
func (h *Handler) checkSessionMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx := c.Request().Context()
		sessID := c.Request().Header.Get("x-session")
		if sessID == "" {
			return errorResponse(c, http.StatusUnauthorized, ErrUnauthorized)
		}

		userID, err := getUserID(c)
		if err != nil {
			return errorResponse(c, http.StatusBadRequest, err)
		}

		requestAt, err := getRequestTime(c)
		if err != nil {
			return errorResponse(c, http.StatusInternalServerError, ErrGetRequestTime)
		}

		sessionUserID, _ := splitSessionID(sessID)
		userSession := new(Session)
		query := "SELECT * FROM user_sessions WHERE session_id=? AND deleted_at IS NULL"
		_db := h.chooseUserDB(sessionUserID)
		if err := _db.GetContext(ctx, userSession, query, sessID); err != nil {
			if err == sql.ErrNoRows {
				return errorResponse(c, http.StatusUnauthorized, ErrUnauthorized)
			}
			return errorResponse(c, http.StatusInternalServerError, err)
		}

		if userSession.UserID != userID {
			return errorResponse(c, http.StatusForbidden, ErrForbidden)
		}

		if userSession.ExpiredAt < requestAt {
			query = "UPDATE user_sessions SET deleted_at=? WHERE session_id=?"
			if _, err = _db.ExecContext(ctx, query, requestAt, sessID); err != nil {
				return errorResponse(c, http.StatusInternalServerError, err)
			}
			return errorResponse(c, http.StatusUnauthorized, ErrExpiredSession)
		}

		// next
		if err := next(c); err != nil {
			c.Error(err)
		}
		return nil
	}
}

// checkOneTimeToken
func (h *Handler) checkOneTimeToken(ctx context.Context, userID int64, token string, tokenType int, requestAt int64) error {
	val, err := h.Redis.GetDel(ctx, userOneTimeTokenKey(userID, token)).Result()
	if err != nil {
		if err == redis.Nil {
			return ErrInvalidToken
		}
		return err
	}
	embedTokenType, expiredAt := parseUserOneTimeTokenValue(val)
	if embedTokenType == -1 && expiredAt == -1 {
		return ErrInvalidToken
	}
	if embedTokenType != tokenType {
		return ErrInvalidToken
	}
	if expiredAt < requestAt {
		return ErrInvalidToken
	}

	return nil
}

// checkViewerID
func (h *Handler) checkViewerID(ctx context.Context, userID int64, viewerID string) error {
	query := "SELECT * FROM user_devices WHERE user_id=? AND platform_id=?"
	device := new(UserDevice)
	_db := h.chooseUserDB(userID)
	if err := _db.GetContext(ctx, device, query, userID, viewerID); err != nil {
		if err == sql.ErrNoRows {
			return ErrUserDeviceNotFound
		}
		return err
	}

	return nil
}

// checkBan
func (h *Handler) checkBan(ctx context.Context, userID int64) (bool, error) {
	banUser := new(UserBan)
	query := "SELECT * FROM user_bans WHERE user_id=?"
	_db := h.chooseUserDB(userID)
	if err := _db.GetContext(ctx, banUser, query, userID); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// getRequestTime リクエストを受けた時間をコンテキストからunixtimeで取得する
func getRequestTime(c echo.Context) (int64, error) {
	v := c.Get("requestTime")
	if requestTime, ok := v.(int64); ok {
		return requestTime, nil
	}
	return 0, ErrGetRequestTime
}

// loginProcess ログイン処理
func (h *Handler) loginProcess(ctx context.Context, tx *sqlx.Tx, userID int64, requestAt int64) (*User, []*UserLoginBonus, []*UserPresent, error) {
	user := new(User)
	query := "SELECT * FROM users WHERE id=?"
	if err := tx.GetContext(ctx, user, query, userID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, nil, ErrUserNotFound
		}
		return nil, nil, nil, err
	}

	// ログインボーナス処理
	loginBonuses, err := h.obtainLoginBonus(ctx, tx, userID, requestAt)
	if err != nil {
		return nil, nil, nil, err
	}

	// 全員プレゼント取得
	allPresents, err := h.obtainPresent(ctx, tx, userID, requestAt)
	if err != nil {
		return nil, nil, nil, err
	}

	if err = tx.GetContext(ctx, &user.IsuCoin, "SELECT isu_coin FROM users WHERE id=?", user.ID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, nil, ErrUserNotFound
		}
		return nil, nil, nil, err
	}

	user.UpdatedAt = requestAt
	user.LastActivatedAt = requestAt

	query = "UPDATE users SET updated_at=?, last_activated_at=? WHERE id=?"
	if _, err := tx.ExecContext(ctx, query, requestAt, requestAt, userID); err != nil {
		return nil, nil, nil, err
	}

	return user, loginBonuses, allPresents, nil
}

// isCompleteTodayLogin ログイン処理が終わっているか
func isCompleteTodayLogin(lastActivatedAt, requestAt time.Time) bool {
	return lastActivatedAt.Year() == requestAt.Year() &&
		lastActivatedAt.Month() == requestAt.Month() &&
		lastActivatedAt.Day() == requestAt.Day()
}

// obtainLoginBonus
func (h *Handler) obtainLoginBonus(ctx context.Context, tx *sqlx.Tx, userID int64, requestAt int64) ([]*UserLoginBonus, error) {
	// login bonus masterから有効なログインボーナスを取得
	loginBonuses := make([]*LoginBonusMaster, 0)
	query := "SELECT * FROM login_bonus_masters WHERE start_at <= ? AND end_at >= ?"
	if err := tx.SelectContext(ctx, &loginBonuses, query, requestAt, requestAt); err != nil {
		return nil, err
	}

	// ユーザーの受け取り履歴 (login_bonus_masters.id -> 受け取り情報) を構築
	var loginBonusIDs []int64
	for _, bonus := range loginBonuses {
		loginBonusIDs = append(loginBonusIDs, bonus.ID)
	}
	query, args, err := sqlx.In(
		"SELECT * FROM user_login_bonuses WHERE user_id=? AND login_bonus_id IN (?)",
		userID,
		loginBonusIDs,
	)
	// TODO: len(loginBonusIDs) == 0 のときのハンドリング
	if err != nil {
		return nil, err
	}
	var userLoginBonuses []*UserLoginBonus
	err = tx.SelectContext(ctx, &userLoginBonuses, query, args...)
	if err != nil {
		return nil, err
	}
	bonusIDtoUserLoginBonus := make(map[int64]*UserLoginBonus)
	for _, userLoginBonus := range userLoginBonuses {
		bonusIDtoUserLoginBonus[userLoginBonus.LoginBonusID] = userLoginBonus
	}

	sendLoginBonuses := make([]*UserLoginBonus, 0)

	loginBonusRewardMasters := localGachaMasters.ListLoginBonusRewardMasters()

	// key: `${login_bonus_id},${reward_sequence}`
	bonusMapKey := func(bonusID int64, rewardSequence int) string {
		return fmt.Sprintf("%d,%d", bonusID, rewardSequence)
	}
	bonusIDandSequenceToBonusReward := make(map[string]*LoginBonusRewardMaster)
	for _, bonusRewardMaster := range loginBonusRewardMasters {
		bonusIDandSequenceToBonusReward[bonusMapKey(bonusRewardMaster.LoginBonusID, bonusRewardMaster.RewardSequence)] = bonusRewardMaster
	}

	obtainer := &ItemObtainer{}
	for _, bonus := range loginBonuses {
		// ボーナスの進捗取得
		userBonus, hasBonusRow := bonusIDtoUserLoginBonus[bonus.ID]
		if !hasBonusRow {
			// 初めてログインボーナスを受け取る
			ubID, err := h.generateID(ctx)
			if err != nil {
				return nil, err
			}
			userBonus = &UserLoginBonus{ // ボーナス初期化
				ID:                 ubID,
				UserID:             userID,
				LoginBonusID:       bonus.ID,
				LastRewardSequence: 0,
				LoopCount:          1,
				CreatedAt:          requestAt,
				UpdatedAt:          requestAt,
			}
		}

		// ボーナス進捗更新
		if userBonus.LastRewardSequence < bonus.ColumnCount {
			userBonus.LastRewardSequence++
		} else {
			if bonus.Looped {
				userBonus.LoopCount += 1
				userBonus.LastRewardSequence = 1
			} else {
				// 上限まで付与完了
				continue
			}
		}
		userBonus.UpdatedAt = requestAt

		// 今回付与するリソース取得
		rewardItem, ok := bonusIDandSequenceToBonusReward[bonusMapKey(bonus.ID, userBonus.LastRewardSequence)]
		if !ok {
			return nil, ErrLoginBonusRewardNotFound

		}

		err := obtainer.ObtainItem(rewardItem.ItemID, rewardItem.ItemType, rewardItem.Amount)
		if err != nil {
			return nil, err
		}

		sendLoginBonuses = append(sendLoginBonuses, userBonus)
	}

	// 進捗の保存
	query = "REPLACE INTO user_login_bonuses " +
		"(id, user_id, login_bonus_id, last_reward_sequence, loop_count, created_at, updated_at) VALUES " +
		"(:id, :user_id, :login_bonus_id, :last_reward_sequence, :loop_count, :created_at, :updated_at)"
	if _, err := tx.NamedExecContext(ctx, query, sendLoginBonuses); err != nil {
		return nil, err
	}

	_, _, _, err = obtainer.Commit(ctx, h, tx, userID, requestAt)
	if err != nil {
		return nil, err
	}

	return sendLoginBonuses, nil
}

// obtainPresent プレゼント付与処理
func (h *Handler) obtainPresent(ctx context.Context, tx *sqlx.Tx, userID int64, requestAt int64) ([]*UserPresent, error) {
	normalPresents := make([]*PresentAllMaster, 0)
	query := "SELECT * FROM present_all_masters WHERE registered_start_at <= ? AND registered_end_at >= ?"
	if err := tx.SelectContext(ctx, &normalPresents, query, requestAt, requestAt); err != nil {
		return nil, err
	}

	// ユーザーの受け取り履歴を構築する
	var normalPresentIDs []int64
	for _, np := range normalPresents {
		normalPresentIDs = append(normalPresentIDs, np.ID)
	}
	query, args, err := sqlx.In(
		"SELECT * FROM user_present_all_received_history WHERE user_id=? AND present_all_id IN (?)",
		userID,
		normalPresentIDs,
	)
	// TODO: normalPresentIDsが長さ0のときのハンドリング
	if err != nil {
		return nil, err
	}
	var userReceivedHistories []UserPresentAllReceivedHistory
	err = tx.SelectContext(ctx, &userReceivedHistories, query, args...)
	if err != nil {
		return nil, err
	}
	isReceivedPresent := make(map[int64]bool)
	for _, history := range userReceivedHistories {
		isReceivedPresent[history.PresentAllID] = true
	}

	// 全員プレゼント取得情報更新
	// 初めて受け取るプレゼントだけに絞っておく
	var firstReceivedPresents []*UserPresent
	var receivedPresentHistories []*UserPresentAllReceivedHistory
	for _, np := range normalPresents {
		// 受け取り済ならスキップ
		if _, ok := isReceivedPresent[np.ID]; ok {
			continue
		}

		// user present boxに入れる
		pID, err := h.generateID(ctx)
		if err != nil {
			return nil, err
		}
		firstReceivedPresents = append(firstReceivedPresents, &UserPresent{
			ID:             pID,
			UserID:         userID,
			SentAt:         requestAt,
			ItemType:       np.ItemType,
			ItemID:         np.ItemID,
			Amount:         int(np.Amount),
			PresentMessage: np.PresentMessage,
			CreatedAt:      requestAt,
			UpdatedAt:      requestAt,
		})

		// historyに入れる
		phID, err := h.generateID(ctx)
		if err != nil {
			return nil, err
		}
		receivedPresentHistories = append(receivedPresentHistories, &UserPresentAllReceivedHistory{
			ID:           phID,
			UserID:       userID,
			PresentAllID: np.ID,
			ReceivedAt:   requestAt,
			CreatedAt:    requestAt,
			UpdatedAt:    requestAt,
		})
	}

	// bulk insertするものがないときは抜ける
	if len(firstReceivedPresents) == 0 {
		return firstReceivedPresents, nil
	}
	if len(receivedPresentHistories) == 0 {
		return firstReceivedPresents, nil
	}

	// user present boxにbulk insert
	if _, err := tx.NamedExecContext(
		ctx,
		"INSERT INTO user_presents"+
			"(id, user_id, sent_at, item_type, item_id, amount, present_message, created_at, updated_at) VALUES "+
			"(:id, :user_id, :sent_at, :item_type, :item_id, :amount, :present_message, :created_at, :updated_at)",
		firstReceivedPresents,
	); err != nil {
		return nil, err
	}

	// historyにbulk insert
	if _, err := tx.NamedExecContext(
		ctx,
		"INSERT INTO user_present_all_received_history"+
			"(id, user_id, present_all_id, received_at, created_at, updated_at) VALUES "+
			"(:id, :user_id, :present_all_id, :received_at, :created_at, :updated_at)",
		receivedPresentHistories,
	); err != nil {
		return nil, err
	}

	return firstReceivedPresents, nil
}

type ItemObtainerObtainItem struct {
	itemID   int64
	itemType int
	amount   int64
	// commitItems で []*UserItem を返す前にorderで並べ替えること！
	order int
}

type ItemObtainer struct {
	// coins (獲得した個数のスライス)
	obtainCoins []int64
	// cards (item_masters.id のスライス)
	obtainCards []int64
	// items
	obtainItems []*ItemObtainerObtainItem
}

func (obtainer *ItemObtainer) ObtainItem(itemID int64, itemType int, obtainAmount int64) error {
	itemOrder := 0
	switch itemType {
	case 1: // coin
		obtainer.obtainCoins = append(obtainer.obtainCoins, obtainAmount)
	case 2: // card(ハンマー)
		obtainer.obtainCards = append(obtainer.obtainCards, itemID)
	case 3, 4: // 強化素材
		obtainer.obtainItems = append(obtainer.obtainItems, &ItemObtainerObtainItem{
			itemID:   itemID,
			itemType: itemType,
			amount:   obtainAmount,
			order:    itemOrder,
		})
		itemOrder++
	default:
		return ErrInvalidItemType
	}
	return nil
}

func (obtainer *ItemObtainer) commitCoins(ctx context.Context, tx *sqlx.Tx, userID int64) ([]int64, error) {
	if len(obtainer.obtainCoins) == 0 {
		return obtainer.obtainCoins, nil
	}

	var obtainAmount int64
	for _, amount := range obtainer.obtainCoins {
		obtainAmount += amount
	}

	query := "UPDATE users SET isu_coin=isu_coin+? WHERE id=?"
	result, err := tx.ExecContext(ctx, query, obtainAmount, userID)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, ErrUserNotFound
	}

	return obtainer.obtainCoins, nil
}

func (obtainer *ItemObtainer) commitCards(ctx context.Context, h *Handler, tx *sqlx.Tx, userID, requestAt int64) ([]*UserCard, error) {
	if len(obtainer.obtainCards) == 0 {
		return []*UserCard{}, nil
	}

	query, args, err := sqlx.In("SELECT * FROM item_masters WHERE id IN (?)", obtainer.obtainCards)
	if err != nil {
		return nil, err
	}

	var itemMasters []*ItemMaster
	err = tx.SelectContext(ctx, &itemMasters, query, args...)
	if err != nil {
		return nil, err
	}

	var obtainCards []*UserCard

	for _, item := range itemMasters {
		// item_typeがおかしかったらエラーにする
		if item.ItemType != 2 {
			return nil, ErrItemNotFound
		}

		cID, err := h.generateID(ctx)
		if err != nil {
			return nil, err
		}
		card := &UserCard{
			ID:           cID,
			UserID:       userID,
			CardID:       item.ID,
			AmountPerSec: *item.AmountPerSec,
			Level:        1,
			TotalExp:     0,
			CreatedAt:    requestAt,
			UpdatedAt:    requestAt,
		}

		obtainCards = append(obtainCards, card)
	}

	query = "INSERT INTO user_cards" +
		"(id, user_id, card_id, amount_per_sec, level, total_exp, created_at, updated_at) VALUES " +
		"(:id, :user_id, :card_id, :amount_per_sec, :level, :total_exp, :created_at, :updated_at)"
	if _, err := tx.NamedExecContext(ctx, query, obtainCards); err != nil {
		return nil, err
	}

	return obtainCards, nil
}

func (obtainer *ItemObtainer) commitItems(ctx context.Context, h *Handler, tx *sqlx.Tx, userID, requestAt int64) ([]*UserItem, error) {
	if len(obtainer.obtainItems) == 0 {
		return []*UserItem{}, nil
	}

	var itemIDs []int64
	for _, item := range obtainer.obtainItems {
		itemIDs = append(itemIDs, item.itemID)
	}
	query, args, err := sqlx.In("SELECT * FROM item_masters WHERE id IN (?)", itemIDs)
	if err != nil {
		return nil, err
	}

	var itemMasters []*ItemMaster
	err = tx.SelectContext(ctx, &itemMasters, query, args...)
	if err != nil {
		return nil, err
	}

	itemIDtoMaster := make(map[int64]*ItemMaster)
	for _, item := range itemMasters {
		// item_typeがおかしかったらエラーにする
		if !(item.ItemType == 3 || item.ItemType == 4) {
			return nil, ErrItemNotFound
		}
		itemIDtoMaster[item.ID] = item
	}

	// 初めて取得したアイテムはINSERT、持っているアイテムは所持数をUPDATEする

	// 持っているアイテムのリストと item_id -> user_item のマッピングを作っておく
	var havingItems []*UserItem
	query, args, err = sqlx.In("SELECT * FROM user_items WHERE user_id = ? AND item_id IN (?)", userID, itemIDs)
	if err != nil {
		return nil, err
	}
	err = tx.SelectContext(ctx, &havingItems, query, args...)
	if err != nil {
		return nil, err
	}
	itemIDtoUserItem := make(map[int64]*UserItem)
	for _, havingItem := range havingItems {
		itemIDtoUserItem[havingItem.ItemID] = havingItem
	}

	// 初めて取得したアイテムをかき集める
	var firstObtainedItems []*UserItem
	for _, item := range obtainer.obtainItems {
		if havingItem, ok := itemIDtoUserItem[item.itemID]; ok {
			havingItem.Amount += int(item.amount)
			havingItem.UpdatedAt = requestAt
		} else {
			uitemID, err := h.generateID(ctx)
			if err != nil {
				return nil, err
			}
			firstObtainedItems = append(firstObtainedItems, &UserItem{
				ID:        uitemID,
				UserID:    userID,
				ItemType:  item.itemType,
				ItemID:    item.itemID,
				Amount:    int(item.amount),
				CreatedAt: requestAt,
				UpdatedAt: requestAt,
			})
		}
	}

	var obtainItems []*UserItem
	obtainItems = append(obtainItems, firstObtainedItems...)
	obtainItems = append(obtainItems, havingItems...)

	if len(obtainItems) > 0 {
		if _, err := tx.NamedExecContext(
			ctx,
			"REPLACE INTO user_items"+
				"(id, user_id, item_id, item_type, amount, created_at, updated_at) VALUES "+
				"(:id, :user_id, :item_id, :item_type, :amount, :created_at, :updated_at)",
			obtainItems); err != nil {
			return nil, err
		}
	}

	return obtainItems, nil
}

func (obtainer *ItemObtainer) Commit(ctx context.Context, h *Handler, tx *sqlx.Tx, userID, requestAt int64) ([]int64, []*UserCard, []*UserItem, error) {
	obtainCoins, err := obtainer.commitCoins(ctx, tx, userID)
	if err != nil {
		return nil, nil, nil, err
	}
	obtainCards, err := obtainer.commitCards(ctx, h, tx, userID, requestAt)
	if err != nil {
		return nil, nil, nil, err
	}
	obtainItems, err := obtainer.commitItems(ctx, h, tx, userID, requestAt)
	if err != nil {
		return nil, nil, nil, err
	}

	return obtainCoins, obtainCards, obtainItems, nil
}

// initialize 初期化処理
// POST /initialize
func (h *Handler) initialize(c echo.Context) error {
	ctx := c.Request().Context()
	dbx, err := connectDB(true)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	defer dbx.Close()
	userDBs, err := connectUserDB(true)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	for _, userDB := range userDBs {
		defer userDB.Close()
	}
	err = h.Redis.FlushAll(ctx).Err()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	out, err := exec.Command("/bin/sh", "-c", SQLDirectory+"init.sh").CombinedOutput()
	if err != nil {
		c.Logger().Errorf("Failed to initialize %s: %v", string(out), err)
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	err = localGachaMasters.Refresh(c, &Handler{
		DB: dbx,
	})
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	c.Logger().Printf("[Gacha] local init master")

	return successResponse(c, &InitializeResponse{
		Language: "go",
	})
}

type InitializeResponse struct {
	Language string `json:"language"`
}

// createUser ユーザの作成
// POST /user
func (h *Handler) createUser(c echo.Context) error {
	ctx := c.Request().Context()
	// parse body
	defer c.Request().Body.Close()
	req := new(CreateUserRequest)
	if err := parseRequestBody(c, req); err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	if req.ViewerID == "" || req.PlatformType < 1 || req.PlatformType > 3 {
		return errorResponse(c, http.StatusBadRequest, ErrInvalidRequestBody)
	}

	requestAt, err := getRequestTime(c)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, ErrGetRequestTime)
	}

	// ユーザ作成
	uID, err := h.generateID(ctx)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	user := &User{
		ID:              uID,
		IsuCoin:         0,
		LastGetRewardAt: requestAt,
		LastActivatedAt: requestAt,
		RegisteredAt:    requestAt,
		CreatedAt:       requestAt,
		UpdatedAt:       requestAt,
	}

	_db := h.chooseUserDB(uID)
	tx, err := _db.Beginx()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	defer tx.Rollback() //nolint:errcheck

	query := "INSERT INTO users(id, last_activated_at, registered_at, last_getreward_at, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?)"
	if _, err = tx.ExecContext(ctx, query, user.ID, user.LastActivatedAt, user.RegisteredAt, user.LastGetRewardAt, user.CreatedAt, user.UpdatedAt); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	udID, err := h.generateID(ctx)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	userDevice := &UserDevice{
		ID:           udID,
		UserID:       user.ID,
		PlatformID:   req.ViewerID,
		PlatformType: req.PlatformType,
		CreatedAt:    requestAt,
		UpdatedAt:    requestAt,
	}
	query = "INSERT INTO user_devices(id, user_id, platform_id, platform_type, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)"
	_, err = tx.ExecContext(ctx, query, userDevice.ID, user.ID, req.ViewerID, req.PlatformType, requestAt, requestAt)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// 初期デッキ付与
	initCard := new(ItemMaster)
	query = "SELECT * FROM item_masters WHERE id=?"
	if err = tx.GetContext(ctx, initCard, query, 2); err != nil {
		if err == sql.ErrNoRows {
			return errorResponse(c, http.StatusNotFound, ErrItemNotFound)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	initCards := make([]*UserCard, 0, 3)
	for i := 0; i < 3; i++ {
		cID, err := h.generateID(ctx)
		if err != nil {
			return errorResponse(c, http.StatusInternalServerError, err)
		}
		card := &UserCard{
			ID:           cID,
			UserID:       user.ID,
			CardID:       initCard.ID,
			AmountPerSec: *initCard.AmountPerSec,
			Level:        1,
			TotalExp:     0,
			CreatedAt:    requestAt,
			UpdatedAt:    requestAt,
		}
		query = "INSERT INTO user_cards(id, user_id, card_id, amount_per_sec, level, total_exp, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
		if _, err := tx.ExecContext(ctx, query, card.ID, card.UserID, card.CardID, card.AmountPerSec, card.Level, card.TotalExp, card.CreatedAt, card.UpdatedAt); err != nil {
			return errorResponse(c, http.StatusInternalServerError, err)
		}
		initCards = append(initCards, card)
	}

	deckID, err := h.generateID(ctx)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	initDeck := &UserDeck{
		ID:        deckID,
		UserID:    user.ID,
		CardID1:   initCards[0].ID,
		CardID2:   initCards[1].ID,
		CardID3:   initCards[2].ID,
		CreatedAt: requestAt,
		UpdatedAt: requestAt,
	}
	query = "INSERT INTO user_decks(id, user_id, user_card_id_1, user_card_id_2, user_card_id_3, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)"
	if _, err := tx.ExecContext(ctx, query, initDeck.ID, initDeck.UserID, initDeck.CardID1, initDeck.CardID2, initDeck.CardID3, initDeck.CreatedAt, initDeck.UpdatedAt); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// ログイン処理
	user, loginBonuses, presents, err := h.loginProcess(ctx, tx, user.ID, requestAt)
	if err != nil {
		if err == ErrUserNotFound || err == ErrItemNotFound || err == ErrLoginBonusRewardNotFound {
			return errorResponse(c, http.StatusNotFound, err)
		}
		if err == ErrInvalidItemType {
			return errorResponse(c, http.StatusBadRequest, err)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// generate session
	sID, err := h.generateID(ctx)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	sessID, err := generateUUID(user.ID)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	sess := &Session{
		ID:        sID,
		UserID:    user.ID,
		SessionID: sessID,
		CreatedAt: requestAt,
		UpdatedAt: requestAt,
		ExpiredAt: requestAt + 86400,
	}
	query = "INSERT INTO user_sessions(id, user_id, session_id, created_at, updated_at, expired_at) VALUES (?, ?, ?, ?, ?, ?)"
	if _, err = tx.ExecContext(ctx, query, sess.ID, sess.UserID, sess.SessionID, sess.CreatedAt, sess.UpdatedAt, sess.ExpiredAt); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	err = tx.Commit()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	return successResponse(c, &CreateUserResponse{
		UserID:           user.ID,
		ViewerID:         req.ViewerID,
		SessionID:        sess.SessionID,
		CreatedAt:        requestAt,
		UpdatedResources: makeUpdatedResources(requestAt, user, userDevice, initCards, []*UserDeck{initDeck}, nil, loginBonuses, presents),
	})
}

type CreateUserRequest struct {
	ViewerID     string `json:"viewerId"`
	PlatformType int    `json:"platformType"`
}

type CreateUserResponse struct {
	UserID           int64            `json:"userId"`
	ViewerID         string           `json:"viewerId"`
	SessionID        string           `json:"sessionId"`
	CreatedAt        int64            `json:"createdAt"`
	UpdatedResources *UpdatedResource `json:"updatedResources"`
}

// login ログイン
// POST /login
func (h *Handler) login(c echo.Context) error {
	ctx := c.Request().Context()
	defer c.Request().Body.Close()
	req := new(LoginRequest)
	if err := parseRequestBody(c, req); err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	requestAt, err := getRequestTime(c)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, ErrGetRequestTime)
	}

	user := new(User)
	query := "SELECT * FROM users WHERE id=?"
	_db := h.chooseUserDB(req.UserID)
	if err := _db.GetContext(ctx, user, query, req.UserID); err != nil {
		if err == sql.ErrNoRows {
			return errorResponse(c, http.StatusNotFound, ErrUserNotFound)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// check ban
	isBan, err := h.checkBan(ctx, user.ID)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	if isBan {
		return errorResponse(c, http.StatusForbidden, ErrForbidden)
	}

	// viewer id check
	if err = h.checkViewerID(ctx, user.ID, req.ViewerID); err != nil {
		if err == ErrUserDeviceNotFound {
			return errorResponse(c, http.StatusNotFound, err)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	tx, err := _db.Beginx()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	defer tx.Rollback() //nolint:errcheck

	// sessionを更新
	query = "UPDATE user_sessions SET deleted_at=? WHERE user_id=? AND deleted_at IS NULL"
	if _, err = tx.ExecContext(ctx, query, requestAt, req.UserID); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	sID, err := h.generateID(ctx)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	sessID, err := generateUUID(user.ID)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	sess := &Session{
		ID:        sID,
		UserID:    req.UserID,
		SessionID: sessID,
		CreatedAt: requestAt,
		UpdatedAt: requestAt,
		ExpiredAt: requestAt + 86400,
	}
	query = "INSERT INTO user_sessions(id, user_id, session_id, created_at, updated_at, expired_at) VALUES (?, ?, ?, ?, ?, ?)"
	if _, err = tx.ExecContext(ctx, query, sess.ID, sess.UserID, sess.SessionID, sess.CreatedAt, sess.UpdatedAt, sess.ExpiredAt); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// すでにログインしているユーザはログイン処理をしない
	if isCompleteTodayLogin(time.Unix(user.LastActivatedAt, 0), time.Unix(requestAt, 0)) {
		user.UpdatedAt = requestAt
		user.LastActivatedAt = requestAt

		query = "UPDATE users SET updated_at=?, last_activated_at=? WHERE id=?"
		if _, err := tx.ExecContext(ctx, query, requestAt, requestAt, req.UserID); err != nil {
			return errorResponse(c, http.StatusInternalServerError, err)
		}

		err = tx.Commit()
		if err != nil {
			return errorResponse(c, http.StatusInternalServerError, err)
		}

		return successResponse(c, &LoginResponse{
			ViewerID:         req.ViewerID,
			SessionID:        sess.SessionID,
			UpdatedResources: makeUpdatedResources(requestAt, user, nil, nil, nil, nil, nil, nil),
		})
	}

	// login process
	user, loginBonuses, presents, err := h.loginProcess(ctx, tx, req.UserID, requestAt)
	if err != nil {
		if err == ErrUserNotFound || err == ErrItemNotFound || err == ErrLoginBonusRewardNotFound {
			return errorResponse(c, http.StatusNotFound, err)
		}
		if err == ErrInvalidItemType {
			return errorResponse(c, http.StatusBadRequest, err)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	err = tx.Commit()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	return successResponse(c, &LoginResponse{
		ViewerID:         req.ViewerID,
		SessionID:        sess.SessionID,
		UpdatedResources: makeUpdatedResources(requestAt, user, nil, nil, nil, nil, loginBonuses, presents),
	})
}

type LoginRequest struct {
	ViewerID string `json:"viewerId"`
	UserID   int64  `json:"userId"`
}

type LoginResponse struct {
	ViewerID         string           `json:"viewerId"`
	SessionID        string           `json:"sessionId"`
	UpdatedResources *UpdatedResource `json:"updatedResources"`
}

func allUserOneTimeTokenKey(userID int64) string {
	return fmt.Sprintf("user_one_time_token:%d:*", userID)
}

func userOneTimeTokenKey(userID int64, token string) string {
	return fmt.Sprintf("user_one_time_token:%d:%s", userID, token)
}

// [tokenType]:[expiredAt]
func formatUserOneTimeTokenValue(tokenType int, expiredAt int64) string {
	return fmt.Sprintf("%d:%d", tokenType, expiredAt)
}

func parseUserOneTimeTokenValue(tokenValue string) (int, int64) {
	xs := strings.Split(tokenValue, ":")
	if len(xs) != 2 {
		return -1, -1
	}
	tokenType, err := strconv.ParseInt(xs[0], 10, 64)
	if err != nil {
		return -1, -1
	}
	expiredAt, err := strconv.ParseInt(xs[1], 10, 64)
	if err != nil {
		return -1, -1
	}
	return int(tokenType), expiredAt
}

// listGacha ガチャ一覧
// GET /user/{userID}/gacha/index
func (h *Handler) listGacha(c echo.Context) error {
	ctx := c.Request().Context()
	userID, err := getUserID(c)
	if err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	requestAt, err := getRequestTime(c)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, ErrGetRequestTime)
	}

	gachaDataList, err := localGachaMasters.List(c, h, requestAt)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// genearte one time token
	err = h.Redis.Del(ctx, allUserOneTimeTokenKey(userID)).Err()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	tk, err := generateUUID(userID)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	err = h.Redis.Set(ctx, userOneTimeTokenKey(userID, tk), formatUserOneTimeTokenValue(1, requestAt+600), 0).Err()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	return successResponse(c, &ListGachaResponse{
		OneTimeToken: tk,
		Gachas:       gachaDataList,
	})
}

type ListGachaResponse struct {
	OneTimeToken string       `json:"oneTimeToken"`
	Gachas       []*GachaData `json:"gachas"`
}

type GachaData struct {
	Gacha     *GachaMaster       `json:"gacha"`
	GachaItem []*GachaItemMaster `json:"gachaItemList"`
}

// drawGacha ガチャを引く
// POST /user/{userID}/gacha/draw/{gachaID}/{n}
func (h *Handler) drawGacha(c echo.Context) error {
	ctx := c.Request().Context()
	userID, err := getUserID(c)
	if err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	gachaID := c.Param("gachaID")
	if gachaID == "" {
		return errorResponse(c, http.StatusBadRequest, fmt.Errorf("invalid gachaID"))
	}

	gachaCount, err := strconv.ParseInt(c.Param("n"), 10, 64)
	if err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}
	if gachaCount != 1 && gachaCount != 10 {
		return errorResponse(c, http.StatusBadRequest, fmt.Errorf("invalid draw gacha times"))
	}

	defer c.Request().Body.Close()
	req := new(DrawGachaRequest)
	if err = parseRequestBody(c, req); err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	requestAt, err := getRequestTime(c)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, ErrGetRequestTime)
	}

	if err = h.checkOneTimeToken(ctx, userID, req.OneTimeToken, 1, requestAt); err != nil {
		if err == ErrInvalidToken {
			return errorResponse(c, http.StatusBadRequest, err)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	if err = h.checkViewerID(ctx, userID, req.ViewerID); err != nil {
		if err == ErrUserDeviceNotFound {
			return errorResponse(c, http.StatusNotFound, err)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	consumedCoin := int64(gachaCount * 1000)

	// userのisuconが足りるか
	user := new(User)
	query := "SELECT * FROM users WHERE id=?"
	_db := h.chooseUserDB(userID)
	if err := _db.GetContext(ctx, user, query, userID); err != nil {
		if err == sql.ErrNoRows {
			return errorResponse(c, http.StatusNotFound, ErrUserNotFound)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	if user.IsuCoin < consumedCoin {
		return errorResponse(c, http.StatusConflict, fmt.Errorf("not enough isucon"))
	}

	// random値の導出 & 抽選
	gachaIDint, err := strconv.ParseInt(gachaID, 10, 64)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	gachaName, result, err := localGachaMasters.Pick(c, h, gachaIDint, gachaCount, requestAt)
	if err != nil {
		return err
	}

	tx, err := _db.Beginx()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	defer tx.Rollback() //nolint:errcheck

	// 直付与 => プレゼントに入れる
	presents := make([]*UserPresent, 0, gachaCount)
	for _, v := range result {
		pID, err := h.generateID(ctx)
		if err != nil {
			return errorResponse(c, http.StatusInternalServerError, err)
		}
		present := &UserPresent{
			ID:             pID,
			UserID:         userID,
			SentAt:         requestAt,
			ItemType:       v.ItemType,
			ItemID:         v.ItemID,
			Amount:         v.Amount,
			PresentMessage: fmt.Sprintf("%sの付与アイテムです", gachaName),
			CreatedAt:      requestAt,
			UpdatedAt:      requestAt,
		}

		presents = append(presents, present)
	}
	query = "INSERT INTO user_presents" +
		"(id, user_id, sent_at, item_type, item_id, amount, present_message, created_at, updated_at) VALUES " +
		"(:id, :user_id, :sent_at, :item_type, :item_id, :amount, :present_message, :created_at, :updated_at)"
	if _, err := tx.NamedExecContext(ctx, query, presents); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// isuconをへらす
	query = "UPDATE users SET isu_coin=? WHERE id=?"
	totalCoin := user.IsuCoin - consumedCoin
	if _, err := tx.ExecContext(ctx, query, totalCoin, user.ID); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	err = tx.Commit()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	return successResponse(c, &DrawGachaResponse{
		Presents: presents,
	})
}

type DrawGachaRequest struct {
	ViewerID     string `json:"viewerId"`
	OneTimeToken string `json:"oneTimeToken"`
}

type DrawGachaResponse struct {
	Presents []*UserPresent `json:"presents"`
}

// listPresent プレゼント一覧
// GET /user/{userID}/present/index/{n}
func (h *Handler) listPresent(c echo.Context) error {
	ctx := c.Request().Context()
	n, err := strconv.Atoi(c.Param("n"))
	if err != nil {
		return errorResponse(c, http.StatusBadRequest, fmt.Errorf("invalid index number (n) parameter"))
	}
	if n == 0 {
		return errorResponse(c, http.StatusBadRequest, fmt.Errorf("index number (n) should be more than or equal to 1"))
	}

	userID, err := getUserID(c)
	if err != nil {
		return errorResponse(c, http.StatusBadRequest, fmt.Errorf("invalid userID parameter"))
	}

	offset := PresentCountPerPage * (n - 1)
	presentList := []*UserPresent{}
	query := `
	SELECT * FROM user_presents 
	WHERE user_id = ? AND deleted_at IS NULL
	ORDER BY created_at DESC, id
	LIMIT ? OFFSET ?`
	_db := h.chooseUserDB(userID)
	// 1件余分に取ってきてisNextの判定に使う
	if err = _db.SelectContext(ctx, &presentList, query, userID, PresentCountPerPage+1, offset); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	isNext := false
	if len(presentList) == PresentCountPerPage+1 {
		presentList = presentList[:PresentCountPerPage]
		isNext = true
	}

	return successResponse(c, &ListPresentResponse{
		Presents: presentList,
		IsNext:   isNext,
	})
}

type ListPresentResponse struct {
	Presents []*UserPresent `json:"presents"`
	IsNext   bool           `json:"isNext"`
}

// receivePresent プレゼント受け取り
// POST /user/{userID}/present/receive
func (h *Handler) receivePresent(c echo.Context) error {
	ctx := c.Request().Context()
	// read body
	defer c.Request().Body.Close()
	req := new(ReceivePresentRequest)
	if err := parseRequestBody(c, req); err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	userID, err := getUserID(c)
	if err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	requestAt, err := getRequestTime(c)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, ErrGetRequestTime)
	}

	if len(req.PresentIDs) == 0 {
		return errorResponse(c, http.StatusUnprocessableEntity, fmt.Errorf("presentIds is empty"))
	}

	if err = h.checkViewerID(ctx, userID, req.ViewerID); err != nil {
		if err == ErrUserDeviceNotFound {
			return errorResponse(c, http.StatusNotFound, err)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// user_presentsに入っているが未取得のプレゼント取得
	query := "SELECT * FROM user_presents WHERE id IN (?) AND deleted_at IS NULL"
	query, params, err := sqlx.In(query, req.PresentIDs)
	if err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}
	obtainPresent := []*UserPresent{}
	_db := h.chooseUserDB(userID)
	if err = _db.SelectContext(ctx, &obtainPresent, query, params...); err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	if len(obtainPresent) == 0 {
		return successResponse(c, &ReceivePresentResponse{
			UpdatedResources: makeUpdatedResources(requestAt, nil, nil, nil, nil, nil, nil, []*UserPresent{}),
		})
	}

	tx, err := _db.Beginx()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	defer tx.Rollback() //nolint:errcheck

	// 配布処理
	obtainer := &ItemObtainer{}
	var obtainItemIDs []int64
	for i := range obtainPresent {
		if obtainPresent[i].DeletedAt != nil {
			return errorResponse(c, http.StatusInternalServerError, fmt.Errorf("received present"))
		}

		obtainPresent[i].UpdatedAt = requestAt
		obtainPresent[i].DeletedAt = &requestAt
		v := obtainPresent[i]
		obtainItemIDs = append(obtainItemIDs, v.ID)

		err = obtainer.ObtainItem(v.ItemID, v.ItemType, int64(v.Amount))
		if err != nil {
			if err == ErrUserNotFound || err == ErrItemNotFound {
				return errorResponse(c, http.StatusNotFound, err)
			}
			if err == ErrInvalidItemType {
				return errorResponse(c, http.StatusBadRequest, err)
			}
			return errorResponse(c, http.StatusInternalServerError, err)
		}
	}
	if len(obtainItemIDs) > 0 {
		query, args, err := sqlx.In(
			"UPDATE user_presents SET deleted_at=?, updated_at=? WHERE id IN (?)",
			requestAt, requestAt, obtainItemIDs,
		)
		if err != nil {
			return errorResponse(c, http.StatusInternalServerError, err)
		}
		_, err = tx.ExecContext(ctx, query, args...)
		if err != nil {
			return errorResponse(c, http.StatusInternalServerError, err)
		}
	}

	_, _, _, err = obtainer.Commit(ctx, h, tx, userID, requestAt)
	if err != nil {
		if err == ErrUserNotFound || err == ErrItemNotFound {
			c.Logger().Debugf("error: %v", err)
			return errorResponse(c, http.StatusNotFound, err)
		}
		if err == ErrInvalidItemType {
			return errorResponse(c, http.StatusBadRequest, err)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	err = tx.Commit()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	return successResponse(c, &ReceivePresentResponse{
		UpdatedResources: makeUpdatedResources(requestAt, nil, nil, nil, nil, nil, nil, obtainPresent),
	})
}

type ReceivePresentRequest struct {
	ViewerID   string  `json:"viewerId"`
	PresentIDs []int64 `json:"presentIds"`
}

type ReceivePresentResponse struct {
	UpdatedResources *UpdatedResource `json:"updatedResources"`
}

// listItem アイテムリスト
// GET /user/{userID}/item
func (h *Handler) listItem(c echo.Context) error {
	ctx := c.Request().Context()
	userID, err := getUserID(c)
	if err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	requestAt, err := getRequestTime(c)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, ErrGetRequestTime)
	}

	user := new(User)
	query := "SELECT * FROM users WHERE id=?"
	_db := h.chooseUserDB(userID)
	if err = _db.GetContext(ctx, user, query, userID); err != nil {
		if err == sql.ErrNoRows {
			return errorResponse(c, http.StatusNotFound, ErrUserNotFound)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	itemList := []*UserItem{}
	query = "SELECT * FROM user_items WHERE user_id = ?"
	if err = _db.SelectContext(ctx, &itemList, query, userID); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	cardList := make([]*UserCard, 0)
	query = "SELECT * FROM user_cards WHERE user_id=?"
	if err = _db.SelectContext(ctx, &cardList, query, userID); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// genearte one time token
	err = h.Redis.Del(ctx, allUserOneTimeTokenKey(userID)).Err()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	tk, err := generateUUID(userID)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	err = h.Redis.Set(ctx, userOneTimeTokenKey(userID, tk), formatUserOneTimeTokenValue(2, requestAt+600), 0).Err()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	return successResponse(c, &ListItemResponse{
		OneTimeToken: tk,
		Items:        itemList,
		User:         user,
		Cards:        cardList,
	})
}

type ListItemResponse struct {
	OneTimeToken string      `json:"oneTimeToken"`
	User         *User       `json:"user"`
	Items        []*UserItem `json:"items"`
	Cards        []*UserCard `json:"cards"`
}

// addExpToCard 装備強化
// POST /user/{userID}/card/addexp/{cardID}
func (h *Handler) addExpToCard(c echo.Context) error {
	ctx := c.Request().Context()
	cardID, err := strconv.ParseInt(c.Param("cardID"), 10, 64)
	if err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	userID, err := getUserID(c)
	if err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	// read body
	defer c.Request().Body.Close()
	req := new(AddExpToCardRequest)
	if err := parseRequestBody(c, req); err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	requestAt, err := getRequestTime(c)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, ErrGetRequestTime)
	}

	if err = h.checkOneTimeToken(ctx, userID, req.OneTimeToken, 2, requestAt); err != nil {
		if err == ErrInvalidToken {
			return errorResponse(c, http.StatusBadRequest, err)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	if err = h.checkViewerID(ctx, userID, req.ViewerID); err != nil {
		if err == ErrUserDeviceNotFound {
			return errorResponse(c, http.StatusNotFound, err)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// get target card
	card := new(TargetUserCardData)
	query := `
	SELECT uc.id , uc.user_id , uc.card_id , uc.amount_per_sec , uc.level, uc.total_exp, im.amount_per_sec as 'base_amount_per_sec', im.max_level , im.max_amount_per_sec , im.base_exp_per_level
	FROM user_cards as uc
	INNER JOIN item_masters as im ON uc.card_id = im.id
	WHERE uc.id = ? AND uc.user_id=?
	`
	_db := h.chooseUserDB(userID)
	if err = _db.GetContext(ctx, card, query, cardID, userID); err != nil {
		if err == sql.ErrNoRows {
			return errorResponse(c, http.StatusNotFound, err)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	if card.Level == card.MaxLevel {
		return errorResponse(c, http.StatusBadRequest, fmt.Errorf("target card is max level"))
	}

	// 消費アイテムの所持チェック
	items := make([]*ConsumeUserItemData, 0)
	query = `
	SELECT ui.id, ui.user_id, ui.item_id, ui.item_type, ui.amount, ui.created_at, ui.updated_at, im.gained_exp
	FROM user_items as ui
	INNER JOIN item_masters as im ON ui.item_id = im.id
	WHERE ui.item_type = 3 AND ui.id=? AND ui.user_id=?
	`
	for _, v := range req.Items {
		item := new(ConsumeUserItemData)
		if err = _db.GetContext(ctx, item, query, v.ID, userID); err != nil {
			if err == sql.ErrNoRows {
				return errorResponse(c, http.StatusNotFound, err)
			}
			return errorResponse(c, http.StatusInternalServerError, err)
		}

		if v.Amount > item.Amount {
			return errorResponse(c, http.StatusBadRequest, fmt.Errorf("item not enough"))
		}
		item.ConsumeAmount = v.Amount
		items = append(items, item)
	}

	// 経験値付与
	// 経験値をカードに付与
	for _, v := range items {
		card.TotalExp += v.GainedExp * v.ConsumeAmount
	}

	// lvup判定(lv upしたら生産性を加算)
	for {
		nextLvThreshold := int(float64(card.BaseExpPerLevel) * math.Pow(1.2, float64(card.Level-1)))
		if nextLvThreshold > card.TotalExp {
			break
		}

		// lv up処理
		card.Level += 1
		card.AmountPerSec += (card.MaxAmountPerSec - card.BaseAmountPerSec) / (card.MaxLevel - 1)
	}

	tx, err := _db.Beginx()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	defer tx.Rollback() //nolint:errcheck

	// cardのlvと経験値の更新、itemの消費
	query = "UPDATE user_cards SET amount_per_sec=?, level=?, total_exp=?, updated_at=? WHERE id=?"
	if _, err = tx.ExecContext(ctx, query, card.AmountPerSec, card.Level, card.TotalExp, requestAt, card.ID); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// user_itemsを一括UPDATE (REPLACE)
	var rows []*UserItem
	for _, v := range items {
		rows = append(rows, &UserItem{
			ID:        v.ID,
			UserID:    v.UserID,
			ItemType:  v.ItemType,
			ItemID:    v.ItemID,
			Amount:    v.Amount - v.ConsumeAmount,
			CreatedAt: v.CreatedAt,
			UpdatedAt: requestAt,
		})
	}
	query = "REPLACE INTO user_items " +
		"(id, user_id, item_type, item_id, amount, created_at, updated_at) VALUES " +
		"(:id, :user_id, :item_type, :item_id, :amount, :created_at, :updated_at)"
	if _, err = tx.NamedExecContext(ctx, query, rows); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// get response data
	resultCard := new(UserCard)
	query = "SELECT * FROM user_cards WHERE id=?"
	if err = tx.GetContext(ctx, resultCard, query, card.ID); err != nil {
		if err == sql.ErrNoRows {
			return errorResponse(c, http.StatusNotFound, fmt.Errorf("not found card"))
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	resultItems := make([]*UserItem, 0)
	for _, v := range items {
		resultItems = append(resultItems, &UserItem{
			ID:        v.ID,
			UserID:    v.UserID,
			ItemID:    v.ItemID,
			ItemType:  v.ItemType,
			Amount:    v.Amount - v.ConsumeAmount,
			CreatedAt: v.CreatedAt,
			UpdatedAt: requestAt,
		})
	}

	err = tx.Commit()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	return successResponse(c, &AddExpToCardResponse{
		UpdatedResources: makeUpdatedResources(requestAt, nil, nil, []*UserCard{resultCard}, nil, resultItems, nil, nil),
	})
}

type AddExpToCardRequest struct {
	ViewerID     string         `json:"viewerId"`
	OneTimeToken string         `json:"oneTimeToken"`
	Items        []*ConsumeItem `json:"items"`
}

type AddExpToCardResponse struct {
	UpdatedResources *UpdatedResource `json:"updatedResources"`
}

type ConsumeItem struct {
	ID     int64 `json:"id"`
	Amount int   `json:"amount"`
}

type ConsumeUserItemData struct {
	ID        int64 `db:"id"`
	UserID    int64 `db:"user_id"`
	ItemID    int64 `db:"item_id"`
	ItemType  int   `db:"item_type"`
	Amount    int   `db:"amount"`
	CreatedAt int64 `db:"created_at"`
	UpdatedAt int64 `db:"updated_at"`
	GainedExp int   `db:"gained_exp"`

	ConsumeAmount int // 消費量
}

type TargetUserCardData struct {
	ID           int64 `db:"id"`
	UserID       int64 `db:"user_id"`
	CardID       int64 `db:"card_id"`
	AmountPerSec int   `db:"amount_per_sec"`
	Level        int   `db:"level"`
	TotalExp     int   `db:"total_exp"`

	// lv1のときの生産性
	BaseAmountPerSec int `db:"base_amount_per_sec"`
	// 最高レベル
	MaxLevel int `db:"max_level"`
	// lv maxのときの生産性
	MaxAmountPerSec int `db:"max_amount_per_sec"`
	// lv1 -> lv2に上がるときのexp
	BaseExpPerLevel int `db:"base_exp_per_level"`
}

// updateDeck 装備変更
// POST /user/{userID}/card
func (h *Handler) updateDeck(c echo.Context) error {
	ctx := c.Request().Context()

	userID, err := getUserID(c)
	if err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	// read body
	defer c.Request().Body.Close()
	req := new(UpdateDeckRequest)
	if err := parseRequestBody(c, req); err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	if len(req.CardIDs) != DeckCardNumber {
		return errorResponse(c, http.StatusBadRequest, fmt.Errorf("invalid number of cards"))
	}

	requestAt, err := getRequestTime(c)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, ErrGetRequestTime)
	}

	if err = h.checkViewerID(ctx, userID, req.ViewerID); err != nil {
		if err == ErrUserDeviceNotFound {
			return errorResponse(c, http.StatusNotFound, err)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// カード所持情報のバリデーション
	query := "SELECT * FROM user_cards WHERE id IN (?)"
	query, params, err := sqlx.In(query, req.CardIDs)
	if err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}
	cards := make([]*UserCard, 0)
	_db := h.chooseUserDB(userID)
	if err = _db.SelectContext(ctx, &cards, query, params...); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	if len(cards) != DeckCardNumber {
		return errorResponse(c, http.StatusBadRequest, fmt.Errorf("invalid card ids"))
	}

	tx, err := _db.Beginx()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	defer tx.Rollback() //nolint:errcheck

	// update data
	query = "UPDATE user_decks SET updated_at=?, deleted_at=? WHERE user_id=? AND deleted_at IS NULL"
	if _, err = tx.ExecContext(ctx, query, requestAt, requestAt, userID); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	udID, err := h.generateID(ctx)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	newDeck := &UserDeck{
		ID:        udID,
		UserID:    userID,
		CardID1:   req.CardIDs[0],
		CardID2:   req.CardIDs[1],
		CardID3:   req.CardIDs[2],
		CreatedAt: requestAt,
		UpdatedAt: requestAt,
	}
	query = "INSERT INTO user_decks(id, user_id, user_card_id_1, user_card_id_2, user_card_id_3, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)"
	if _, err := tx.ExecContext(ctx, query, newDeck.ID, newDeck.UserID, newDeck.CardID1, newDeck.CardID2, newDeck.CardID3, newDeck.CreatedAt, newDeck.UpdatedAt); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	err = tx.Commit()
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	return successResponse(c, &UpdateDeckResponse{
		UpdatedResources: makeUpdatedResources(requestAt, nil, nil, nil, []*UserDeck{newDeck}, nil, nil, nil),
	})
}

type UpdateDeckRequest struct {
	ViewerID string  `json:"viewerId"`
	CardIDs  []int64 `json:"cardIds"`
}

type UpdateDeckResponse struct {
	UpdatedResources *UpdatedResource `json:"updatedResources"`
}

// reward ゲーム報酬受取
// POST /user/{userID}/reward
func (h *Handler) reward(c echo.Context) error {
	ctx := c.Request().Context()
	userID, err := getUserID(c)
	if err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	// parse body
	defer c.Request().Body.Close()
	req := new(RewardRequest)
	if err := parseRequestBody(c, req); err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}

	requestAt, err := getRequestTime(c)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, ErrGetRequestTime)
	}

	if err = h.checkViewerID(ctx, userID, req.ViewerID); err != nil {
		if err == ErrUserDeviceNotFound {
			return errorResponse(c, http.StatusNotFound, err)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// 最後に取得した報酬時刻取得
	user := new(User)
	query := "SELECT * FROM users WHERE id=?"
	_db := h.chooseUserDB(userID)
	if err = _db.GetContext(ctx, user, query, userID); err != nil {
		if err == sql.ErrNoRows {
			return errorResponse(c, http.StatusNotFound, ErrUserNotFound)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	// 使っているデッキの取得
	deck := new(UserDeck)
	query = "SELECT * FROM user_decks WHERE user_id=? AND deleted_at IS NULL"
	if err = _db.GetContext(ctx, deck, query, userID); err != nil {
		if err == sql.ErrNoRows {
			return errorResponse(c, http.StatusNotFound, err)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	cards := make([]*UserCard, 0)
	query = "SELECT * FROM user_cards WHERE id IN (?, ?, ?)"
	if err = _db.SelectContext(ctx, &cards, query, deck.CardID1, deck.CardID2, deck.CardID3); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	if len(cards) != 3 {
		return errorResponse(c, http.StatusBadRequest, fmt.Errorf("invalid cards length"))
	}

	// 経過時間*生産性のcoin (1椅子 = 1coin)
	pastTime := requestAt - user.LastGetRewardAt
	getCoin := int(pastTime) * (cards[0].AmountPerSec + cards[1].AmountPerSec + cards[2].AmountPerSec)

	// 報酬の保存(ゲームない通貨を保存)(users)
	user.IsuCoin += int64(getCoin)
	user.LastGetRewardAt = requestAt

	query = "UPDATE users SET isu_coin=?, last_getreward_at=? WHERE id=?"
	if _, err = _db.ExecContext(ctx, query, user.IsuCoin, user.LastGetRewardAt, user.ID); err != nil {
		return errorResponse(c, http.StatusInternalServerError, err)
	}

	return successResponse(c, &RewardResponse{
		UpdatedResources: makeUpdatedResources(requestAt, user, nil, nil, nil, nil, nil, nil),
	})
}

type RewardRequest struct {
	ViewerID string `json:"viewerId"`
}

type RewardResponse struct {
	UpdatedResources *UpdatedResource `json:"updatedResources"`
}

// home ホーム取得
// GET /user/{userID}/home
func (h *Handler) home(c echo.Context) error {
	ctx := c.Request().Context()
	userID, err := getUserID(c)
	if err != nil {
		return errorResponse(c, http.StatusBadRequest, err)
	}
	c.Logger().Debugf("GET /user/{userID}/home userID: %d", userID)

	requestAt, err := getRequestTime(c)
	if err != nil {
		return errorResponse(c, http.StatusInternalServerError, ErrGetRequestTime)
	}

	// 装備情報
	deck := new(UserDeck)
	query := "SELECT * FROM user_decks WHERE user_id=? AND deleted_at IS NULL"
	_db := h.chooseUserDB(userID)
	if err = _db.GetContext(ctx, deck, query, userID); err != nil {
		if err != sql.ErrNoRows {
			return errorResponse(c, http.StatusInternalServerError, err)
		}
		deck = nil
	}

	// 生産性
	cards := make([]*UserCard, 0)
	if deck != nil {
		cardIds := []int64{deck.CardID1, deck.CardID2, deck.CardID3}
		query, params, err := sqlx.In("SELECT * FROM user_cards WHERE id IN (?)", cardIds)
		if err != nil {
			return errorResponse(c, http.StatusInternalServerError, err)
		}
		if err = _db.SelectContext(ctx, &cards, query, params...); err != nil {
			return errorResponse(c, http.StatusInternalServerError, err)
		}
	}
	totalAmountPerSec := 0
	for _, v := range cards {
		totalAmountPerSec += v.AmountPerSec
	}

	// 経過時間
	user := new(User)
	query = "SELECT * FROM users WHERE id=?"
	if err = _db.GetContext(ctx, user, query, userID); err != nil {
		if err == sql.ErrNoRows {
			return errorResponse(c, http.StatusNotFound, ErrUserNotFound)
		}
		return errorResponse(c, http.StatusInternalServerError, err)
	}
	pastTime := requestAt - user.LastGetRewardAt

	return successResponse(c, &HomeResponse{
		Now:               requestAt,
		User:              user,
		Deck:              deck,
		TotalAmountPerSec: totalAmountPerSec,
		PastTime:          pastTime,
	})
}

type HomeResponse struct {
	Now               int64     `json:"now"`
	User              *User     `json:"user"`
	Deck              *UserDeck `json:"deck,omitempty"`
	TotalAmountPerSec int       `json:"totalAmountPerSec"`
	PastTime          int64     `json:"pastTime"` // 経過時間を秒単位で
}

// //////////////////////////////////////
// util

// health ヘルスチェック
func (h *Handler) health(c echo.Context) error {
	return c.String(http.StatusOK, "OK")
}

// errorResponse returns error.
func errorResponse(c echo.Context, statusCode int, err error) error {
	if c != nil && c.Logger() != nil {
		c.Logger().Errorf("status=%d, err=%+v", statusCode, errors.WithStack(err))
	} else {
		fmt.Printf("status=%d, err=%+v", statusCode, errors.WithStack(err))
	}

	return c.JSON(statusCode, struct {
		StatusCode int    `json:"status_code"`
		Message    string `json:"message"`
	}{
		StatusCode: statusCode,
		Message:    err.Error(),
	})
}

// successResponse responds success.
func successResponse(c echo.Context, v interface{}) error {
	return c.JSON(http.StatusOK, v)
}

// noContentResponse
func noContentResponse(c echo.Context, status int) error {
	return c.NoContent(status)
}

var lastID int64 = 0

// generateID uniqueなIDを生成する
func (h *Handler) generateID(ctx context.Context) (int64, error) {
	if lastID == 0 {
		lastID = time.Now().Unix()*1000 + h.AppID
	}
	lastID += 7
	return lastID, nil
}

// generateSessionID
func generateUUID(userID int64) (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%d,%s", userID, id.String()), nil
}

func splitSessionID(sessionID string) (int64, string) {
	splitted := strings.Split(sessionID, ",")
	if len(splitted) != 2 {
		return 0, "session_not_found"
	}
	userID, err := strconv.ParseInt(splitted[0], 10, 64)
	if err != nil {
		return -1, "parse_failed"
	}
	return userID, splitted[1]
}

// getUserID gets userID by path param.
func getUserID(c echo.Context) (int64, error) {
	return strconv.ParseInt(c.Param("userID"), 10, 64)
}

// getEnv gets environment variable.
func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v == "" {
		return defaultVal
	} else {
		return v
	}
}

// parseRequestBody parses request body.
func parseRequestBody(c echo.Context, dist interface{}) error {
	buf, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return ErrInvalidRequestBody
	}
	if err = json.Unmarshal(buf, &dist); err != nil {
		return ErrInvalidRequestBody
	}
	return nil
}

type UpdatedResource struct {
	Now  int64 `json:"now"`
	User *User `json:"user,omitempty"`

	UserDevice       *UserDevice       `json:"userDevice,omitempty"`
	UserCards        []*UserCard       `json:"userCards,omitempty"`
	UserDecks        []*UserDeck       `json:"userDecks,omitempty"`
	UserItems        []*UserItem       `json:"userItems,omitempty"`
	UserLoginBonuses []*UserLoginBonus `json:"userLoginBonuses,omitempty"`
	UserPresents     []*UserPresent    `json:"userPresents,omitempty"`
}

func makeUpdatedResources(
	requestAt int64,
	user *User,
	userDevice *UserDevice,
	userCards []*UserCard,
	userDecks []*UserDeck,
	userItems []*UserItem,
	userLoginBonuses []*UserLoginBonus,
	userPresents []*UserPresent,
) *UpdatedResource {
	return &UpdatedResource{
		Now:              requestAt,
		User:             user,
		UserDevice:       userDevice,
		UserCards:        userCards,
		UserItems:        userItems,
		UserDecks:        userDecks,
		UserLoginBonuses: userLoginBonuses,
		UserPresents:     userPresents,
	}
}

// //////////////////////////////////////
// entity

type User struct {
	ID              int64  `json:"id" db:"id"`
	IsuCoin         int64  `json:"isuCoin" db:"isu_coin"`
	LastGetRewardAt int64  `json:"lastGetRewardAt" db:"last_getreward_at"`
	LastActivatedAt int64  `json:"lastActivatedAt" db:"last_activated_at"`
	RegisteredAt    int64  `json:"registeredAt" db:"registered_at"`
	CreatedAt       int64  `json:"createdAt" db:"created_at"`
	UpdatedAt       int64  `json:"updatedAt" db:"updated_at"`
	DeletedAt       *int64 `json:"deletedAt,omitempty" db:"deleted_at"`
}

type UserDevice struct {
	ID           int64  `json:"id" db:"id"`
	UserID       int64  `json:"userId" db:"user_id"`
	PlatformID   string `json:"platformId" db:"platform_id"`
	PlatformType int    `json:"platformType" db:"platform_type"`
	CreatedAt    int64  `json:"createdAt" db:"created_at"`
	UpdatedAt    int64  `json:"updatedAt" db:"updated_at"`
	DeletedAt    *int64 `json:"deletedAt,omitempty" db:"deleted_at"`
}

type UserBan struct {
	ID        int64  `db:"id"`
	UserID    int64  `db:"user_id"`
	CreatedAt int64  `db:"created_at"`
	UpdatedAt int64  `db:"updated_at"`
	DeletedAt *int64 `db:"deleted_at"`
}

type UserCard struct {
	ID           int64  `json:"id" db:"id"`
	UserID       int64  `json:"userId" db:"user_id"`
	CardID       int64  `json:"cardId" db:"card_id"`
	AmountPerSec int    `json:"amountPerSec" db:"amount_per_sec"`
	Level        int    `json:"level" db:"level"`
	TotalExp     int64  `json:"totalExp" db:"total_exp"`
	CreatedAt    int64  `json:"createdAt" db:"created_at"`
	UpdatedAt    int64  `json:"updatedAt" db:"updated_at"`
	DeletedAt    *int64 `json:"deletedAt,omitempty" db:"deleted_at"`
}

type UserDeck struct {
	ID        int64  `json:"id" db:"id"`
	UserID    int64  `json:"userId" db:"user_id"`
	CardID1   int64  `json:"cardId1" db:"user_card_id_1"`
	CardID2   int64  `json:"cardId2" db:"user_card_id_2"`
	CardID3   int64  `json:"cardId3" db:"user_card_id_3"`
	CreatedAt int64  `json:"createdAt" db:"created_at"`
	UpdatedAt int64  `json:"updatedAt" db:"updated_at"`
	DeletedAt *int64 `json:"deletedAt,omitempty" db:"deleted_at"`
}

type UserItem struct {
	ID        int64  `json:"id" db:"id"`
	UserID    int64  `json:"userId" db:"user_id"`
	ItemType  int    `json:"itemType" db:"item_type"`
	ItemID    int64  `json:"itemId" db:"item_id"`
	Amount    int    `json:"amount" db:"amount"`
	CreatedAt int64  `json:"createdAt" db:"created_at"`
	UpdatedAt int64  `json:"updatedAt" db:"updated_at"`
	DeletedAt *int64 `json:"deletedAt,omitempty" db:"deleted_at"`
}

type UserLoginBonus struct {
	ID                 int64  `json:"id" db:"id"`
	UserID             int64  `json:"userId" db:"user_id"`
	LoginBonusID       int64  `json:"loginBonusId" db:"login_bonus_id"`
	LastRewardSequence int    `json:"lastRewardSequence" db:"last_reward_sequence"`
	LoopCount          int    `json:"loopCount" db:"loop_count"`
	CreatedAt          int64  `json:"createdAt" db:"created_at"`
	UpdatedAt          int64  `json:"updatedAt" db:"updated_at"`
	DeletedAt          *int64 `json:"deletedAt,omitempty" db:"deleted_at"`
}

type UserPresent struct {
	ID             int64  `json:"id" db:"id"`
	UserID         int64  `json:"userId" db:"user_id"`
	SentAt         int64  `json:"sentAt" db:"sent_at"`
	ItemType       int    `json:"itemType" db:"item_type"`
	ItemID         int64  `json:"itemId" db:"item_id"`
	Amount         int    `json:"amount" db:"amount"`
	PresentMessage string `json:"presentMessage" db:"present_message"`
	CreatedAt      int64  `json:"createdAt" db:"created_at"`
	UpdatedAt      int64  `json:"updatedAt" db:"updated_at"`
	DeletedAt      *int64 `json:"deletedAt,omitempty" db:"deleted_at"`
}

type UserPresentAllReceivedHistory struct {
	ID           int64  `json:"id" db:"id"`
	UserID       int64  `json:"userId" db:"user_id"`
	PresentAllID int64  `json:"presentAllId" db:"present_all_id"`
	ReceivedAt   int64  `json:"receivedAt" db:"received_at"`
	CreatedAt    int64  `json:"createdAt" db:"created_at"`
	UpdatedAt    int64  `json:"updatedAt" db:"updated_at"`
	DeletedAt    *int64 `json:"deletedAt,omitempty" db:"deleted_at"`
}

type Session struct {
	ID        int64  `json:"id" db:"id"`
	UserID    int64  `json:"userId" db:"user_id"`
	SessionID string `json:"sessionId" db:"session_id"`
	ExpiredAt int64  `json:"expiredAt" db:"expired_at"`
	CreatedAt int64  `json:"createdAt" db:"created_at"`
	UpdatedAt int64  `json:"updatedAt" db:"updated_at"`
	DeletedAt *int64 `json:"deletedAt,omitempty" db:"deleted_at"`
}

type UserOneTimeToken struct {
	ID        int64  `json:"id" db:"id"`
	UserID    int64  `json:"userId" db:"user_id"`
	Token     string `json:"token" db:"token"`
	TokenType int    `json:"tokenType" db:"token_type"`
	ExpiredAt int64  `json:"expiredAt" db:"expired_at"`
	CreatedAt int64  `json:"createdAt" db:"created_at"`
	UpdatedAt int64  `json:"updatedAt" db:"updated_at"`
	DeletedAt *int64 `json:"deletedAt,omitempty" db:"deleted_at"`
}

// //////////////////////////////////////
// master

type GachaMaster struct {
	ID           int64  `json:"id" db:"id"`
	Name         string `json:"name" db:"name"`
	StartAt      int64  `json:"startAt" db:"start_at"`
	EndAt        int64  `json:"endAt" db:"end_at"`
	DisplayOrder int    `json:"displayOrder" db:"display_order"`
	CreatedAt    int64  `json:"createdAt" db:"created_at"`
}

type GachaItemMaster struct {
	ID        int64 `json:"id" db:"id"`
	GachaID   int64 `json:"gachaId" db:"gacha_id"`
	ItemType  int   `json:"itemType" db:"item_type"`
	ItemID    int64 `json:"itemId" db:"item_id"`
	Amount    int   `json:"amount" db:"amount"`
	Weight    int   `json:"weight" db:"weight"`
	CreatedAt int64 `json:"createdAt" db:"created_at"`
}

type ItemMaster struct {
	ID              int64  `json:"id" db:"id"`
	ItemType        int    `json:"itemType" db:"item_type"`
	Name            string `json:"name" db:"name"`
	Description     string `json:"description" db:"description"`
	AmountPerSec    *int   `json:"amountPerSec" db:"amount_per_sec"`
	MaxLevel        *int   `json:"maxLevel" db:"max_level"`
	MaxAmountPerSec *int   `json:"maxAmountPerSec" db:"max_amount_per_sec"`
	BaseExpPerLevel *int   `json:"baseExpPerLevel" db:"base_exp_per_level"`
	GainedExp       *int   `json:"gainedExp" db:"gained_exp"`
	ShorteningMin   *int64 `json:"shorteningMin" db:"shortening_min"`
	// CreatedAt       int64 `json:"createdAt"`
}

type LoginBonusMaster struct {
	ID          int64 `json:"id" db:"id"`
	StartAt     int64 `json:"startAt" db:"start_at"`
	EndAt       int64 `json:"endAt" db:"end_at"`
	ColumnCount int   `json:"columnCount" db:"column_count"`
	Looped      bool  `json:"looped" db:"looped"`
	CreatedAt   int64 `json:"createdAt" db:"created_at"`
}

type LoginBonusRewardMaster struct {
	ID             int64 `json:"id" db:"id"`
	LoginBonusID   int64 `json:"loginBonusId" db:"login_bonus_id"`
	RewardSequence int   `json:"rewardSequence" db:"reward_sequence"`
	ItemType       int   `json:"itemType" db:"item_type"`
	ItemID         int64 `json:"itemId" db:"item_id"`
	Amount         int64 `json:"amount" db:"amount"`
	CreatedAt      int64 `json:"createdAt" db:"created_at"`
}

type PresentAllMaster struct {
	ID                int64  `json:"id" db:"id"`
	RegisteredStartAt int64  `json:"registeredStartAt" db:"registered_start_at"`
	RegisteredEndAt   int64  `json:"registeredEndAt" db:"registered_end_at"`
	ItemType          int    `json:"itemType" db:"item_type"`
	ItemID            int64  `json:"itemId" db:"item_id"`
	Amount            int64  `json:"amount" db:"amount"`
	PresentMessage    string `json:"presentMessage" db:"present_message"`
	CreatedAt         int64  `json:"createdAt" db:"created_at"`
}

type VersionMaster struct {
	ID            int64  `json:"id" db:"id"`
	Status        int    `json:"status" db:"status"`
	MasterVersion string `json:"masterVersion" db:"master_version"`
}
