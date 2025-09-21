package ranking

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/go-redis/redis/v8"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

// NFT представляет структуру NFT из Google Sheets
type NFT struct {
	ID           string
	Name         string
	Description  string
	ReleaseDate  string
	Rarity       string
	Price        int
	Collection   string
	ImageURL     string
	BasePriceUSD float64   // Базовая цена из мапы
	LastUpdated  time.Time // Время последнего обновления цены
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
	"Legendary":  0xFF0000,
}

// RarityProbabilities определяет вероятности выпадения
var RarityProbabilities = []RarityProb{
	{"Common", 0.5},
	{"Rare", 0.25},
	{"Super-rare", 0.10},
	{"Epic", 0.05},
	{"Nephrite", 0.01},
	{"Exotic", 0.01},
	{"Legendary", 0.005},
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

// GetNFTs возвращает копию карты NFT для автодополнения
func (k *KKI) GetNFTs() map[string]NFT {
	k.mu.Lock()
	defer k.mu.Unlock()

	// Создаем копию карты
	nftsCopy := make(map[string]NFT)
	for k, v := range k.nfts {
		nftsCopy[k] = v
	}
	return nftsCopy
}

// GetCases возвращает копию карты кейсов для автодополнения
func (k *KKI) GetCases() map[string]Case {
	k.mu.Lock()
	defer k.mu.Unlock()

	// Создаем копию карты
	casesCopy := make(map[string]Case)
	for k, v := range k.cases {
		casesCopy[k] = v
	}
	return casesCopy
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
	resp, err := k.sheets.Spreadsheets.Values.Get(os.Getenv("GOOGLE_SHEETS_ID"), "NFTs!A:G").Do()
	if err != nil {
		return fmt.Errorf("не удалось загрузить NFTs: %v", err)
	}

	k.nfts = make(map[string]NFT)
	for i, row := range resp.Values {
		if i == 0 {
			continue
		}
		if len(row) < 7 {
			continue
		}

		rarity := fmt.Sprintf("%v", row[4])
		basePrice, exists := BaseRarityPrices[rarity]
		if !exists {
			log.Printf("Warning: Unknown rarity '%s', using default price 10", rarity)
			basePrice = 10
		}

		nft := NFT{
			ID:           fmt.Sprintf("%v", row[0]),
			Name:         fmt.Sprintf("%v", row[1]),
			Description:  fmt.Sprintf("%v", row[2]),
			ReleaseDate:  fmt.Sprintf("%v", row[3]),
			Rarity:       rarity,
			Collection:   fmt.Sprintf("%v", row[5]),
			ImageURL:     fmt.Sprintf("%v", row[6]),
			BasePriceUSD: basePrice,
		}

		log.Printf("Loaded NFT: %s, Rarity: %s, BasePrice: $%.0f", nft.Name, nft.Rarity, nft.BasePriceUSD)

		// Вычисляем текущую цену
		nft.Price = r.CalculateNFTPrice(nft)
		nft.LastUpdated = time.Now()

		k.nfts[nft.ID] = nft
		jsonData, _ := json.Marshal(nft)
		r.redis.Set(r.ctx, "nft:"+nft.ID, jsonData, 0)
	}

	log.Printf("Total NFTs loaded: %d", len(k.nfts))

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

// StartPriceUpdater запускает обновление цен каждые 15 минут
func (r *Ranking) StartPriceUpdater() {
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				log.Printf("🔄 Автоматическое обновление цен NFT...")

				// Обновляем курс BTC
				_, err := r.GetBitcoinPrice()
				if err != nil {
					log.Printf("Ошибка обновления курса BTC: %v", err)
					continue
				}

				// Обновляем цены всех NFT
				r.mu.Lock()
				for id, nft := range r.Kki.nfts {
					newPrice := r.CalculateNFTPrice(nft)
					if newPrice != nft.Price {
						nft.Price = newPrice
						nft.LastUpdated = time.Now()
						r.Kki.nfts[id] = nft

						// Обновляем в Redis
						jsonData, _ := json.Marshal(nft)
						r.redis.Set(r.ctx, "nft:"+nft.ID, jsonData, 0)
					}
				}
				r.mu.Unlock()

				log.Printf("✅ Цены NFT обновлены по курсу BTC: $%.2f", r.BitcoinTracker.CurrentPrice)

			case <-r.stopResetChan:
				return
			}
		}
	}()
}

// HandleBitcoinPriceCommand !btc
func (r *Ranking) HandleBitcoinPriceCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	price := r.BitcoinTracker.CurrentPrice
	avgPrice := r.BitcoinTracker.Get24hAverage()
	volatility := r.BitcoinTracker.CalculateVolatility() * 100

	change := ((price - avgPrice) / avgPrice) * 100
	changeEmoji := "➡️"
	if change > 5 {
		changeEmoji = "📈"
	}
	if change < -5 {
		changeEmoji = "📉"
	}

	embed := &discordgo.MessageEmbed{
		Title: "💰 Курс биткойна",
		Description: fmt.Sprintf("**Текущая цена**: $%.2f %s\n**24ч средняя**: $%.2f\n**Изменение**: %.1f%%\n**Волатильность**: %.1f%%",
			price, changeEmoji, avgPrice, change, volatility),
		Color:  0xF7931A,
		Footer: &discordgo.MessageEmbedFooter{Text: "Влияет на цены редких NFT"},
	}
	s.ChannelMessageSendEmbed(m.ChannelID, embed)
}

// HandlePriceStatsCommand !price_stats - детальная статистика
func (r *Ranking) HandlePriceStatsCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Добавляем проверку на загруженные NFT
	if len(r.Kki.nfts) == 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ **NFT не загружены! Используйте !sync_nfts**")
		return
	}

	btcPrice := r.BitcoinTracker.CurrentPrice
	btcAvg := r.BitcoinTracker.Get24hAverage()
	btcVolatility := r.BitcoinTracker.CalculateVolatility() * 100
	btcChange := ((btcPrice - btcAvg) / btcAvg) * 100

	var lines []string
	for _, rarity := range []string{"Common", "Rare", "Super-rare", "Epic", "Nephrite", "Exotic", "Legendary"} {
		// Ищем реальный NFT этой редкости для расчета
		var exampleNFT *NFT
		for _, nft := range r.Kki.nfts {
			if nft.Rarity == rarity {
				exampleNFT = &nft
				break
			}
		}

		// Если не нашли NFT такой редкости, используем базовую цену
		if exampleNFT == nil {
			basePrice := BaseRarityPrices[rarity]
			lines = append(lines, fmt.Sprintf("%s **%s**:\n- Базовая: $%.0f\n- Текущая: $%.0f\n- Изменение: 0.0%% ➡️\n- Волатильность: %.0f%%",
				RarityEmojis[rarity], rarity, basePrice, basePrice, RarityVolatility[rarity]*100))
			continue
		}

		currentPrice := r.CalculateNFTPrice(*exampleNFT)
		basePrice := exampleNFT.BasePriceUSD

		change := (float64(currentPrice) - basePrice) / basePrice * 100
		emoji := "➡️"
		if change > 5 {
			emoji = "📈"
		}
		if change < -5 {
			emoji = "📉"
		}
		if math.Abs(change) > 20 {
			emoji = "🚀"
		}
		if math.Abs(change) < -20 {
			emoji = "💥"
		}

		lines = append(lines, fmt.Sprintf("%s **%s**:\n- Базовая: $%.0f\n- Текущая: $%d\n- Изменение: %.1f%% %s\n- Волатильность: %.0f%%",
			RarityEmojis[rarity], rarity, basePrice, currentPrice, change, emoji, RarityVolatility[rarity]*100))
	}

	embed := &discordgo.MessageEmbed{
		Title: "📊 **Детальная статистика цен**",
		Description: fmt.Sprintf("💰 **BTC**: $%.2f (Δ %.1f%%, волатильность %.1f%%)\n\n%s",
			btcPrice, btcChange, btcVolatility, strings.Join(lines, "\n\n")),
		Color:  0x00BFFF,
		Footer: &discordgo.MessageEmbedFooter{Text: "Цены обновляются каждые 15 минут"},
	}
	s.ChannelMessageSendEmbed(m.ChannelID, embed)
}
