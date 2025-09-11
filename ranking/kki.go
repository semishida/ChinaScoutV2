package ranking

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/go-redis/redis/v8"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

// NFT представляет структуру NFT из Google Sheets
type NFT struct {
	ID          string
	Name        string
	Description string
	ReleaseDate string
	Rarity      string
	Price       int
	Collection  string
	ImageURL    string
}

// Case представляет кейс с коллекциями
type Case struct {
	ID                   string
	Name                 string
	ContainedCollections string
	Price                int
}

// UserInventory хранит количество NFT для пользователя
type UserInventory map[string]int

// UserCaseInventory хранит количество кейсов для пользователя
type UserCaseInventory map[string]int

// RarityProb определяет вероятности выпадения редкостей
type RarityProb struct {
	Rarity string
	Prob   float64
}

// RarityColors определяет цвета для редкостей в Discord embed
var RarityColors = map[string]int{
	"Common":     0xFFFFFF,
	"Rare":       0x1E90FF,
	"Super-rare": 0xFFD700,
	"Epic":       0x9932CC,
	"Nephrite":   0x00FF7F,
	"Exotic":     0xFF4500,
	"LEGENDARY":  0xFF0000,
}

// RarityProbabilities определяет вероятности выпадения
var RarityProbabilities = []RarityProb{
	{"Common", 0.5},
	{"Rare", 0.25},
	{"Super-rare", 0.12},
	{"Epic", 0.07},
	{"Nephrite", 0.04},
	{"Exotic", 0.015},
	{"LEGENDARY", 0.005},
}

// KKI управляет NFT и кейсами
type KKI struct {
	nfts   map[string]NFT
	cases  map[string]Case
	redis  *redis.Client
	ctx    context.Context
	sheets *sheets.Service
	mu     sync.Mutex
}

// NewKKI инициализирует KKI с подключением к Google Sheets и Redis
func NewKKI(ctx context.Context) (*KKI, error) {
	sheetID := os.Getenv("GOOGLE_SHEETS_ID")
	if sheetID == "" {
		return nil, fmt.Errorf("GOOGLE_SHEETS_ID не указан")
	}

	credsPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if credsPath == "" {
		return nil, fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS не указан")
	}

	srv, err := sheets.NewService(ctx, option.WithCredentialsFile(credsPath))
	if err != nil {
		return nil, fmt.Errorf("не удалось создать сервис Google Sheets: %v", err)
	}

	k := &KKI{
		nfts:   make(map[string]NFT),
		cases:  make(map[string]Case),
		redis:  redis.NewClient(&redis.Options{Addr: os.Getenv("REDIS_ADDR"), Password: os.Getenv("REDIS_PASSWORD")}),
		ctx:    ctx,
		sheets: srv,
	}
	return k, nil
}

// SyncFromSheets загружает данные из Google Sheets в Redis и память
func (k *KKI) SyncFromSheets(r *Ranking) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	// Загрузка NFT
	resp, err := k.sheets.Spreadsheets.Values.Get(os.Getenv("GOOGLE_SHEETS_ID"), "NFTs!A:H").Do()
	if err != nil {
		return fmt.Errorf("не удалось загрузить NFTs: %v", err)
	}

	k.nfts = make(map[string]NFT)
	for i, row := range resp.Values {
		if i == 0 {
			continue // Пропускаем заголовок
		}
		if len(row) < 8 {
			continue
		}
		price, _ := strconv.Atoi(fmt.Sprintf("%v", row[5]))
		nft := NFT{
			ID:          fmt.Sprintf("%v", row[0]),
			Name:        fmt.Sprintf("%v", row[1]),
			Description: fmt.Sprintf("%v", row[2]),
			ReleaseDate: fmt.Sprintf("%v", row[3]),
			Rarity:      fmt.Sprintf("%v", row[4]),
			Price:       price,
			Collection:  fmt.Sprintf("%v", row[6]),
			ImageURL:    fmt.Sprintf("%v", row[7]),
		}
		k.nfts[nft.ID] = nft
		jsonData, _ := json.Marshal(nft)
		r.redis.Set(r.ctx, "nft:"+nft.ID, jsonData, 0)
	}

	// Загрузка кейсов
	resp, err = k.sheets.Spreadsheets.Values.Get(os.Getenv("GOOGLE_SHEETS_ID"), "Cases!A:D").Do()
	if err != nil {
		return fmt.Errorf("не удалось загрузить Cases: %v", err)
	}

	k.cases = make(map[string]Case)
	for i, row := range resp.Values {
		if i == 0 {
			continue // Пропускаем заголовок
		}
		if len(row) < 4 {
			continue
		}
		price, _ := strconv.Atoi(fmt.Sprintf("%v", row[3]))
		kase := Case{
			ID:                   fmt.Sprintf("%v", row[0]),
			Name:                 fmt.Sprintf("%v", row[1]),
			ContainedCollections: fmt.Sprintf("%v", row[2]),
			Price:                price,
		}
		k.cases[kase.ID] = kase
		jsonData, _ := json.Marshal(kase)
		r.redis.Set(r.ctx, "case:"+kase.ID, jsonData, 0)
	}

	return nil
}

// GetUserCaseInventory получает инвентарь кейсов пользователя
func (k *KKI) GetUserCaseInventory(r *Ranking, userID string) UserCaseInventory {
	jsonData, err := r.redis.Get(r.ctx, "case_inventory:"+userID).Bytes()
	if err == redis.Nil {
		return make(UserCaseInventory)
	}
	var inv UserCaseInventory
	if err := json.Unmarshal(jsonData, &inv); err != nil {
		return make(UserCaseInventory)
	}
	return inv
}

// SaveUserCaseInventory сохраняет инвентарь кейсов пользователя
func (k *KKI) SaveUserCaseInventory(r *Ranking, userID string, inv UserCaseInventory) error {
	jsonData, _ := json.Marshal(inv)
	r.redis.Set(r.ctx, "case_inventory:"+userID, jsonData, 0)
	return nil
}
