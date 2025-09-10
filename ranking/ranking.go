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
		s.ChannelMessageSend(m.ChannelID, "Ваш инвентарь пуст.")
		return
	}

	var lines []string
	for nftID, count := range inv {
		nft, ok := r.Kki.nfts[nftID]
		if !ok {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s (%s, x%d) - Цена: %d", nft.Name, nft.Rarity, count, nft.Price))
	}
	sort.Strings(lines)
	embed := &discordgo.MessageEmbed{
		Title:       "Ваш инвентарь",
		Description: strings.Join(lines, "\n"),
		Color:       0x00FF00,
	}
	s.ChannelMessageSendEmbed(m.ChannelID, embed)
}

// HandleSellCommand !sell <nftID> <count>
func (r *Ranking) HandleSellCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Split(command, " ")
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "Использование: !sell <nftID> <count>")
		return
	}
	nftID := parts[1]
	count, _ := strconv.Atoi(parts[2])

	inv := r.GetUserInventory(m.Author.ID)
	if inv[nftID] < count {
		s.ChannelMessageSend(m.ChannelID, "Недостаточно NFT.")
		return
	}
	nft, ok := r.Kki.nfts[nftID]
	if !ok {
		s.ChannelMessageSend(m.ChannelID, "Некорректный NFT.")
		return
	}

	// Продажа в банк за полцены
	sellPrice := (nft.Price / 2) * count
	r.LogCreditOperation(s, fmt.Sprintf("%s продал %d x %s за %d кредитов", m.Author.Username, count, nft.Name, sellPrice))

	inv[nftID] -= count
	if inv[nftID] == 0 {
		delete(inv, nftID)
	}
	r.SaveUserInventory(m.Author.ID, inv)
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Продано %d x %s за %d кредитов (цена банка).", count, nft.Name, sellPrice))
}

// HandleTransferNFTCommand !transfer_nft <userMention> <nftID> <count>
func (r *Ranking) HandleTransferNFTCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Split(command, " ")
	if len(parts) < 4 {
		s.ChannelMessageSend(m.ChannelID, "Использование: !transfer_nft @user <nftID> <count>")
		return
	}
	targetID := strings.Trim(parts[1], "<@!>")
	nftID := parts[2]
	count, _ := strconv.Atoi(parts[3])

	inv := r.GetUserInventory(m.Author.ID)
	if inv[nftID] < count {
		s.ChannelMessageSend(m.ChannelID, "Недостаточно NFT.")
		return
	}

	targetInv := r.GetUserInventory(targetID)
	targetInv[nftID] += count
	r.SaveUserInventory(targetID, targetInv)

	inv[nftID] -= count
	if inv[nftID] == 0 {
		delete(inv, nftID)
	}
	r.SaveUserInventory(m.Author.ID, inv)

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Передано %d x %s пользователю <@%s>.", count, nftID, targetID))
}

// HandleOpenCaseCommand !open_case <caseID>
func (r *Ranking) HandleOpenCaseCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Split(command, " ")
	if len(parts) < 2 {
		s.ChannelMessageSend(m.ChannelID, "Использование: !open_case <caseID>")
		return
	}
	caseID := parts[1]
	kase, ok := r.Kki.cases[caseID]
	if !ok {
		s.ChannelMessageSend(m.ChannelID, "Некорректный кейс.")
		return
	}

	// Проверка инвентаря кейсов
	userCaseInv := r.Kki.GetUserCaseInventory(r, m.Author.ID)
	if userCaseInv[caseID] < 1 {
		s.ChannelMessageSend(m.ChannelID, "У вас нет этого кейса.")
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
		s.ChannelMessageSend(m.ChannelID, "Достигнут дневной лимит (5 кейсов в день).")
		return
	}
	r.redis.Incr(r.ctx, key)
	r.redis.Expire(r.ctx, key, 24*time.Hour)

	// Начало анимации
	animMsg, _ := s.ChannelMessageSend(m.ChannelID, "Открываем кейс...")
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
		s.ChannelMessageEdit(m.ChannelID, animMsg.ID, "В кейсе нет NFT.")
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
				Description: "Крутим... " + randRarity,
				Color:       RarityColors[randRarity],
			}
			s.ChannelMessageEditEmbed(m.ChannelID, animMsg.ID, embed)
			time.Sleep(300 * time.Millisecond)
		}

		// Показ выпавших NFT
		var lines []string
		inv := r.GetUserInventory(m.Author.ID)
		for _, nft := range dropped {
			inv[nft.ID]++
			embed := &discordgo.MessageEmbed{
				Title:       "Выпало: " + nft.Name,
				Description: fmt.Sprintf("Редкость: %s\nОписание: %s\nДата выпуска: %s\nЦена: %d\nКоллекция: %s", nft.Rarity, nft.Description, nft.ReleaseDate, nft.Price, nft.Collection),
				Color:       RarityColors[nft.Rarity],
				Image:       &discordgo.MessageEmbedImage{URL: nft.ImageURL},
			}
			s.ChannelMessageSendEmbed(m.ChannelID, embed)
			lines = append(lines, nft.Name)
			time.Sleep(1 * time.Second)
		}
		r.SaveUserInventory(m.Author.ID, inv)
		s.ChannelMessageSend(m.ChannelID, "Вы получили: "+strings.Join(lines, ", "))
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
	parts := strings.Split(command, " ")
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "Использование: !admin_give_case <userID> <caseID>")
		return
	}
	userID := parts[1]
	caseID := parts[2]

	inv := r.Kki.GetUserCaseInventory(r, userID)
	inv[caseID]++
	r.Kki.SaveUserCaseInventory(r, userID, inv)

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Выдан кейс %s пользователю <@%s>", caseID, userID))
}

// HandleAdminGiveNFT !admin_give_nft <userID> <nftID> <count>
func (r *Ranking) HandleAdminGiveNFT(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Split(command, " ")
	if len(parts) < 4 {
		s.ChannelMessageSend(m.ChannelID, "Использование: !admin_give_nft <userID> <nftID> <count>")
		return
	}
	userID := parts[1]
	nftID := parts[2]
	count, _ := strconv.Atoi(parts[3])

	inv := r.GetUserInventory(userID)
	inv[nftID] += count
	r.SaveUserInventory(userID, inv)

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Выдано %d x %s пользователю <@%s>", count, nftID, userID))
}

// HandleAdminRemoveNFT !admin_remove_nft <userID> <nftID> <count>
func (r *Ranking) HandleAdminRemoveNFT(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Split(command, " ")
	if len(parts) < 4 {
		s.ChannelMessageSend(m.ChannelID, "Использование: !admin_remove_nft <userID> <nftID> <count>")
		return
	}
	userID := parts[1]
	nftID := parts[2]
	count, _ := strconv.Atoi(parts[3])

	inv := r.GetUserInventory(userID)
	if inv[nftID] < count {
		s.ChannelMessageSend(m.ChannelID, "Недостаточно NFT.")
		return
	}
	inv[nftID] -= count
	if inv[nftID] == 0 {
		delete(inv, nftID)
	}
	r.SaveUserInventory(userID, inv)

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Удалено %d x %s у <@%s>", count, nftID, userID))
}

// HandleAdminHolidayCase !admin_holiday_case <userID> <count>
func (r *Ranking) HandleAdminHolidayCase(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Split(command, " ")
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "Использование: !admin_holiday_case <userID> <count>")
		return
	}
	userID := parts[1]
	count, _ := strconv.Atoi(parts[2])

	inv := r.Kki.GetUserCaseInventory(r, userID)
	inv["holiday_case"] += count
	r.Kki.SaveUserCaseInventory(r, userID, inv)

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Выдано %d праздничных кейсов пользователю <@%s>", count, userID))
}

// HandleShowNFTCommand !show_nft <nftID>
func (r *Ranking) HandleShowNFTCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Split(command, " ")
	if len(parts) < 2 {
		s.ChannelMessageSend(m.ChannelID, "Использование: !show_nft <nftID>")
		return
	}
	nftID := parts[1]
	nft, ok := r.Kki.nfts[nftID]
	if !ok {
		s.ChannelMessageSend(m.ChannelID, "Некорректный NFT.")
		return
	}
	embed := &discordgo.MessageEmbed{
		Title:       nft.Name,
		Description: fmt.Sprintf("Редкость: %s\nОписание: %s\nДата выпуска: %s\nЦена: %d\nКоллекция: %s", nft.Rarity, nft.Description, nft.ReleaseDate, nft.Price, nft.Collection),
		Color:       RarityColors[nft.Rarity],
		Image:       &discordgo.MessageEmbedImage{URL: nft.ImageURL},
	}
	s.ChannelMessageSendEmbed(r.floodChannelID, embed)
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
