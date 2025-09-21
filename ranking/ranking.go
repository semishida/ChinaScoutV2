package ranking

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/go-redis/redis/v8"
	"github.com/joho/godotenv"
)

// CaseBank представляет структуру банка кейсов
type CaseBank struct {
	Cases       map[string]int `json:"cases"`
	LastUpdated time.Time      `json:"last_updated"`
}

var RarityEmojis = map[string]string{
	"Common":     "🟦",
	"Rare":       "🟪",
	"Super-rare": "🟧",
	"Epic":       "🟨",
	"Nephrite":   "🟥",
	"Exotic":     "🟩",
	"Legendary":  "⭐",
}

// BitcoinTracker отслеживает курс и волатильность BTC
type BitcoinTracker struct {
	CurrentPrice  float64
	PreviousPrice float64
	LastUpdate    time.Time
	PriceHistory  []float64
	mu            sync.Mutex
}

// RarityVolatility определяет волатильность цены для каждой редкости
var RarityVolatility = map[string]float64{
	"Common":     10.0,   // ±50% - было 0.1 (10%)
	"Rare":       100.0,  // ±100% - было 0.3 (30%)
	"Super-rare": 200.0,  // ±200% - было 0.6 (60%)
	"Epic":       400.0,  // ±400% - было 1.0 (100%)
	"Nephrite":   600.0,  // ±600% - было 1.5 (150%)
	"Exotic":     800.0,  // ±800% - было 2.0 (200%)
	"Legendary":  1000.0, // ±1000% - было 3.0 (300%)
}

// BaseRarityPrices базовые цены в USD для каждой редкости
var BaseRarityPrices = map[string]float64{
	"Common":     10,
	"Rare":       50,
	"Super-rare": 200,
	"Epic":       1000,
	"Nephrite":   5000,
	"Exotic":     5000,
	"Legendary":  10000,
}

// Ranking управляет рейтингами, опросами, играми и голосовой активностью.
type Ranking struct {
	mu                sync.Mutex
	admins            map[string]bool
	polls             map[string]*Poll
	duels             map[string]*Duel
	redis             *redis.Client
	ctx               context.Context
	voiceAct          map[string]int
	redBlackGames     map[string]*RedBlackGame
	blackjackGames    map[string]*BlackjackGame
	floodChannelID    string
	logChannelID      string
	cinemaOptions     []CinemaOption
	pendingCinemaBids map[string]PendingCinemaBid
	cinemaChannelID   string
	Kki               *KKI
	sellMessageIDs    map[string]string // userID -> messageID
	caseBank          *CaseBank
	stopResetChan     chan struct{}
	BitcoinTracker    *BitcoinTracker // НОВОЕ ПОЛЕ
}

// NewRanking инициализирует структуру Ranking.
func NewRanking(adminFilePath, redisAddr, floodChannelID, cinemaChannelID string) (*Ranking, error) {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Ошибка загрузки .env файла")
	}

	r := &Ranking{
		admins:            make(map[string]bool),
		polls:             make(map[string]*Poll),
		duels:             make(map[string]*Duel),
		voiceAct:          map[string]int{},
		redBlackGames:     make(map[string]*RedBlackGame),
		blackjackGames:    make(map[string]*BlackjackGame),
		ctx:               context.Background(),
		floodChannelID:    floodChannelID,
		logChannelID:      os.Getenv("LOG_CHANNEL_ID"),
		cinemaOptions:     []CinemaOption{},
		pendingCinemaBids: make(map[string]PendingCinemaBid),
		cinemaChannelID:   cinemaChannelID,
		sellMessageIDs:    make(map[string]string),
		caseBank: &CaseBank{
			Cases:       make(map[string]int),
			LastUpdated: time.Now(),
		},
		BitcoinTracker: &BitcoinTracker{
			PriceHistory: make([]float64, 0),
		},
	}

	// Подключение к Redis с повторными попытками
	var redisErr error
	for i := 0; i < 5; i++ {
		r.redis = redis.NewClient(&redis.Options{
			Addr:     redisAddr,
			Password: os.Getenv("REDIS_PASSWORD"),
		})
		_, redisErr = r.redis.Ping(r.ctx).Result()
		if redisErr == nil {
			break
		}
		log.Printf("Не удалось подключиться к Redis (попытка %d/5): %v", i+1, redisErr)
		time.Sleep(5 * time.Second)
	}
	if redisErr != nil {
		return nil, fmt.Errorf("не удалось подключиться к Redis после 5 попыток: %v", redisErr)
	}

	// Загрузка администраторов из файла
	file, err := os.Open(adminFilePath)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть файл администраторов: %v", err)
	}
	defer file.Close()

	var admins struct {
		IDs []string `json:"admin_ids"`
	}
	if err := json.NewDecoder(file).Decode(&admins); err != nil {
		return nil, fmt.Errorf("не удалось разобрать файл администраторов: %v", err)
	}
	for _, id := range admins.IDs {
		r.admins[id] = true
	}

	// Первоначальное получение курса BTC
	if _, err := r.GetBitcoinPrice(); err != nil {
		log.Printf("Предупреждение: не удалось получить курс BTC: %v", err)
	}

	// Запускаем обновление цен
	go r.StartPriceUpdater()

	r.stopResetChan = make(chan struct{})
	go r.startDailyReset()
	// Загрузка cinema options
	r.LoadCinemaOptions()

	// Инициализация KKI
	r.Kki, err = NewKKI(r.ctx)
	if err != nil {
		log.Fatalf("Failed to init KKI: %v", err)
	}
	if err := r.Kki.SyncFromSheets(r); err != nil {
		log.Printf("Failed initial sync: %v", err)
	}

	log.Printf("Инициализирован рейтинг с %d администраторами", len(r.admins))

	// Инициализация банка кейсов
	r.initializeCaseBank()
	// Запуск обновления банка кейсов каждые 10 минут
	go r.StartBitcoinUpdater() // <- ДОБАВЬТЕ ЭТУ СТРОКУ

	return r, nil
}

// IsAdmin проверяет, является ли пользователь администратором.
func (r *Ranking) IsAdmin(userID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	isAdmin := r.admins[userID]
	log.Printf("Проверка администратора %s: %v", userID, isAdmin)
	return isAdmin
}

// GetPolls возвращает копию карты опросов для автодополнения
func (r *Ranking) GetPolls() map[string]*Poll {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Создаем копию карты
	pollsCopy := make(map[string]*Poll)
	for k, v := range r.polls {
		pollsCopy[k] = v
	}
	return pollsCopy
}

// generateGameID создаёт уникальный ID для игры.
func generateGameID(playerID string) string {
	rand.Seed(time.Now().UnixNano())
	// Заменяем _ на - для избежания проблем с парсингом
	safePlayerID := strings.ReplaceAll(playerID, "_", "-")
	return fmt.Sprintf("%s-%d-%d", safePlayerID, time.Now().UnixNano(), rand.Intn(10000))
}

// generatePollID создаёт уникальный 5-символьный ID для опроса.
func generatePollID() string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	rand.Seed(time.Now().UnixNano())
	id := make([]byte, 5)
	for i := range id {
		id[i] = letters[rand.Intn(len(letters))]
	}
	return string(id)
}

// LogCreditOperation отправляет лог операции с кредитами в канал логов.
func (r *Ranking) LogCreditOperation(s *discordgo.Session, message string) {
	if r.logChannelID != "" {
		_, err := s.ChannelMessageSend(r.logChannelID, message)
		if err != nil {
			log.Printf("Не удалось отправить лог в канал %s: %v", r.logChannelID, err)
		}
	}
}

// GetUserInventory возвращает инвентарь NFT пользователя
func (r *Ranking) GetUserInventory(userID string) UserInventory {
	jsonData, err := r.redis.Get(r.ctx, "inventory:"+userID).Bytes()
	if err == redis.Nil {
		return make(UserInventory)
	}
	var inv UserInventory
	if err := json.Unmarshal(jsonData, &inv); err != nil {
		return make(UserInventory)
	}
	return inv
}

// SaveUserInventory сохраняет инвентарь NFT пользователя
func (r *Ranking) SaveUserInventory(userID string, inv UserInventory) {
	jsonData, _ := json.Marshal(inv)
	r.redis.Set(r.ctx, "inventory:"+userID, jsonData, 0)
}

// HandleInventoryCommand отображает инвентарь пользователя
func (r *Ranking) HandleInventoryCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	inv := r.GetUserInventory(m.Author.ID)
	if len(inv) == 0 {
		s.ChannelMessageSend(m.ChannelID, "🎒 **Ваш инвентарь пуст** ══════\nНичего нет, Император ждёт добычи! 😢")
		return
	}

	var lines []string
	for nftID, count := range inv {
		nft, ok := r.Kki.nfts[nftID]
		if !ok {
			continue
		}
		rarityEmoji := RarityEmojis[nft.Rarity]
		lines = append(lines, fmt.Sprintf("%s **%s** (x%d)\n📌 ID для передачи и продажи: %s\n💰 Цена: %d | %s", rarityEmoji, nft.Name, count, nftID, nft.Price, nft.Rarity))
	}
	sort.Strings(lines)
	embed := &discordgo.MessageEmbed{
		Title:       "🎒 **Инвентарь** ══════",
		Description: strings.Join(lines, "\n\n"),
		Color:       0x00FF00,
		Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Владелец: %s | Славь Императора! 👑", m.Author.Username)},
	}
	s.ChannelMessageSendEmbed(m.ChannelID, embed)
}

// HandleSellCommand !sell <nftID> <count>
func (r *Ranking) HandleSellCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Fields(command)
	if len(parts) != 3 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Использование**: !sell <nftID> <count>")
		return
	}
	nftID, countStr := parts[1], parts[2]
	count, err := strconv.Atoi(countStr)
	if err != nil || count <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Некорректное количество**")
		return
	}

	// Проверка NFT
	nft, ok := r.Kki.nfts[nftID]
	if !ok {
		s.ChannelMessageSend(m.ChannelID, "❌ **NFT не найдено. Проверьте ID.**")
		return
	}

	// Проверка инвентаря
	inv := r.GetUserInventory(m.Author.ID)
	if inv[nftID] < count {
		s.ChannelMessageSend(m.ChannelID, "❌ **Недостаточно NFT для продажи.**")
		return
	}

	// Расчёт суммы
	sellPrice := nft.Price / 2 * count

	// Отправка сообщения с подтверждением
	customID := fmt.Sprintf("sell_confirm_%s_%s_%d_%d", m.Author.ID, nftID, count, sellPrice)
	cancelID := fmt.Sprintf("sell_cancel_%s", m.Author.ID)
	embed := &discordgo.MessageEmbed{
		Title:       "🃏 **Подтверждение продажи** ══════",
		Description: fmt.Sprintf("Вы хотите продать %d x %s **%s** (ID для передачи и продажи: %s) за 💰 %d кредитов?", count, RarityEmojis[nft.Rarity], nft.Name, nftID, sellPrice),
		Color:       RarityColors[nft.Rarity],
		Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Владелец: %s | Славь Императора! 👑", m.Author.Username)},
	}
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "✅ Подтвердить",
					Style:    discordgo.SuccessButton,
					CustomID: customID,
				},
				discordgo.Button{
					Label:    "❌ Отменить",
					Style:    discordgo.DangerButton,
					CustomID: cancelID,
				},
			},
		},
	}
	msg, err := s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Embed:      embed,
		Components: components,
	})
	if err != nil {
		log.Printf("Failed to send sell confirmation: %v", err)
		return
	}
	r.mu.Lock()
	r.sellMessageIDs[m.Author.ID] = msg.ID
	r.mu.Unlock()
}

// HandleSellConfirm обрабатывает подтверждение продажи
func (r *Ranking) HandleSellConfirm(s *discordgo.Session, i *discordgo.InteractionCreate) {
	parts := strings.Split(i.MessageComponentData().CustomID, "_")
	if len(parts) != 6 {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ **Ошибка обработки продажи.**"},
		})
		return
	}
	userID, nftID, countStr, sellPriceStr := parts[2], parts[3], parts[4], parts[5]
	if userID != i.Member.User.ID {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ **Кнопка не для вас! Император гневен! 👑**"},
		})
		return
	}
	count, err := strconv.Atoi(countStr)
	if err != nil {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ **Ошибка: некорректное количество.**"},
		})
		return
	}
	sellPrice, err := strconv.Atoi(sellPriceStr)
	if err != nil {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ **Ошибка: некорректная сумма.**"},
		})
		return
	}

	inv := r.GetUserInventory(userID)
	if inv[nftID] < count {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ **Недостаточно NFT.**"},
		})
		return
	}

	// Уменьшение NFT
	inv[nftID] -= count
	if inv[nftID] == 0 {
		delete(inv, nftID)
	}
	r.SaveUserInventory(userID, inv)

	// Начисление кредитов
	r.UpdateRating(userID, sellPrice)

	// Отправка лога
	nft := r.Kki.nfts[nftID]
	r.LogCreditOperation(s, fmt.Sprintf("🃏 **%s** продал %d x %s **%s** (ID: %s) за 💰 %d кредитов.", i.Member.User.Username, count, RarityEmojis[nft.Rarity], nft.Name, nftID, sellPrice))

	// Обновление сообщения для удаления кнопок
	embed := &discordgo.MessageEmbed{
		Title:       "🃏 **Продажа завершена** ══════",
		Description: fmt.Sprintf("✅ **Продано** %d x %s **%s** (ID: %s) за 💰 %d кредитов!", count, RarityEmojis[nft.Rarity], nft.Name, nftID, sellPrice),
		Color:       RarityColors[nft.Rarity],
		Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Владелец: %s | Славь Императора! 👑", i.Member.User.Username)},
	}
	emptyComponents := []discordgo.MessageComponent{}
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    i.ChannelID,
		ID:         i.Message.ID,
		Embed:      embed,
		Components: &emptyComponents,
	})
	if err != nil {
		log.Printf("Failed to update sell message: %v", err)
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("✅ **Продано** %d x %s **%s** (ID: %s) за 💰 %d кредитов!", count, RarityEmojis[nft.Rarity], nft.Name, nftID, sellPrice),
		},
	})

	r.mu.Lock()
	delete(r.sellMessageIDs, userID)
	r.mu.Unlock()
}

// HandleSellCancel обрабатывает отмену продажи
func (r *Ranking) HandleSellCancel(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if strings.Split(i.MessageComponentData().CustomID, "_")[2] != i.Member.User.ID {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ **Кнопка не для вас! Император гневен! 👑**"},
		})
		return
	}
	embed := &discordgo.MessageEmbed{
		Title:       "🃏 **Продажа отменена** ══════",
		Description: "❌ Продажа отменена. Император разочарован! 😢",
		Color:       0xFF0000,
		Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Владелец: %s | Славь Императора! 👑", i.Member.User.Username)},
	}
	emptyComponents := []discordgo.MessageComponent{}
	_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    i.ChannelID,
		ID:         i.Message.ID,
		Embed:      embed,
		Components: &emptyComponents,
	})
	if err != nil {
		log.Printf("Failed to update sell cancel message: %v", err)
	}
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: "❌ **Продажа отменена.**"},
	})
	r.mu.Lock()
	delete(r.sellMessageIDs, i.Member.User.ID)
	r.mu.Unlock()
}

// HandleTradeNFTCommand !trade_nft <@user> <nftID> <count>
func (r *Ranking) HandleTradeNFTCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	if len(m.Mentions) != 1 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Упомяните одного пользователя**: !trade_nft @user <nftID> <count>")
		return
	}
	targetID := m.Mentions[0].ID
	if targetID == m.Author.ID {
		s.ChannelMessageSend(m.ChannelID, "❌ **Нельзя передать NFT себе.**")
		return
	}
	parts := strings.Fields(command)
	if len(parts) != 4 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Использование**: !trade_nft @user <nftID> <count>")
		return
	}
	nftID, countStr := parts[2], parts[3]
	count, err := strconv.Atoi(countStr)
	if err != nil || count <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Некорректное количество.**")
		return
	}

	// Проверка NFT
	nft, ok := r.Kki.nfts[nftID]
	if !ok {
		s.ChannelMessageSend(m.ChannelID, "❌ **NFT не найдено. Проверьте ID.**")
		return
	}

	// Проверка инвентаря
	inv := r.GetUserInventory(m.Author.ID)
	if inv[nftID] < count {
		s.ChannelMessageSend(m.ChannelID, "❌ **Недостаточно NFT для передачи.**")
		return
	}

	// Передача NFT
	inv[nftID] -= count
	if inv[nftID] == 0 {
		delete(inv, nftID)
	}
	r.SaveUserInventory(m.Author.ID, inv)

	targetInv := r.GetUserInventory(targetID)
	targetInv[nftID] += count
	r.SaveUserInventory(targetID, targetInv)

	// Ответ
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("✅ **Передано** %d x 🃏 **%s** (ID для передачи и продажи: %s) пользователю <@%s>.", count, nft.Name, nftID, targetID))
}

// HandleCaseTradeCommand !case_trade <@user> <caseID> <count>
func (r *Ranking) HandleCaseTradeCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	if len(m.Mentions) != 1 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Упомяните одного пользователя**: !case_trade @user <caseID> <count>")
		return
	}
	sellerID := m.Mentions[0].ID
	if sellerID == m.Author.ID {
		s.ChannelMessageSend(m.ChannelID, "❌ **Нельзя купить кейс у себя.**")
		return
	}
	parts := strings.Fields(command)
	if len(parts) != 4 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Использование**: !case_trade @user <caseID> <count>")
		return
	}
	caseID := parts[2]
	count, err := strconv.Atoi(parts[3])
	if err != nil || count <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Некорректное количество.**")
		return
	}

	// Унификация daily_case
	if caseID == "daily" {
		caseID = "daily_case"
	}
	kase, ok := r.Kki.cases[caseID]
	if !ok {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ **Кейс с ID %s не найден. Проверьте ID.**", caseID))
		return
	}

	sellerInv := r.Kki.GetUserCaseInventory(r, sellerID)
	if sellerInv[caseID] < count {
		sellerInvStr := ""
		for id, c := range sellerInv {
			k, _ := r.Kki.cases[id]
			sellerInvStr += fmt.Sprintf("%s (ID: %s, x%d), ", k.Name, id, c)
		}
		if sellerInvStr == "" {
			sellerInvStr = "пуст"
		}
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ **У продавца недостаточно кейсов.** Инвентарь продавца: %s", sellerInvStr))
		return
	}

	price := kase.Price * count
	buyerCoins := r.GetRating(m.Author.ID)
	if buyerCoins < price {
		s.ChannelMessageSend(m.ChannelID, "❌ **Недостаточно кредитов.**")
		return
	}

	// Обновление кредитов
	r.UpdateRating(m.Author.ID, -price)
	r.UpdateRating(sellerID, price)

	// Обновление инвентаря
	buyerInv := r.Kki.GetUserCaseInventory(r, m.Author.ID)
	buyerInv[caseID] += count
	r.Kki.SaveUserCaseInventory(r, m.Author.ID, buyerInv)

	sellerInv[caseID] -= count
	if sellerInv[caseID] == 0 {
		delete(sellerInv, caseID)
	}
	r.Kki.SaveUserCaseInventory(r, sellerID, sellerInv)

	// Лог операции
	r.LogCreditOperation(s, fmt.Sprintf("🛒 **%s** купил %d x 📦 **%s** (ID: %s) у <@%s> за 💰 %d кредитов.", m.Author.Username, count, kase.Name, caseID, sellerID, price))

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🛒 **Куплено** %d x 📦 **%s** (ID для открытия/передачи: %s) у <@%s> за 💰 %d кредитов.", count, kase.Name, caseID, sellerID, price))
}

// HandleOpenCaseCommand !open_case <caseID>
func (r *Ranking) HandleOpenCaseCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Split(command, " ")
	if len(parts) < 2 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Использование**: !open_case <caseID>")
		return
	}
	caseID := parts[1]
	if caseID == "daily" {
		caseID = "daily_case"
	}
	kase, ok := r.Kki.cases[caseID]
	if !ok {
		s.ChannelMessageSend(m.ChannelID, "❌ **Некорректный кейс. Проверьте ID.**")
		return
	}

	// Проверка инвентаря кейсов
	userCaseInv := r.Kki.GetUserCaseInventory(r, m.Author.ID)
	if userCaseInv[caseID] < 1 {
		s.ChannelMessageSend(m.ChannelID, "❌ **У вас нет этого кейса.**")
		return
	}
	userCaseInv[caseID]--
	if userCaseInv[caseID] == 0 {
		delete(userCaseInv, caseID)
	}
	r.Kki.SaveUserCaseInventory(r, m.Author.ID, userCaseInv)

	// Проверка дневного лимита
	key := fmt.Sprintf("case_limit:%s:%s", m.Author.ID, time.Now().Format("2006-01-02"))
	opened, _ := r.redis.Get(r.ctx, key).Int()
	if opened >= 5 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Достигнут дневной лимит (5 кейсов в день).**")
		return
	}
	r.redis.Incr(r.ctx, key)
	r.redis.Expire(r.ctx, key, 24*time.Hour)

	// Начало анимации
	animMsg, _ := s.ChannelMessageSend(m.ChannelID, "🎰 **Открываем кейс...**")
	collections := strings.Split(kase.ContainedCollections, ",")
	var possibleNFTs []NFT
	for _, nft := range r.Kki.nfts {
		for _, col := range collections {
			if nft.Collection == col && (caseID != "daily_case" || nft.Collection != "holiday") {
				possibleNFTs = append(possibleNFTs, nft)
				break
			}
		}
	}
	if len(possibleNFTs) == 0 {
		s.ChannelMessageEdit(m.ChannelID, animMsg.ID, "❌ **В кейсе нет NFT.**")
		return
	}

	// Открытие 5 NFT
	var dropped []NFT
	for i := 0; i < 5; i++ {
		dropped = append(dropped, r.rollNFT(possibleNFTs))
	}

	// Анимация в горутине
	go func() {
		rarities := []string{"Common", "Rare", "Super-rare", "Epic", "Nephrite", "Exotic", "Legendary"}
		for i := 0; i < 10; i++ {
			randRarity := rarities[rand.Intn(len(rarities))]
			embed := &discordgo.MessageEmbed{
				Description: fmt.Sprintf("🎰 **Крутим...** %s", randRarity),
				Color:       RarityColors[randRarity],
			}
			s.ChannelMessageEditEmbed(m.ChannelID, animMsg.ID, embed)
			time.Sleep(300 * time.Millisecond)
		}

		// Показ выпавших NFT
		var lines []string
		inv := r.GetUserInventory(m.Author.ID)
		for _, nft := range dropped {
			wasEmpty := inv[nft.ID] == 0
			inv[nft.ID]++
			newTag := ""
			if wasEmpty {
				newTag = "\n**Новая в коллекции!** 🎉"
			}
			embed := &discordgo.MessageEmbed{
				Title:       fmt.Sprintf("🎉 **Выпало**: %s **%s**", RarityEmojis[nft.Rarity], nft.Name),
				Description: fmt.Sprintf("**ID для передачи и продажи**: %s\n**Редкость**: %s\n**Описание**: %s\n**Дата выпуска**: %s\n**Цена**: 💰 %d\n**Коллекция**: %s%s", nft.ID, nft.Rarity, nft.Description, nft.ReleaseDate, nft.Price, nft.Collection, newTag),
				Color:       RarityColors[nft.Rarity],
				Image:       &discordgo.MessageEmbedImage{URL: nft.ImageURL},
				Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Владелец: %s | Славь Императора! 👑", m.Author.Username)},
			}
			msg, err := s.ChannelMessageSendEmbed(m.ChannelID, embed)
			if err == nil {
				go func(msgID string) {
					time.Sleep(5 * time.Second)
					if err := s.ChannelMessageDelete(m.ChannelID, msgID); err != nil {
						log.Printf("Failed to delete message %s: %v", msgID, err)
					}
				}(msg.ID)
			} else {
				log.Printf("Failed to send embed for NFT %s: %v", nft.ID, err)
			}
			lines = append(lines, fmt.Sprintf("%s **%s** (ID: %s)", RarityEmojis[nft.Rarity], nft.Name, nft.ID))
			time.Sleep(1 * time.Second)
		}
		r.SaveUserInventory(m.Author.ID, inv)
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🎉 **Вы получили** ══════\n%s", strings.Join(lines, "\n")))
	}()
}

// rollNFT выбирает случайный NFT с учётом редкости
func (r *Ranking) rollNFT(possible []NFT) NFT {
	totalProb := 0.0
	for _, p := range RarityProbabilities {
		totalProb += p.Prob
	}
	roll := rand.Float64() * totalProb
	cum := 0.0
	var selectedRarity string
	for _, p := range RarityProbabilities {
		cum += p.Prob
		if roll <= cum {
			selectedRarity = p.Rarity
			break
		}
	}

	var candidates []NFT
	for _, nft := range possible {
		if nft.Rarity == selectedRarity {
			candidates = append(candidates, nft)
		}
	}
	if len(candidates) == 0 {
		return possible[rand.Intn(len(possible))]
	}
	return candidates[rand.Intn(len(candidates))]
}

// HandleDailyCaseCommand !daily_case
func (r *Ranking) HandleDailyCaseCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	key := fmt.Sprintf("daily_case:%s:%s", m.Author.ID, time.Now().Format("2006-01-02"))
	if r.redis.Exists(r.ctx, key).Val() > 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Ежедневный кейс уже получен сегодня.**")
		return
	}

	// Проверка наличия daily_case
	if _, ok := r.Kki.cases["daily_case"]; !ok {
		s.ChannelMessageSend(m.ChannelID, "❌ **Ежедневный кейс (ID: daily_case) не найден в базе. Проверьте Google Sheets.**")
		log.Printf("daily_case not found in r.Kki.cases")
		return
	}

	userCaseInv := r.Kki.GetUserCaseInventory(r, m.Author.ID)
	userCaseInv["daily_case"]++ // Исправлено с "daily" на "daily_case"
	err := r.Kki.SaveUserCaseInventory(r, m.Author.ID, userCaseInv)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ **Ошибка сохранения кейса. Попробуйте снова.**")
		log.Printf("Failed to save daily_case for user %s: %v", m.Author.ID, err)
		return
	}

	r.redis.Set(r.ctx, key, "claimed", 24*time.Hour)
	s.ChannelMessageSend(m.ChannelID, "✅ **Вы получили ежедневный кейс!** Используйте `!open_case daily_case` для открытия.")
}

// HandleBuyCaseFromCommand !buy_case_from <@user> <caseID> <count>
func (r *Ranking) HandleBuyCaseFromCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Split(command, " ")
	if len(parts) < 4 {
		s.ChannelMessageSend(m.ChannelID, "Использование: !buy_case_from @user <caseID> <count>")
		return
	}
	sellerID := strings.Trim(parts[1], "<@!>")
	caseID := parts[2]
	count, _ := strconv.Atoi(parts[3])

	kase, ok := r.Kki.cases[caseID]
	if !ok {
		s.ChannelMessageSend(m.ChannelID, "Некорректный кейс.")
		return
	}

	sellerInv := r.Kki.GetUserCaseInventory(r, sellerID)
	if sellerInv[caseID] < count {
		s.ChannelMessageSend(m.ChannelID, "У продавца недостаточно кейсов.")
		return
	}

	price := kase.Price * count
	r.LogCreditOperation(s, fmt.Sprintf("%s купил %d x %s у %s за %d кредитов", m.Author.Username, count, kase.Name, sellerID, price))

	buyerInv := r.Kki.GetUserCaseInventory(r, m.Author.ID)
	buyerInv[caseID] += count
	r.Kki.SaveUserCaseInventory(r, m.Author.ID, buyerInv)

	sellerInv[caseID] -= count
	if sellerInv[caseID] == 0 {
		delete(sellerInv, caseID)
	}
	r.Kki.SaveUserCaseInventory(r, sellerID, sellerInv)

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Куплено %d x %s у <@%s> за %d кредитов.", count, kase.Name, sellerID, price))
}

// HandleAdminGiveCase !admin_give_case <userID> <caseID>
func (r *Ranking) HandleAdminGiveCase(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	if len(m.Mentions) != 1 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Упомяните одного пользователя**: !a_give_case @user <caseID>")
		return
	}
	userID := m.Mentions[0].ID
	parts := strings.Fields(command)
	if len(parts) != 3 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Использование**: !a_give_case @user <caseID>")
		return
	}
	caseID := parts[2]
	kase, exists := r.Kki.cases[caseID]
	if !exists {
		s.ChannelMessageSend(m.ChannelID, "❌ **Кейс не найден. Проверьте ID.**")
		return
	}
	inv := r.Kki.GetUserCaseInventory(r, userID)
	inv[caseID]++
	r.Kki.SaveUserCaseInventory(r, userID, inv)
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("✅ **Выдан** 📦 **%s** (ID для открытия/передачи: %s) пользователю <@%s>.", kase.Name, caseID, userID))
}

// HandleAdminGiveNFT !admin_give_nft <userID> <nftID> <count>
func (r *Ranking) HandleAdminGiveNFT(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	if len(m.Mentions) != 1 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Упомяните одного пользователя**: !a_give_nft @user <nftID> <count>")
		return
	}
	userID := m.Mentions[0].ID
	parts := strings.Fields(command)
	if len(parts) != 4 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Использование**: !a_give_nft @user <nftID> <count>")
		return
	}
	nftID, countStr := parts[2], parts[3]
	count, err := strconv.Atoi(countStr)
	if err != nil || count <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Некорректное количество.**")
		return
	}

	// Проверка NFT
	nft, ok := r.Kki.nfts[nftID]
	if !ok {
		s.ChannelMessageSend(m.ChannelID, "❌ **NFT не найдено. Проверьте ID.**")
		return
	}

	inv := r.GetUserInventory(userID)
	inv[nftID] += count
	r.SaveUserInventory(userID, inv)

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("✅ **Выдано** %d x 🃏 **%s** (ID для передачи и продажи: %s) пользователю <@%s>.", count, nft.Name, nftID, userID))
}

// HandleAdminRemoveNFT !a_remove_nft <@user> <nftID> <count>
func (r *Ranking) HandleAdminRemoveNFT(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	if len(m.Mentions) != 1 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Упомяните одного пользователя**: !a_remove_nft @user <nftID> <count>")
		return
	}
	userID := m.Mentions[0].ID
	parts := strings.Fields(command)
	if len(parts) != 4 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Использование**: !a_remove_nft @user <nftID> <count>")
		return
	}
	nftID, countStr := parts[2], parts[3]
	count, err := strconv.Atoi(countStr)
	if err != nil || count <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Некорректное количество.**")
		return
	}

	// Проверка NFT
	nft, ok := r.Kki.nfts[nftID]
	if !ok {
		s.ChannelMessageSend(m.ChannelID, "❌ **NFT не найдено. Проверьте ID.**")
		return
	}

	inv := r.GetUserInventory(userID)
	if inv[nftID] < count {
		s.ChannelMessageSend(m.ChannelID, "❌ **Недостаточно NFT.**")
		return
	}
	inv[nftID] -= count
	if inv[nftID] == 0 {
		delete(inv, nftID)
	}
	r.SaveUserInventory(userID, inv)

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("✅ **Удалено** %d x 🃏 **%s** (ID для передачи и продажи: %s) у <@%s>.", count, nft.Name, nftID, userID))
}

// HandleAdminHolidayCase !a_holiday_case <@user> <count>
func (r *Ranking) HandleAdminHolidayCase(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	if len(m.Mentions) != 1 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Упомяните одного пользователя**: !a_holiday_case @user <count>")
		return
	}
	userID := m.Mentions[0].ID
	parts := strings.Fields(command)
	if len(parts) != 3 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Использование**: !a_holiday_case @user <count>")
		return
	}
	count, err := strconv.Atoi(parts[2])
	if err != nil || count <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Некорректное количество.**")
		return
	}

	inv := r.Kki.GetUserCaseInventory(r, userID)
	inv["holiday_case"] += count
	r.Kki.SaveUserCaseInventory(r, userID, inv)

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("✅ **Выдано** %d x 📦 **Праздничный кейс** (ID для открытия/передачи: holiday_case) пользователю <@%s>.", count, userID))
}

// HandleShowNFTCommand !show_nft <nftID>
func (r *Ranking) HandleShowNFTCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Fields(command)
	if len(parts) != 2 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Использование**: !nft_show <nftID>")
		return
	}
	nftID := parts[1]
	nft, ok := r.Kki.nfts[nftID]
	if !ok {
		s.ChannelMessageSend(m.ChannelID, "❌ **NFT не найдено. Проверьте ID.**")
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("🃏 %s **%s**", RarityEmojis[nft.Rarity], nft.Name),
		Description: fmt.Sprintf("**ID для передачи и продажи**: %s\n**Описание**: %s\n**Редкость**: %s\n**Дата выпуска**: %s\n**Цена**: 💰 %d\n**Коллекция**: %s", nftID, nft.Description, nft.Rarity, nft.ReleaseDate, nft.Price, nft.Collection),
		Color:       RarityColors[nft.Rarity],
		Image:       &discordgo.MessageEmbedImage{URL: nft.ImageURL},
		Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Похвастался: %s | Славь Императора! 👑", m.Author.Username)},
	}
	s.ChannelMessageSendEmbed(m.ChannelID, embed)
}

// ClearAllUserNFTs очищает все NFT и кейсы для теста
func (r *Ranking) ClearAllUserNFTs(s *discordgo.Session, m *discordgo.MessageCreate) {
	keys, _ := r.redis.Keys(r.ctx, "inventory:*").Result()
	for _, key := range keys {
		r.redis.Del(r.ctx, key)
	}
	keys, _ = r.redis.Keys(r.ctx, "case_inventory:*").Result()
	for _, key := range keys {
		r.redis.Del(r.ctx, key)
	}
	keys, _ = r.redis.Keys(r.ctx, "case_limit:*").Result()
	for _, key := range keys {
		r.redis.Del(r.ctx, key)
	}
	keys, _ = r.redis.Keys(r.ctx, "daily_case:*").Result()
	for _, key := range keys {
		r.redis.Del(r.ctx, key)
	}
	keys, _ = r.redis.Keys(r.ctx, "case_buy_limit:*").Result()
	for _, key := range keys {
		r.redis.Del(r.ctx, key)
	}
	// Сброс банка кейсов
	r.initializeCaseBank()

	s.ChannelMessageSend(m.ChannelID, "❌ **Все NFT, кейсы, лимиты и банк кейсов очищены.**")
}

// HandleCaseInventoryCommand отображает инвентарь кейсов пользователя и лимит открытия
func (r *Ranking) HandleCaseInventoryCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	inv := r.Kki.GetUserCaseInventory(r, m.Author.ID)
	if len(inv) == 0 {
		s.ChannelMessageSend(m.ChannelID, "📦 **Инвентарь кейсов пуст** ══════\nИмператор ждёт, открывай кейсы! 😤")
		return
	}

	var lines []string
	for caseID, count := range inv {
		// Унификация daily_case
		displayID := caseID
		if caseID == "daily_case" {
			displayID = "daily_case"
		}
		kase, ok := r.Kki.cases[caseID]
		if !ok {
			log.Printf("Case %s not found in r.Kki.cases for user %s", caseID, m.Author.ID)
			continue
		}
		lines = append(lines, fmt.Sprintf("📦 **%s** (x%d)\n📌 ID для открытия/передачи: %s\n💰 Цена: %d", kase.Name, count, displayID, kase.Price))
	}
	if len(lines) == 0 {
		s.ChannelMessageSend(m.ChannelID, "📦 **Инвентарь кейсов пуст** ══════\nИмператор ждёт, открывай кейсы! 😤")
		return
	}
	sort.Strings(lines)

	// Проверка дневного лимита
	key := fmt.Sprintf("case_limit:%s:%s", m.Author.ID, time.Now().Format("2006-01-02"))
	opened, _ := r.redis.Get(r.ctx, key).Int()
	limitMsg := fmt.Sprintf("🔄 **Лимит открытия кейсов сегодня**: %d/5", opened)

	embed := &discordgo.MessageEmbed{
		Title:       "📦 **Инвентарь кейсов** ══════",
		Description: strings.Join(lines, "\n\n") + "\n" + limitMsg,
		Color:       0x00BFFF,
		Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Владелец: %s | Славь Императора! 👑", m.Author.Username)},
	}
	s.ChannelMessageSendEmbed(m.ChannelID, embed)
}

// HandleAdminGiveHolidayCaseAll !a_give_holiday_case_all <count>
func (r *Ranking) HandleAdminGiveHolidayCaseAll(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ **Только админы могут использовать эту команду!**")
		return
	}
	parts := strings.Fields(command)
	if len(parts) != 2 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Использование**: !a_give_holiday_case_all <count>")
		return
	}
	count, err := strconv.Atoi(parts[1])
	if err != nil || count <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Некорректное количество.**")
		return
	}

	// Проверка наличия holiday_case
	if _, ok := r.Kki.cases["holiday_case"]; !ok {
		s.ChannelMessageSend(m.ChannelID, "❌ **Праздничный кейс (ID: holiday_case) не найден в базе. Проверьте Google Sheets.**")
		log.Printf("holiday_case not found in r.Kki.cases")
		return
	}

	// Получение всех участников гильдии
	guild, err := s.Guild(m.GuildID)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ **Ошибка получения списка участников. Проверьте права бота (View Guild Members).**")
		log.Printf("Failed to fetch guild members: %v", err)
		return
	}

	if len(guild.Members) == 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Гильдия пуста или бот не может получить участников. Проверьте права.**")
		log.Printf("No members found in guild %s", m.GuildID)
		return
	}

	successCount := 0
	for _, member := range guild.Members {
		if member.User.Bot {
			log.Printf("Skipping bot user %s", member.User.ID)
			continue
		}
		inv := r.Kki.GetUserCaseInventory(r, member.User.ID)
		inv["holiday_case"] += count
		err := r.Kki.SaveUserCaseInventory(r, member.User.ID, inv)
		if err != nil {
			log.Printf("Failed to save case inventory for user %s: %v", member.User.ID, err)
			continue
		}
		successCount++
		log.Printf("Added %d holiday_case to user %s", count, member.User.ID)
	}

	if successCount == 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Не удалось выдать кейсы ни одному участнику. Проверьте логи и права бота.**")
		log.Printf("No holiday cases distributed in guild %s", m.GuildID)
		return
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("✅ **Выдано** %d x 📦 **Праздничный кейс** (ID для открытия/передачи: holiday_case) %d участникам сервера!", count, successCount))
}

// HandleCaseHelpCommand !case_help - обновленная версия
func (r *Ranking) HandleCaseHelpCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	embed := &discordgo.MessageEmbed{
		Title:       "📦 **Помощь по кейсам, NFT и экономике** ══════",
		Description: "Славь Императора! 👑 Динамическая экономика привязана к курсу BTC",
		Color:       0xFFD700,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "💰 **Экономика и цены**",
				Value:  "```!btc - Текущий курс биткойна\n!prices - Динамика цен по редкостям\n!price_stats - Подробная статистика цен```",
				Inline: true,
			},
			{
				Name:   "📦 **Кейсы и инвентарь**",
				Value:  "```!case_inventory - Мои кейсы\n!open_case <ID> - Открыть кейс\n!daily_case - Ежедневный кейс\n!case_bank - Кейсы в банке\n!buy_case_bank <ID> <count> - Купить из банка\n!case_trade @user <ID> <count> - Купить у игрока```",
				Inline: true,
			},
			{
				Name:   "🃏 **NFT и торговля**",
				Value:  "```!inventory - Мои NFT\n!nft_show <ID> - Показать NFT\n!sell <ID> <count> - Продать NFT\n!trade_nft @user <ID> <count> - Передать NFT\n!market - Рыночные цены (скоро)```",
				Inline: true,
			},
			{
				Name:   "👑 **Админские команды**",
				Value:  "```!sync_nfts - Синхронизация с Sheets\n!a_give_case @user <ID> - Выдать кейс\n!a_give_nft @user <ID> <count> - Выдать NFT\n!a_remove_nft @user <ID> <count> - Удалить NFT\n!a_refresh_bank - Обновить банк кейсов\n!a_reset_case_limits - Сбросить лимиты\n!test_clear_all_nfts - Очистить всё```",
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Вызвал: %s | Редкие NFT зависят от курса BTC!", m.Author.Username),
		},
	}
	s.ChannelMessageSendEmbed(m.ChannelID, embed)
}

// initializeCaseBank инициализирует банк кейсов случайными кейсами
func (r *Ranking) initializeCaseBank() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Получаем все доступные кейсы
	allCases := make([]string, 0, len(r.Kki.cases))
	for caseID := range r.Kki.cases {
		allCases = append(allCases, caseID)
	}

	// Выбираем 2 случайных кейса
	if len(allCases) > 0 {
		rand.Seed(time.Now().UnixNano())
		rand.Shuffle(len(allCases), func(i, j int) {
			allCases[i], allCases[j] = allCases[j], allCases[i]
		})

		numToSelect := min(2, len(allCases))
		selectedCases := allCases[:numToSelect]

		newCases := make(map[string]int)
		for _, caseID := range selectedCases {
			newCases[caseID] = 70
		}

		r.caseBank = &CaseBank{
			Cases:       newCases,
			LastUpdated: time.Now(),
		}

		jsonData, _ := json.Marshal(r.caseBank)
		r.redis.Set(r.ctx, "case_bank", jsonData, 0)
		log.Printf("Case bank initialized with: %v", selectedCases)
	} else {
		// Fallback если кейсов нет
		r.caseBank = &CaseBank{
			Cases:       make(map[string]int),
			LastUpdated: time.Now(),
		}
		log.Printf("Case bank initialized empty - no cases available")
	}
}

// HandleAdminRefreshBankCommand !a_refresh_bank
func (r *Ranking) HandleAdminRefreshBankCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ **Только админы могут использовать эту команду!**")
		return
	}

	// Принудительно обновляем банк
	r.mu.Lock()
	defer r.mu.Unlock()

	// Получаем ВСЕ доступные кейсы из таблицы
	allCases := make([]string, 0, len(r.Kki.cases))
	for caseID := range r.Kki.cases {
		allCases = append(allCases, caseID)
	}

	// Рандомно выбираем 2 кейса
	if len(allCases) < 2 {
		s.ChannelMessageSend(m.ChannelID, "❌ **В таблице меньше 2 кейсов!**")
		return
	}

	// Перемешиваем и выбираем 2 случайных кейса
	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(allCases), func(i, j int) {
		allCases[i], allCases[j] = allCases[j], allCases[i]
	})
	selectedCases := allCases[:2]

	// Устанавливаем по 70 штук для каждого выбранного кейса
	newCases := make(map[string]int)
	for _, caseID := range selectedCases {
		newCases[caseID] = 70
	}

	r.caseBank.Cases = newCases
	r.caseBank.LastUpdated = time.Now()

	// Сохраняем в Redis
	jsonData, _ := json.Marshal(r.caseBank)
	r.redis.Set(r.ctx, "case_bank", jsonData, 0)

	// Формируем список выбранных кейсов для ответа
	var caseList []string
	for _, caseID := range selectedCases {
		kase := r.Kki.cases[caseID]
		caseList = append(caseList, fmt.Sprintf("📦 **%s** (ID: `%s`)", kase.Name, caseID))
	}

	embed := &discordgo.MessageEmbed{
		Title: "🔄 **Банк кейсов обновлен!**",
		Description: fmt.Sprintf("Выбраны случайные кейсы:\n%s\n\nКоличество: **70** каждого\nОбновлено: %s",
			strings.Join(caseList, "\n"), time.Now().Format("15:04:05")),
		Color:  0x00FF00,
		Footer: &discordgo.MessageEmbedFooter{Text: "Император одобряет случайный выбор!"},
	}

	s.ChannelMessageSendEmbed(m.ChannelID, embed)
	log.Printf("Банк кейсов обновлен вручную: %v", selectedCases)
}

// refreshCaseBank обновляет банк кейсов случайными кейсами из таблицы
func (r *Ranking) refreshCaseBank() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Загружаем текущий банк из Redis
	jsonData, err := r.redis.Get(r.ctx, "case_bank").Bytes()
	if err == redis.Nil {
		r.initializeCaseBank()
		return
	}
	var bank CaseBank
	if err := json.Unmarshal(jsonData, &bank); err != nil {
		log.Printf("Failed to unmarshal case_bank: %v", err)
		r.initializeCaseBank()
		return
	}
	r.caseBank = &bank

	// Обновляем если прошло 12 часов ИЛИ если банк пустой
	if time.Since(r.caseBank.LastUpdated) >= 12*time.Hour || len(r.caseBank.Cases) == 0 {
		// Получаем все доступные кейсы из таблицы
		allCases := make([]string, 0, len(r.Kki.cases))
		for caseID := range r.Kki.cases {
			allCases = append(allCases, caseID)
		}

		// Рандомно выбираем 2 кейса (если меньше 2, выбираем все)
		numToSelect := 2
		if len(allCases) < numToSelect {
			numToSelect = len(allCases)
		}

		// Перемешиваем массив
		rand.Seed(time.Now().UnixNano())
		rand.Shuffle(len(allCases), func(i, j int) {
			allCases[i], allCases[j] = allCases[j], allCases[i]
		})
		selectedCases := allCases[:numToSelect]

		// Устанавливаем по 70 штук для каждого выбранного кейса
		newCases := make(map[string]int)
		for _, caseID := range selectedCases {
			newCases[caseID] = 50
		}

		r.caseBank.Cases = newCases
		r.caseBank.LastUpdated = time.Now()

		jsonData, _ := json.Marshal(r.caseBank)
		r.redis.Set(r.ctx, "case_bank", jsonData, 0)
		log.Printf("Case bank refreshed at %s with cases: %v", time.Now(), selectedCases)
	}
}

// randomShuffle возвращает перемешанный слайс (Go не имеет встроенного random.shuffle)
func randomShuffle(slice []string) []string {
	n := len(slice)
	rand.Seed(time.Now().UnixNano())
	for i := n - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		slice[i], slice[j] = slice[j], slice[i]
	}
	return slice
}

// HandleCaseBankCommand !case_bank
func (r *Ranking) HandleCaseBankCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	r.refreshCaseBank()

	var lines []string
	for caseID, count := range r.caseBank.Cases {
		kase, ok := r.Kki.cases[caseID]
		if !ok {
			continue
		}
		lines = append(lines, fmt.Sprintf("📦 **%s** (x%d)\n📌 ID: %s\n💰 Цена: %d", kase.Name, count, caseID, kase.Price))
	}
	if len(lines) == 0 {
		s.ChannelMessageSend(m.ChannelID, "🏦 **Банк кейсов пуст** ══════\nИмператор ждёт новых поставок! 😢")
		return
	}

	nextUpdate := r.caseBank.LastUpdated.Add(12 * time.Hour)
	timeLeft := time.Until(nextUpdate).Round(time.Second)
	hours := int(timeLeft.Hours())
	minutes := int(timeLeft.Minutes()) % 60
	timeLeftStr := fmt.Sprintf("%dч %dм", hours, minutes)

	embed := &discordgo.MessageEmbed{
		Title:       "🏦 **Банк кейсов** ══════",
		Description: fmt.Sprintf("Доступные кейсы для покупки:\n\n%s\n\n🕒 **До обновления магазина**: %s", strings.Join(lines, "\n\n"), timeLeftStr),
		Color:       0x00BFFF,
		Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Вызвал: %s | Славь Императора! 👑", m.Author.Username)},
	}
	s.ChannelMessageSendEmbed(m.ChannelID, embed)
}

// HandleBuyCaseBankCommand !buy_case_bank <caseID> <count>
func (r *Ranking) HandleBuyCaseBankCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Fields(command)
	if len(parts) != 3 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Использование**: !buy_case_bank <caseID> <count>")
		return
	}
	caseID, countStr := parts[1], parts[2]
	count, err := strconv.Atoi(countStr)
	if err != nil || count <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ **Некорректное количество.**")
		return
	}

	// Проверка кейса
	kase, ok := r.Kki.cases[caseID]
	if !ok {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ **Кейс с ID %s не найден.**", caseID))
		return
	}

	// Проверка банка
	r.refreshCaseBank()
	if r.caseBank.Cases[caseID] < count {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ **В банке недостаточно кейсов (%s). Остаток: %d.**", kase.Name, r.caseBank.Cases[caseID]))
		return
	}

	// Проверка лимита покупок
	key := fmt.Sprintf("case_buy_limit:%s:%s", m.Author.ID, time.Now().Format("2006-01-02"))
	bought, _ := r.redis.Get(r.ctx, key).Int()
	if bought+count > 5 {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ **Достигнут дневной лимит покупок (5 кейсов). Куплено сегодня: %d.**", bought))
		return
	}

	// Проверка кредитов
	price := kase.Price * count
	buyerCoins := r.GetRating(m.Author.ID)
	if buyerCoins < price {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ **Недостаточно кредитов. Нужно: %d, у вас: %d.**", price, buyerCoins))
		return
	}

	// Обновление банка
	r.mu.Lock()
	r.caseBank.Cases[caseID] -= count
	if r.caseBank.Cases[caseID] == 0 {
		delete(r.caseBank.Cases, caseID)
	}
	jsonData, _ := json.Marshal(r.caseBank)
	r.redis.Set(r.ctx, "case_bank", jsonData, 0)
	r.mu.Unlock()

	// Обновление инвентаря
	buyerInv := r.Kki.GetUserCaseInventory(r, m.Author.ID)
	buyerInv[caseID] += count
	err = r.Kki.SaveUserCaseInventory(r, m.Author.ID, buyerInv)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ **Ошибка сохранения инвентаря. Попробуйте снова.**")
		log.Printf("Failed to save case inventory for user %s: %v", m.Author.ID, err)
		return
	}

	// Обновление кредитов
	r.UpdateRating(m.Author.ID, -price)
	r.redis.IncrBy(r.ctx, key, int64(count))
	r.redis.Expire(r.ctx, key, 24*time.Hour)

	// Лог операции
	r.LogCreditOperation(s, fmt.Sprintf("🛒 **%s** купил %d x 📦 **%s** (ID: %s) из банка за 💰 %d кредитов.", m.Author.Username, count, kase.Name, caseID, price))

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("✅ **Куплено** %d x 📦 **%s** (ID: %s) за 💰 %d кредитов!", count, kase.Name, caseID, price))
}

// HandleResetCaseLimitsCommand !a_reset_case_limits
func (r *Ranking) HandleResetCaseLimitsCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ **Только админы могут использовать эту команду!**")
		return
	}

	totalDeleted := 0

	// Сброс лимитов на открытие кейсов
	keys, err := r.redis.Keys(r.ctx, "case_limit:*").Result()
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ **Ошибка получения ключей case_limit из Redis.**")
		log.Printf("Failed to get case_limit keys: %v", err)
		return
	}
	for _, key := range keys {
		r.redis.Del(r.ctx, key)
		log.Printf("Deleted case limit key: %s", key)
		totalDeleted++
	}

	// Сброс лимитов на покупку кейсов
	keys, err = r.redis.Keys(r.ctx, "case_buy_limit:*").Result()
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ **Ошибка получения ключей case_buy_limit из Redis.**")
		log.Printf("Failed to get case_buy_limit keys: %v", err)
		return
	}
	for _, key := range keys {
		r.redis.Del(r.ctx, key)
		log.Printf("Deleted case buy limit key: %s", key)
		totalDeleted++
	}

	// Сброс лимитов на ежедневный кейс
	keys, err = r.redis.Keys(r.ctx, "daily_case:*").Result()
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ **Ошибка получения ключей daily_case из Redis.**")
		log.Printf("Failed to get daily_case keys: %v", err)
		return
	}
	for _, key := range keys {
		r.redis.Del(r.ctx, key)
		log.Printf("Deleted daily case key: %s", key)
		totalDeleted++
	}

	if totalDeleted == 0 {
		s.ChannelMessageSend(m.ChannelID, "ℹ️ **Лимиты не найдены для сброса. Возможно, они уже были сброшены автоматически в 4:00 по Красноярску.**")
		log.Printf("No limits found to reset for command !a_reset_case_limits")
		return
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("✅ **Сброшено %d лимитов (открытие, покупка, ежедневный кейс) для всех пользователей!**", totalDeleted))
	log.Printf("Reset %d limits for all users", totalDeleted)
}

// startDailyReset запускает горутину для сброса лимитов в 4:00 по Красноярску
func (r *Ranking) startDailyReset() {
	// Загружаем часовой пояс Красноярска
	loc, err := time.LoadLocation("Asia/Krasnoyarsk")
	if err != nil {
		log.Printf("Ошибка загрузки часового пояса Asia/Krasnoyarsk: %v", err)
		return
	}

	for {
		// Вычисляем время до следующего сброса (4:00 следующего дня)
		now := time.Now().In(loc)
		nextReset := time.Date(now.Year(), now.Month(), now.Day(), 4, 0, 0, 0, loc)
		if now.After(nextReset) || now.Equal(nextReset) {
			nextReset = nextReset.Add(24 * time.Hour)
		}
		timeUntilReset := nextReset.Sub(now)

		// Ожидаем до следующего сброса или сигнала остановки
		select {
		case <-time.After(timeUntilReset):
			// Выполняем сброс всех лимитов
			r.resetAllLimits()
			log.Printf("Автоматический сброс лимитов выполнен в %s", time.Now().In(loc).Format(time.RFC3339))
		case <-r.stopResetChan:
			log.Printf("Горутина сброса лимитов остановлена")
			return
		}
	}
}

// resetAllLimits сбрасывает все лимиты (открытие, покупка, ежедневный кейс)
func (r *Ranking) resetAllLimits() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Сброс лимитов на открытие кейсов
	keys, err := r.redis.Keys(r.ctx, "case_limit:*").Result()
	if err != nil {
		log.Printf("Ошибка получения ключей case_limit: %v", err)
		return
	}
	for _, key := range keys {
		r.redis.Del(r.ctx, key)
		log.Printf("Автоматически удален ключ case_limit: %s", key)
	}

	// Сброс лимитов на покупку кейсов
	keys, err = r.redis.Keys(r.ctx, "case_buy_limit:*").Result()
	if err != nil {
		log.Printf("Ошибка получения ключей case_buy_limit: %v", err)
		return
	}
	for _, key := range keys {
		r.redis.Del(r.ctx, key)
		log.Printf("Автоматически удален ключ case_buy_limit: %s", key)
	}

	// Сброс лимитов на ежедневный кейс
	keys, err = r.redis.Keys(r.ctx, "daily_case:*").Result()
	if err != nil {
		log.Printf("Ошибка получения ключей daily_case: %v", err)
		return
	}
	for _, key := range keys {
		r.redis.Del(r.ctx, key)
		log.Printf("Автоматически удален ключ daily_case: %s", key)
	}
}

// Stop прекращает работу горутины сброса лимитов
func (r *Ranking) Stop() {
	close(r.stopResetChan)
}

// GetBitcoinPrice получает текущий курс биткойна
func (r *Ranking) GetBitcoinPrice() (float64, error) {
	cacheKey := "bitcoin_price"

	// Пробуем получить из кэша
	cached, err := r.redis.Get(r.ctx, cacheKey).Result()
	if err == nil {
		cachedPrice, _ := strconv.ParseFloat(cached, 64)
		// Если кэш свежий (менее 10 минут), используем его
		if time.Since(r.BitcoinTracker.LastUpdate) < 10*time.Minute {
			return cachedPrice, nil
		}
	}

	// Получаем свежий курс
	resp, err := http.Get("https://api.coingecko.com/api/v3/simple/price?ids=bitcoin&vs_currencies=usd")
	if err != nil {
		log.Printf("Ошибка запроса к CoinGecko: %v", err)

		// Fallback: используем последнее известное значение
		if r.BitcoinTracker.CurrentPrice > 0 {
			return r.BitcoinTracker.CurrentPrice, nil
		}
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("CoinGecko API вернул статус: %d", resp.StatusCode)
		if r.BitcoinTracker.CurrentPrice > 0 {
			return r.BitcoinTracker.CurrentPrice, nil
		}
		return 0, fmt.Errorf("API вернул статус %d", resp.StatusCode)
	}

	var data map[string]map[string]float64
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Printf("Ошибка парсинга ответа CoinGecko: %v", err)
		if r.BitcoinTracker.CurrentPrice > 0 {
			return r.BitcoinTracker.CurrentPrice, nil
		}
		return 0, err
	}

	price := data["bitcoin"]["usd"]

	// Обновляем трекер
	r.BitcoinTracker.mu.Lock()
	r.BitcoinTracker.PreviousPrice = r.BitcoinTracker.CurrentPrice
	r.BitcoinTracker.CurrentPrice = price
	r.BitcoinTracker.LastUpdate = time.Now()

	// Сохраняем в историю (последние 24 часа)
	r.BitcoinTracker.PriceHistory = append(r.BitcoinTracker.PriceHistory, price)
	if len(r.BitcoinTracker.PriceHistory) > 288 { // 288 записей = 24 часа (каждые 5 мин)
		r.BitcoinTracker.PriceHistory = r.BitcoinTracker.PriceHistory[1:]
	}
	r.BitcoinTracker.mu.Unlock()

	// Сохраняем в Redis на 10 минут
	r.redis.Set(r.ctx, cacheKey, fmt.Sprintf("%.2f", price), 10*time.Minute)

	return price, nil
}

// Get24hAverage возвращает среднюю цену BTC за 24 часа
func (bt *BitcoinTracker) Get24hAverage() float64 {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	if len(bt.PriceHistory) == 0 {
		return bt.CurrentPrice
	}

	sum := 0.0
	for _, price := range bt.PriceHistory {
		sum += price
	}
	return sum / float64(len(bt.PriceHistory))
}

// CalculateVolatility вычисляет волатильность BTC
func (bt *BitcoinTracker) CalculateVolatility() float64 {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	if len(bt.PriceHistory) < 2 {
		return 0.2 // Базовая волатильность 20% если данных мало
	}

	// Берем последние 12 значений (1 час при обновлении каждые 5 минут)
	recentPrices := bt.PriceHistory
	if len(recentPrices) > 12 {
		recentPrices = recentPrices[len(recentPrices)-12:]
	}

	// Вычисляем стандартное отклонение для более точной волатильности
	sum := 0.0
	for _, price := range recentPrices {
		sum += price
	}
	mean := sum / float64(len(recentPrices))

	variance := 0.0
	for _, price := range recentPrices {
		variance += math.Pow(price-mean, 2)
	}
	variance /= float64(len(recentPrices))
	stdDev := math.Sqrt(variance)

	// Волатильность как коэффициент вариации
	volatility := stdDev / mean

	// Увеличиваем воспринимаемую волатильность в 2 раза
	return math.Min(1.0, volatility*2.0)
}

// CalculateNFTPrice вычисляет текущую цену NFT
func (r *Ranking) CalculateNFTPrice(nft NFT) int {
	// Защита от нулевой базовой цены
	if nft.BasePriceUSD == 0 {
		log.Printf("WARNING: Zero base price for NFT %s (Rarity: %s)", nft.Name, nft.Rarity)
		// Используем базовую цену из мапы как fallback
		basePrice, exists := BaseRarityPrices[nft.Rarity]
		if !exists {
			basePrice = 10 // Fallback значение
		}
		nft.BasePriceUSD = basePrice
	}

	basePrice := nft.BasePriceUSD
	rarityVolatility := RarityVolatility[nft.Rarity]

	// Для Common - меньшая волатильность
	if nft.Rarity == "Common" {
		// Common все еще более стабильны, но не полностью фиксированы
		btcVolatility := r.BitcoinTracker.CalculateVolatility()
		currentBtcPrice := r.BitcoinTracker.CurrentPrice
		averageBtcPrice := r.BitcoinTracker.Get24hAverage()

		btcDeviation := (currentBtcPrice - averageBtcPrice) / averageBtcPrice
		// Меньшее влияние на Common
		impactStrength := btcVolatility * rarityVolatility * 0.3

		volatilityMultiplier := 1.0 + (btcDeviation * impactStrength)
		// Ограничиваем разброс для Common
		volatilityMultiplier = math.Max(0.8, math.Min(1.2, volatilityMultiplier))

		finalPrice := basePrice * volatilityMultiplier
		return int(finalPrice)
	}

	// Для Rare и выше - полная волатильность
	btcVolatility := r.BitcoinTracker.CalculateVolatility()
	currentBtcPrice := r.BitcoinTracker.CurrentPrice
	averageBtcPrice := r.BitcoinTracker.Get24hAverage()

	// Отклонение BTC от среднего
	btcDeviation := (currentBtcPrice - averageBtcPrice) / averageBtcPrice

	// Сила воздействия = волатильность BTC * множитель редкости
	// Увеличиваем влияние в 3 раза для больших колебаний
	impactStrength := btcVolatility * rarityVolatility * 30.0

	// Применяем воздействие
	volatilityMultiplier := 1.0 + (btcDeviation * impactStrength)

	// Увеличиваем максимальный разброс для редких NFT
	var minMultiplier, maxMultiplier float64

	switch nft.Rarity {
	case "Rare":
		minMultiplier, maxMultiplier = 0.7, 1.5
	case "Super-rare":
		minMultiplier, maxMultiplier = 0.6, 2.0
	case "Epic":
		minMultiplier, maxMultiplier = 0.5, 30.0
	case "Nephrite":
		minMultiplier, maxMultiplier = 0.4, 40.0
	case "Exotic":
		minMultiplier, maxMultiplier = 0.3, 50.0
	case "Legendary":
		minMultiplier, maxMultiplier = 0.2, 60.0 // Легендарки могут упасть до 20% или вырасти в 6 раз
	default:
		minMultiplier, maxMultiplier = 0.1, 100.0
	}

	// Ограничиваем разброс
	volatilityMultiplier = math.Max(minMultiplier, math.Min(maxMultiplier, volatilityMultiplier))

	finalPrice := basePrice * volatilityMultiplier

	log.Printf("Цена %s: база $%.0f, множитель %.2f, итого $%.0f (BTC отклонение: %.1f%%)",
		nft.Rarity, basePrice, volatilityMultiplier, finalPrice, btcDeviation*100)

	return int(finalPrice)
}

// min возвращает минимальное из двух чисел
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// StartBitcoinUpdater запускает обновление курса BTC каждые 5 минут
func (r *Ranking) StartBitcoinUpdater() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				price, err := r.GetBitcoinPrice()
				if err != nil {
					log.Printf("Ошибка обновления курса BTC: %v", err)
					continue
				}
				log.Printf("✅ Курс BTC обновлен: $%.2f", price)
			case <-r.stopResetChan:
				return
			}
		}
	}()
}

// getBitcoinPriceFromAlternative получает курс с альтернативного API
func (r *Ranking) getBitcoinPriceFromAlternative() (float64, error) {
	// Попробуем Binance API
	resp, err := http.Get("https://api.binance.com/api/v3/ticker/price?symbol=BTCUSDT")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var binanceData struct {
		Symbol string `json:"symbol"`
		Price  string `json:"price"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&binanceData); err != nil {
		return 0, err
	}

	price, err := strconv.ParseFloat(binanceData.Price, 64)
	if err != nil {
		return 0, err
	}

	return price, nil
}
