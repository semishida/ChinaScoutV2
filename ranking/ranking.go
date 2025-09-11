package ranking

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
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
		s.ChannelMessageSend(m.ChannelID, "🎒 **Ваш инвентарь пуст.**")
		return
	}

	var lines []string
	for nftID, count := range inv {
		nft, ok := r.Kki.nfts[nftID]
		if !ok {
			continue
		}
		lines = append(lines, fmt.Sprintf("🃏 **%s** (ID для передачи и продажи: %s, %s, x%d) - 💰 %d", nft.Name, nftID, nft.Rarity, count, nft.Price))
	}
	sort.Strings(lines)
	embed := &discordgo.MessageEmbed{
		Title:       "🎒 **Ваш инвентарь**",
		Description: strings.Join(lines, "\n"),
		Color:       0x00FF00,
		Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Владелец: %s", m.Author.Username)},
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
		Title:       "🃏 **Подтверждение продажи**",
		Description: fmt.Sprintf("Вы хотите продать %d x **%s** (ID для передачи и продажи: %s) за 💰 %d кредитов?", count, nft.Name, nftID, sellPrice),
		Color:       RarityColors[nft.Rarity],
		Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Владелец: %s", m.Author.Username)},
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
	s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Embed:      embed,
		Components: components,
	})
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
	currentCoins, _ := r.redis.Get(r.ctx, "coins:"+userID).Int()
	r.redis.Set(r.ctx, "coins:"+userID, currentCoins+sellPrice, 0)

	// Отправка лога
	nft := r.Kki.nfts[nftID]
	r.LogCreditOperation(s, fmt.Sprintf("🃏 **%s** продал %d x **%s** (ID: %s) за 💰 %d кредитов.", i.Member.User.Username, count, nft.Name, nftID, sellPrice))

	// Ответ пользователю
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("✅ **Продано** %d x 🃏 **%s** (ID: %s) за 💰 %d кредитов!", count, nft.Name, nftID, sellPrice),
		},
	})
}

// HandleSellCancel обрабатывает отмену продажи
func (r *Ranking) HandleSellCancel(s *discordgo.Session, i *discordgo.InteractionCreate) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: "❌ **Продажа отменена.**"},
	})
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

	kase, ok := r.Kki.cases[caseID]
	if !ok {
		s.ChannelMessageSend(m.ChannelID, "❌ **Некорректный кейс. Проверьте ID.**")
		return
	}

	sellerInv := r.Kki.GetUserCaseInventory(r, sellerID)
	if sellerInv[caseID] < count {
		s.ChannelMessageSend(m.ChannelID, "❌ **У продавца недостаточно кейсов.**")
		return
	}

	price := kase.Price * count
	buyerCoins, _ := r.redis.Get(r.ctx, "coins:"+m.Author.ID).Int()
	if buyerCoins < price {
		s.ChannelMessageSend(m.ChannelID, "❌ **Недостаточно кредитов.**")
		return
	}

	// Обновление кредитов
	r.redis.Set(r.ctx, "coins:"+m.Author.ID, buyerCoins-price, 0)
	sellerCoins, _ := r.redis.Get(r.ctx, "coins:"+sellerID).Int()
	r.redis.Set(r.ctx, "coins:"+sellerID, sellerCoins+price, 0)

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
			if nft.Collection == col && (caseID != "daily" || nft.Collection != "holiday") {
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
		rarities := []string{"Common", "Rare", "Super-rare", "Epic", "Nephrite", "Exotic", "LEGENDARY"}
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
				Title:       fmt.Sprintf("🎉 **Выпало**: %s (ID для передачи и продажи: %s)", nft.Name, nft.ID),
				Description: fmt.Sprintf("**Редкость**: %s\n**Описание**: %s\n**Дата выпуска**: %s\n**Цена**: 💰 %d\n**Коллекция**: %s%s", nft.Rarity, nft.Description, nft.ReleaseDate, nft.Price, nft.Collection, newTag),
				Color:       RarityColors[nft.Rarity],
				Image:       &discordgo.MessageEmbedImage{URL: nft.ImageURL},
				Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Владелец: %s", m.Author.Username)},
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
			lines = append(lines, fmt.Sprintf("%s (ID: %s)", nft.Name, nft.ID))
			time.Sleep(1 * time.Second)
		}
		r.SaveUserInventory(m.Author.ID, inv)
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🎉 **Вы получили**: %s", strings.Join(lines, ", ")))
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
		s.ChannelMessageSend(m.ChannelID, "Ежедневный кейс уже получен.")
		return
	}

	userCaseInv := r.Kki.GetUserCaseInventory(r, m.Author.ID)
	userCaseInv["daily"]++
	r.Kki.SaveUserCaseInventory(r, m.Author.ID, userCaseInv)
	r.redis.Set(r.ctx, key, "claimed", 24*time.Hour)

	s.ChannelMessageSend(m.ChannelID, "Вы получили ежедневный кейс! Используйте !open_case daily для открытия.")
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
		Title:       fmt.Sprintf("🃏 **%s** (ID для передачи и продажи: %s)", nft.Name, nftID),
		Description: fmt.Sprintf("**Описание**: %s\n**Редкость**: %s\n**Дата выпуска**: %s\n**Цена**: 💰 %d\n**Коллекция**: %s", nft.Description, nft.Rarity, nft.ReleaseDate, nft.Price, nft.Collection),
		Color:       RarityColors[nft.Rarity],
		Image:       &discordgo.MessageEmbedImage{URL: nft.ImageURL},
		Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Похвастался: %s", m.Author.Username)},
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
	s.ChannelMessageSend(m.ChannelID, "Все NFT, кейсы и лимиты пользователей очищены.")
}

func (r *Ranking) HandleCaseInventoryCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	inv := r.Kki.GetUserCaseInventory(r, m.Author.ID)
	if len(inv) == 0 {
		s.ChannelMessageSend(m.ChannelID, "📦 **Ваш инвентарь кейсов пуст.**")
		return
	}

	var lines []string
	for caseID, count := range inv {
		kase, ok := r.Kki.cases[caseID]
		if !ok {
			continue
		}
		lines = append(lines, fmt.Sprintf("📦 **%s** (ID для открытия/передачи: %s, x%d) - 💰 %d", kase.Name, caseID, count, kase.Price))
	}
	sort.Strings(lines)

	// Проверка дневного лимита
	key := fmt.Sprintf("case_limit:%s:%s", m.Author.ID, time.Now().Format("2006-01-02"))
	opened, _ := r.redis.Get(r.ctx, key).Int()
	limitMsg := fmt.Sprintf("🔄 **Лимит открытия кейсов сегодня**: %d/5", opened)

	embed := &discordgo.MessageEmbed{
		Title:       "📦 **Ваш инвентарь кейсов**",
		Description: strings.Join(lines, "\n") + "\n\n" + limitMsg,
		Color:       0x00BFFF,
		Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Владелец: %s", m.Author.Username)},
	}
	s.ChannelMessageSendEmbed(m.ChannelID, embed)
}
