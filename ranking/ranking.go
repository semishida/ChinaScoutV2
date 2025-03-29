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
)

type User struct {
	ID     string `json:"id"`
	Rating int    `json:"rating"`
}

type Poll struct {
	ID       string         // Уникальный 5-символьный ID опроса
	Question string         // Вопрос опроса
	Options  []string       // Варианты ответа
	Bets     map[string]int // Ставки: userID -> сумма ставки
	Choices  map[string]int // Выбор: userID -> номер варианта (1, 2, ...)
	Active   bool           // Активен ли опрос
	Creator  string         // ID админа, создавшего опрос
	Created  time.Time      // Время создания
}

type RedBlackGame struct {
	GameID        string // Уникальный ID игры
	PlayerID      string
	Bet           int
	Choice        string
	Active        bool
	MenuMessageID string // ID начального сообщения
	Color         int    // Случайный цвет для embed
}

type Card struct {
	Suit  string // Масть (♠, ♥, ♦, ♣)
	Value string // Значение (2-10, J, Q, K, A)
}

type BlackjackGame struct {
	GameID        string // Уникальный ID игры
	PlayerID      string
	Bet           int
	PlayerCards   []Card
	DealerCards   []Card
	Active        bool
	LastActivity  time.Time
	MenuMessageID string // ID начального сообщения (и единственного, которое редактируем)
	Color         int    // Случайный цвет для embed
	ChannelID     string // Канал игры
}

type Ranking struct {
	mu             sync.Mutex
	admins         map[string]bool
	polls          map[string]*Poll
	redis          *redis.Client
	ctx            context.Context
	voiceAct       map[string]int
	redBlackGames  map[string]*RedBlackGame
	blackjackGames map[string]*BlackjackGame
	floodChannelID string // Для уведомлений
}

func NewRanking(adminFilePath, redisAddr, floodChannelID string) (*Ranking, error) {
	r := &Ranking{
		admins:         make(map[string]bool),
		polls:          make(map[string]*Poll),
		voiceAct:       make(map[string]int),
		redBlackGames:  make(map[string]*RedBlackGame),
		blackjackGames: make(map[string]*BlackjackGame),
		ctx:            context.Background(),
		floodChannelID: floodChannelID,
	}

	// Пытаемся подключиться к Redis с повторными попытками
	var err error
	for i := 0; i < 5; i++ {
		r.redis = redis.NewClient(&redis.Options{
			Addr: redisAddr,
		})
		_, err = r.redis.Ping(r.ctx).Result()
		if err == nil {
			break
		}
		log.Printf("Failed to connect to Redis (attempt %d/5): %v", i+1, err)
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Redis after 5 attempts: %v", err)
	}

	file, err := os.Open(adminFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open admin file: %v", err)
	}
	defer file.Close()

	var admins struct {
		IDs []string `json:"admin_ids"`
	}
	if err := json.NewDecoder(file).Decode(&admins); err != nil {
		return nil, fmt.Errorf("failed to parse admin file: %v", err)
	}
	for _, id := range admins.IDs {
		r.admins[id] = true
	}

	log.Printf("Initialized ranking with %d admins", len(r.admins))
	return r, nil
}

func (r *Ranking) GetRating(userID string) int {
	for i := 0; i < 3; i++ {
		data, err := r.redis.Get(r.ctx, "user:"+userID).Result()
		if err == redis.Nil {
			return 0
		}
		if err != nil {
			log.Printf("Failed to get rating for %s from Redis (attempt %d/3): %v", userID, i+1, err)
			time.Sleep(1 * time.Second)
			continue
		}
		var user User
		if err := json.Unmarshal([]byte(data), &user); err != nil {
			log.Printf("Failed to unmarshal user %s: %v", userID, err)
			return 0
		}
		return user.Rating
	}
	log.Printf("Failed to get rating for %s after 3 attempts", userID)
	return 0
}

func (r *Ranking) UpdateRating(userID string, points int) {
	// Получаем текущий рейтинг
	user := User{ID: userID, Rating: 0}
	for i := 0; i < 3; i++ {
		data, err := r.redis.Get(r.ctx, "user:"+userID).Result()
		if err == nil {
			if err := json.Unmarshal([]byte(data), &user); err != nil {
				log.Printf("Failed to unmarshal user %s: %v", userID, err)
				return
			}
			break
		} else if err == redis.Nil {
			break
		} else {
			log.Printf("Failed to get user %s from Redis (attempt %d/3): %v", userID, i+1, err)
			time.Sleep(1 * time.Second)
		}
	}

	user.Rating += points
	if user.Rating < 0 {
		user.Rating = 0
	}

	// Сохраняем обновлённый рейтинг
	dataBytes, err := json.Marshal(user)
	if err != nil {
		log.Printf("Failed to marshal user %s: %v", userID, err)
		return
	}

	for i := 0; i < 3; i++ {
		if err := r.redis.Set(r.ctx, "user:"+userID, dataBytes, 0).Err(); err != nil {
			log.Printf("Failed to save user %s to Redis (attempt %d/3): %v", userID, i+1, err)
			time.Sleep(1 * time.Second)
			continue
		}
		log.Printf("Updated rating for %s: %d (change: %d)", userID, user.Rating, points)
		return
	}
	log.Printf("Failed to save user %s to Redis after 3 attempts", userID)
	// Уведомляем в floodChannelID
	if r.floodChannelID != "" {
		s, err := discordgo.New("Bot " + os.Getenv("DISCORD_TOKEN"))
		if err == nil {
			s.ChannelMessageSend(r.floodChannelID, "❌ Ошибка: Не удалось сохранить рейтинг в Redis после 3 попыток! Проверьте Redis-сервер.")
		}
	}
}

func (r *Ranking) GetTop5() []User {
	keys, err := r.redis.Keys(r.ctx, "user:*").Result()
	if err != nil {
		log.Printf("Failed to get user keys from Redis: %v", err)
		return nil
	}

	users := make([]User, 0)
	for _, key := range keys {
		data, err := r.redis.Get(r.ctx, key).Result()
		if err != nil {
			log.Printf("Failed to load user %s from Redis: %v", key, err)
			continue
		}
		var user User
		if err := json.Unmarshal([]byte(data), &user); err != nil {
			log.Printf("Failed to unmarshal user %s: %v", key, err)
			continue
		}
		if user.Rating > 0 {
			users = append(users, user)
		}
	}

	sort.Slice(users, func(i, j int) bool {
		return users[i].Rating > users[j].Rating
	})

	if len(users) > 5 {
		return users[:5]
	}
	log.Printf("Top 5 users: %v", users)
	return users
}

func (r *Ranking) IsAdmin(userID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	isAdmin := r.admins[userID]
	log.Printf("Checking if %s is admin: %v", userID, isAdmin)
	return isAdmin
}

func generateGameID(playerID string) string {
	rand.Seed(time.Now().UnixNano())
	return fmt.Sprintf("%s_%d_%d", playerID, time.Now().UnixNano(), rand.Intn(10000))
}

func randomColor() int {
	// Случайный цвет в формате RGB (0xRRGGBB)
	return rand.Intn(0xFFFFFF)
}

func generatePollID() string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	rand.Seed(time.Now().UnixNano())
	id := make([]byte, 5)
	for i := range id {
		id[i] = letters[rand.Intn(len(letters))]
	}
	return string(id)
}

// GetCoefficients возвращает текущие коэффициенты для каждого варианта опроса
func (p *Poll) GetCoefficients() []float64 {
	totalBet := 0
	optionBets := make([]int, len(p.Options))

	// Считаем общую сумму ставок и сумму ставок на каждый вариант
	for _, bet := range p.Bets {
		totalBet += bet
	}
	for userID, choice := range p.Choices {
		optionBets[choice-1] += p.Bets[userID]
	}

	// Вычисляем коэффициенты
	coefficients := make([]float64, len(p.Options))
	for i := range p.Options {
		if optionBets[i] == 0 {
			coefficients[i] = 0
		} else {
			coefficients[i] = float64(totalBet) / float64(optionBets[i])
		}
	}
	return coefficients
}

func (r *Ranking) HandlePollCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !cpoll: %s from %s", command, m.Author.ID)

	parts := splitCommand(command)
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!cpoll Вопрос [Вариант1] [Вариант2] ...`")
		return
	}

	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только товарищи-админы могут создавать опросы! 🔒")
		return
	}

	var questionParts []string
	var options []string
	for _, part := range parts[1:] {
		if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
			trimmed := strings.Trim(part, "[]")
			if trimmed != "" {
				options = append(options, trimmed)
			}
		} else {
			questionParts = append(questionParts, part)
		}
	}
	question := strings.Join(questionParts, " ")
	if question == "" {
		s.ChannelMessageSend(m.ChannelID, "❌ Вопрос не может быть пустым! 📝")
		return
	}

	if len(options) < 2 {
		s.ChannelMessageSend(m.ChannelID, "❌ Нужно минимум 2 варианта ответа! 📊")
		return
	}

	pollID := generatePollID()
	r.mu.Lock()
	r.polls[pollID] = &Poll{
		ID:       pollID,
		Question: question,
		Options:  options,
		Bets:     make(map[string]int),
		Choices:  make(map[string]int),
		Active:   true,
		Creator:  m.Author.ID,
		Created:  time.Now(),
	}
	r.mu.Unlock()

	response := fmt.Sprintf("🎉 **Опрос %s запущен!**\n<@%s> создал опрос: **%s**\n\n📋 **Варианты:**\n", pollID, m.Author.ID, question)
	for i, opt := range options {
		response += fmt.Sprintf("%d. [%s]\n", i+1, opt)
	}
	response += fmt.Sprintf("\n💸 Ставьте: `!dep %s <номер_варианта> <сумма>`\n🔒 Закрытие: `!closedep %s <номер>`", pollID, pollID)
	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("Poll %s created by %s: %s with options %v", pollID, m.Author.ID, question, options)
}

func (r *Ranking) HandleDepCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !dep: %s from %s", command, m.Author.ID)

	parts := splitCommand(command)
	if len(parts) != 4 {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!dep <ID_опроса> <номер_варианта> <сумма>`")
		return
	}

	pollID := parts[1]
	option, err := strconv.Atoi(parts[2])
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Номер варианта должен быть числом! 🔢")
		return
	}

	amount, err := strconv.Atoi(parts[3])
	if err != nil || amount <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть положительным числом! 💸")
		return
	}

	r.mu.Lock()
	poll, exists := r.polls[pollID]
	if !exists || !poll.Active {
		s.ChannelMessageSend(m.ChannelID, "❌ Опрос не найден или уже закрыт! 🔒")
		r.mu.Unlock()
		return
	}

	if option < 1 || option > len(poll.Options) {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Номер варианта должен быть от 1 до %d! 📊", len(poll.Options)))
		r.mu.Unlock()
		return
	}

	userRating := r.GetRating(m.Author.ID)
	if userRating < amount {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Недостаточно кредитов! Твой баланс: %d 💰", userRating))
		r.mu.Unlock()
		return
	}

	// Обновляем ставку (суммируем, если уже есть)
	r.UpdateRating(m.Author.ID, -amount)
	if _, exists := poll.Bets[m.Author.ID]; exists {
		poll.Bets[m.Author.ID] += amount // Суммируем ставку
	} else {
		poll.Bets[m.Author.ID] = amount
	}
	poll.Choices[m.Author.ID] = option
	r.mu.Unlock()

	// Вычисляем текущий коэффициент
	coefficients := poll.GetCoefficients()
	coefficient := coefficients[option-1]

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🎲 <@%s> поставил %d кредитов на [%s] в опросе **%s** 📊\n**📈 Текущий коэффициент:** %.2f", m.Author.ID, amount, poll.Options[option-1], poll.Question, coefficient))
	log.Printf("User %s bet %d on option %d in poll %s, coefficient: %.2f", m.Author.ID, amount, option, pollID, coefficient)
}

func (r *Ranking) HandleCloseDepCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !closedep: %s from %s", command, m.Author.ID)

	parts := strings.Fields(command)
	if len(parts) != 3 {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!closedep <ID_опроса> <номер_победившего_варианта>`")
		return
	}

	pollID := parts[1]
	winningOptionStr := strings.Trim(parts[2], "<>[]")
	winningOption, err := strconv.Atoi(winningOptionStr)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Номер варианта должен быть числом! 🔢")
		return
	}

	r.mu.Lock()
	poll, exists := r.polls[pollID]
	if !exists {
		s.ChannelMessageSend(m.ChannelID, "❌ Опрос не найден! 📊")
		r.mu.Unlock()
		return
	}
	if !poll.Active {
		s.ChannelMessageSend(m.ChannelID, "❌ Опрос уже закрыт! 🔒")
		r.mu.Unlock()
		return
	}

	if m.Author.ID != poll.Creator {
		s.ChannelMessageSend(m.ChannelID, "❌ Только создатель опроса может его закрыть! 🔐")
		r.mu.Unlock()
		return
	}

	if winningOption < 1 || winningOption > len(poll.Options) {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Номер варианта должен быть от 1 до %d! 📊", len(poll.Options)))
		r.mu.Unlock()
		return
	}

	totalBet := 0
	winnersBet := 0
	for _, bet := range poll.Bets {
		totalBet += bet
	}
	for userID, choice := range poll.Choices {
		if choice == winningOption {
			winnersBet += poll.Bets[userID]
		}
	}

	var coefficient float64
	if winnersBet == 0 {
		coefficient = 0
	} else {
		coefficient = float64(totalBet) / float64(winnersBet)
	}

	response := fmt.Sprintf("✅ **Опрос %s завершён!** 🏆\nПобедил: **%s** (№%d)\n📈 **Коэффициент:** %.2f\n\n🎉 **Победители:**\n", pollID, poll.Options[winningOption-1], winningOption, coefficient)
	for userID, choice := range poll.Choices {
		if choice == winningOption {
			winnings := int(float64(poll.Bets[userID]) * coefficient)
			r.UpdateRating(userID, winnings+poll.Bets[userID])
			response += fmt.Sprintf("<@%s>: %d кредитов (ставка: %d) 💰\n", userID, winnings+poll.Bets[userID], poll.Bets[userID])
		}
	}
	if winnersBet == 0 {
		response += "Никто не победил! 😢"
	}

	poll.Active = false
	r.mu.Unlock()

	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("Poll %s closed by %s, winner: %s, coefficient: %.2f", pollID, m.Author.ID, poll.Options[winningOption-1], coefficient)
}

// !polls
func (r *Ranking) HandlePollsCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Processing !polls: %s from %s", m.Content, m.Author.ID)

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.polls) == 0 {
		s.ChannelMessageSend(m.ChannelID, "📊 Нет активных опросов! Создай новый с помощью `!cpoll`! 🎉")
		return
	}

	response := "📊 **Активные опросы:**\n"
	for pollID, poll := range r.polls {
		if !poll.Active {
			continue
		}
		response += fmt.Sprintf("\n**Опрос %s: %s** 🎉\n", pollID, poll.Question)
		coefficients := poll.GetCoefficients()
		for i, option := range poll.Options {
			response += fmt.Sprintf("📋 Вариант %d. [%s] (📈 Коэффициент: %.2f)\n", i+1, option, coefficients[i])
			// Показываем, кто поставил на этот вариант
			for userID, choice := range poll.Choices {
				if choice == i+1 {
					bet := poll.Bets[userID]
					potentialWin := int(float64(bet) * coefficients[i])
					response += fmt.Sprintf("  - <@%s>: %d кредитов (💰 Потенциальный выигрыш: %d)\n", userID, bet, potentialWin+bet)
				}
			}
		}
	}

	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("Polls list sent to %s", m.Author.ID)
}

// !rb
// !rb
func (r *Ranking) StartRBGame(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("StartRBGame called for user %s", m.Author.ID)

	r.mu.Lock()
	gameID := generateGameID(m.Author.ID)
	color := randomColor()
	game := &RedBlackGame{
		GameID:   gameID,
		PlayerID: m.Author.ID,
		Active:   true,
		Color:    color,
	}
	r.redBlackGames[gameID] = game
	r.mu.Unlock()

	embed := &discordgo.MessageEmbed{
		Title:       "🎰 Игра: Красный-Чёрный",
		Description: fmt.Sprintf("Велком, <@%s>! 🥳\nИмператор велит: выбирать цвет и ставка делай!\n\n**💰 Баланса твоя:** %d кредитов\n\nПиши вот: `!rb <red/black> <сумма>`\nНапример: `!rb red 50`\nИмператор следит за тобой! 👑", m.Author.ID, r.GetRating(m.Author.ID)),
		Color:       color,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Славь Императора и везёт тебе! 🍀",
		},
	}
	msg, err := s.ChannelMessageSendEmbed(m.ChannelID, embed)
	if err != nil {
		log.Printf("Failed to send RB menu: %v", err)
		return
	}

	r.mu.Lock()
	game.MenuMessageID = msg.ID
	r.mu.Unlock()

	// Запускаем таймер для отключения игры через 15 минут
	go func(messageID string, channelID string) {
		time.Sleep(15 * time.Minute)
		r.mu.Lock()
		if g, exists := r.redBlackGames[gameID]; exists && g.Active {
			g.Active = false
			delete(r.redBlackGames, gameID)
			embed := &discordgo.MessageEmbed{
				Title:       "🎰 Игра: Красный-Чёрный",
				Description: fmt.Sprintf("Игра закончи, <@%s>! Время нету. ⏰\nИмператор недоволен! 😡", m.Author.ID),
				Color:       color,
				Footer: &discordgo.MessageEmbedFooter{
					Text: "Время вышло! Император гневен! ⏰",
				},
			}
			_, err := s.ChannelMessageEditEmbed(channelID, messageID, embed)
			if err != nil {
				log.Printf("Failed to update RB message on timeout: %v", err)
			}
		}
		r.mu.Unlock()
	}(msg.ID, m.ChannelID)
}

// !rb <red/black> <amount>
func (r *Ranking) HandleRBCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Fields(command)
	if len(parts) < 3 {
		r.sendTemporaryReply(s, m, "❌ Пиши правильно: `!rb <red/black> <сумма>`")
		return
	}

	choice := strings.ToLower(parts[1])
	if choice != "red" && choice != "black" {
		r.sendTemporaryReply(s, m, "❌ Выбирать надо `red` или `black`! Император ждёт! 👑")
		return
	}

	amount, err := strconv.Atoi(parts[2])
	if err != nil || amount <= 0 {
		r.sendTemporaryReply(s, m, "❌ Сумма надо число хорошее! Император не любит шутки! 😡")
		return
	}

	userRating := r.GetRating(m.Author.ID)
	if userRating < amount {
		r.sendTemporaryReply(s, m, fmt.Sprintf("❌ Кредитов мало! Баланса твоя: %d 😢 Император не даст взаймы!", userRating))
		return
	}

	r.mu.Lock()
	var game *RedBlackGame
	for _, g := range r.redBlackGames {
		if g.PlayerID == m.Author.ID && g.Active && g.Bet == 0 {
			game = g
			break
		}
	}
	if game == nil {
		r.sendTemporaryReply(s, m, "❌ Игру начинай с `!rb`! Император ждёт тебя! 👑")
		r.mu.Unlock()
		return
	}

	game.Bet = amount
	game.Choice = choice
	r.mu.Unlock()

	r.UpdateRating(m.Author.ID, -amount)

	// Обновляем "окно игры" с началом анимации
	embed := &discordgo.MessageEmbed{
		Title:       "🎰 Игра: Красный-Чёрный",
		Description: fmt.Sprintf("<@%s> ставка делай %d кредитов на %s!\n\n🎲 Крутим-крутим... Император смотрит! 👑", m.Author.ID, amount, choice),
		Color:       game.Color,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Славь Императора и везёт тебе! 🍀",
		},
	}
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel: m.ChannelID,
		ID:      game.MenuMessageID,
		Embed:   embed,
	})
	if err != nil {
		log.Printf("Failed to update RB message: %v", err)
		return
	}

	// Анимация: переключаем цвета несколько раз
	colors := []string{"🔴", "⚫"}
	for i := 0; i < 5; i++ {
		color := colors[i%2]
		embed.Description = fmt.Sprintf("<@%s> ставка делай %d кредитов на %s!\n\n🎲 Крутим-крутим... %s Император смотрит! 👑", m.Author.ID, amount, choice, color)
		_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel: m.ChannelID,
			ID:      game.MenuMessageID,
			Embed:   embed,
		})
		if err != nil {
			log.Printf("Failed to update RB animation: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Финальный результат
	result := "red"
	if rand.Intn(2) == 1 {
		result = "black"
	}
	colorEmoji := "🔴"
	if result == "black" {
		colorEmoji = "⚫"
	}

	embed.Description = fmt.Sprintf("<@%s> ставка делай %d кредитов на %s!\n\n🎲 Результат: %s", m.Author.ID, amount, choice, colorEmoji)
	if result == choice {
		winnings := amount * 2
		r.UpdateRating(m.Author.ID, winnings)
		embed.Description += fmt.Sprintf("\n\n✅ Победа! Император доволен! Ты бери %d кредитов! 🎉", winnings)
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Император хвалит тебя! 🏆"}
	} else {
		embed.Description += fmt.Sprintf("\n\n❌ Проиграл! Император гневен! Потерял: %d кредитов. 😢", amount)
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Император недоволен! 😡"}
	}

	// Редактируем существующее сообщение вместо создания нового
	customID := fmt.Sprintf("rb_replay_%s_%d", game.PlayerID, time.Now().UnixNano())
	log.Printf("Setting button CustomID: %s", customID)
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Играть снова для Императора! 🎮",
					Style:    discordgo.PrimaryButton,
					CustomID: customID,
				},
			},
		},
	}
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    m.ChannelID,
		ID:         game.MenuMessageID, // Редактируем существующее сообщение
		Embed:      embed,
		Components: &components,
	})
	if err != nil {
		log.Printf("Failed to edit RB message with button: %v", err)
		return
	}

	// Обновляем MenuMessageID (хотя ID не меняется, оставляем для консистентности)
	r.mu.Lock()
	game.Active = false
	delete(r.redBlackGames, game.GameID)
	r.mu.Unlock()

	// Запускаем таймер для отключения кнопки через 15 минут
	go func(messageID string, channelID string) {
		time.Sleep(15 * time.Minute)
		r.mu.Lock()
		// Проверяем, не была ли игра уже завершена
		var activeGame *RedBlackGame
		for _, g := range r.redBlackGames {
			if g.MenuMessageID == messageID && g.Active {
				activeGame = g
				break
			}
		}
		if activeGame == nil {
			// Если игры нет или она неактивна, отключаем кнопку
			emptyComponents := []discordgo.MessageComponent{}
			_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
				Channel:    channelID,
				ID:         messageID,
				Embed:      embed,
				Components: &emptyComponents,
			})
			if err != nil {
				log.Printf("Failed to disable RB button: %v", err)
			}
			log.Printf("Disabled RB button for message %s", messageID)
		}
		r.mu.Unlock()
	}(game.MenuMessageID, m.ChannelID)
}

// !blackjack
func (r *Ranking) StartBlackjackGame(s *discordgo.Session, m *discordgo.MessageCreate) {
	r.mu.Lock()
	gameID := generateGameID(m.Author.ID)
	color := randomColor()
	game := &BlackjackGame{
		GameID:       gameID,
		PlayerID:     m.Author.ID,
		Active:       true,
		LastActivity: time.Now(),
		Color:        color,
		ChannelID:    m.ChannelID,
	}
	r.blackjackGames[gameID] = game
	r.mu.Unlock()

	embed := &discordgo.MessageEmbed{
		Title:       "♠️ Казино: Блэкджек 🎰",
		Description: fmt.Sprintf("Добро пожаловать, <@%s>! 🎉\nСделай ставку, чтобы начать игру.\n\n**💰 Твой баланс:** %d кредитов\n\nНапиши: `!blackjack <сумма>`", m.Author.ID, r.GetRating(m.Author.ID)),
		Color:       color,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Играй с умом! 🍀",
		},
	}
	msg, err := s.ChannelMessageSendEmbed(m.ChannelID, embed)
	if err != nil {
		log.Printf("Failed to send Blackjack menu: %v", err)
		return
	}

	r.mu.Lock()
	game.MenuMessageID = msg.ID
	r.mu.Unlock()

	go r.blackjackTimeout(s, gameID)
}

func (r *Ranking) sendTemporaryReply(s *discordgo.Session, m *discordgo.MessageCreate, content string) {
	msg, err := s.ChannelMessageSendReply(m.ChannelID, content, m.Reference())
	if err != nil {
		log.Printf("Failed to send temporary reply: %v", err)
		return
	}
	time.Sleep(5 * time.Second)
	s.ChannelMessageDelete(m.ChannelID, msg.ID)
}

// !blackjack <amount>
func (r *Ranking) HandleBlackjackBet(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Fields(command)
	if len(parts) != 2 {
		r.sendTemporaryReply(s, m, "❌ Используй: `!blackjack <сумма>`\nПример: `!blackjack 50`")
		return
	}

	amount, err := strconv.Atoi(parts[1])
	if err != nil || amount <= 0 {
		r.sendTemporaryReply(s, m, "❌ Сумма должна быть положительным числом!")
		return
	}

	userRating := r.GetRating(m.Author.ID)
	if userRating < amount {
		r.sendTemporaryReply(s, m, fmt.Sprintf("❌ Недостаточно кредитов! Твой баланс: %d", userRating))
		return
	}

	r.mu.Lock()
	var game *BlackjackGame
	for _, g := range r.blackjackGames {
		if g.PlayerID == m.Author.ID && g.Active && g.Bet == 0 { // Находим первую активную игру без ставки
			game = g
			break
		}
	}
	if game == nil {
		r.sendTemporaryReply(s, m, "❌ Начни игру с помощью `!blackjack`!")
		r.mu.Unlock()
		return
	}

	game.Bet = amount
	game.LastActivity = time.Now() // Обновляем LastActivity
	r.mu.Unlock()

	r.UpdateRating(m.Author.ID, -amount)

	// Инициализируем колоду
	suits := []string{"♠️", "♥️", "♦️", "♣️"}
	values := []string{"2", "3", "4", "5", "6", "7", "8", "9", "10", "J", "Q", "K", "A"}
	deck := make([]Card, 0, 52)
	for _, suit := range suits {
		for _, value := range values {
			deck = append(deck, Card{Suit: suit, Value: value})
		}
	}
	rand.Shuffle(len(deck), func(i, j int) { deck[i], deck[j] = deck[j], deck[i] })

	// Раздаём карты
	playerCards := []Card{deck[0], deck[1]}
	dealerCards := []Card{deck[2], deck[3]}

	r.mu.Lock()
	game.PlayerCards = playerCards
	game.DealerCards = dealerCards
	game.LastActivity = time.Now() // Ещё раз обновляем, чтобы быть уверенными
	r.mu.Unlock()

	// Обновляем "окно игры"
	embed := &discordgo.MessageEmbed{
		Title:       "♠️ Блэкджек 🎲",
		Description: fmt.Sprintf("<@%s> начал игру со ставкой %d кредитов! 💸\n\n**🃏 Твои карты:** %s (Сумма: %d)\n**🃏 Карты дилера:** %s [Скрытая карта]", m.Author.ID, amount, r.cardsToString(playerCards), r.calculateHand(playerCards), r.cardToString(dealerCards[0])),
		Color:       game.Color,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Сделай ход! 🍀",
		},
	}
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{Label: "Взять карту 🃏", Style: discordgo.PrimaryButton, CustomID: fmt.Sprintf("blackjack_hit_%s", game.GameID)},
				discordgo.Button{Label: "Остановиться ⏹️", Style: discordgo.SecondaryButton, CustomID: fmt.Sprintf("blackjack_stand_%s", game.GameID)},
			},
		},
	}

	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    m.ChannelID,
		ID:         game.MenuMessageID,
		Embed:      embed,
		Components: &components,
	})
	if err != nil {
		log.Printf("Failed to update Blackjack game message: %v", err)
	}
}

func (r *Ranking) cardsToString(cards []Card) string {
	result := ""
	for _, card := range cards {
		result += fmt.Sprintf("%s%s ", card.Value, card.Suit)
	}
	return result
}

func (r *Ranking) cardToString(card Card) string {
	return fmt.Sprintf("%s%s", card.Value, card.Suit)
}

func (r *Ranking) calculateHand(cards []Card) int {
	sum := 0
	aces := 0
	for _, card := range cards {
		switch card.Value {
		case "A":
			aces++
			sum += 11
		case "J", "Q", "K":
			sum += 10
		default:
			val, _ := strconv.Atoi(card.Value)
			sum += val
		}
	}
	for sum > 21 && aces > 0 {
		sum -= 10
		aces--
	}
	return sum
}

func (r *Ranking) blackjackTimeout(s *discordgo.Session, gameID string) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		r.mu.Lock()
		game, exists := r.blackjackGames[gameID]
		if !exists || !game.Active {
			r.mu.Unlock()
			return
		}

		if time.Since(game.LastActivity) > 60*time.Second {
			game.Active = false
			embed := &discordgo.MessageEmbed{
				Title:       "♠️ Блэкджек 🎲",
				Description: fmt.Sprintf("Игра завершена: <@%s> ушёл из-за стола! 😢", game.PlayerID),
				Color:       game.Color,
				Footer: &discordgo.MessageEmbedFooter{
					Text: "Время вышло! ⏰",
				},
			}
			_, err := s.ChannelMessageEditEmbed(game.ChannelID, game.MenuMessageID, embed)
			if err != nil {
				log.Printf("Failed to edit message on timeout: %v", err)
			}
			// Удаляем игру из blackjackGames
			delete(r.blackjackGames, gameID)
			r.mu.Unlock()
			s.ChannelMessageSendReply(r.floodChannelID, fmt.Sprintf("♠️ <@%s> ушёл и потерял %d кредитов! 💸", game.PlayerID, game.Bet), &discordgo.MessageReference{MessageID: game.MenuMessageID, ChannelID: r.floodChannelID})
			return
		}
		r.mu.Unlock()
	}
}

func (r *Ranking) HandleBlackjackHit(s *discordgo.Session, i *discordgo.InteractionCreate) {
	parts := strings.Split(i.MessageComponentData().CustomID, "_")
	if len(parts) < 3 {
		log.Printf("Invalid CustomID format: %s", i.MessageComponentData().CustomID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Ошибка: неверный формат кнопки!", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}
	// Объединяем все части после "blackjack_hit_"
	gameID := strings.Join(parts[2:], "_")

	r.mu.Lock()
	game, exists := r.blackjackGames[gameID]
	if !exists {
		log.Printf("Game not found for GameID: %s", gameID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Игра не найдена!", Flags: discordgo.MessageFlagsEphemeral},
		})
		r.mu.Unlock()
		return
	}
	if !game.Active {
		log.Printf("Game is not active for GameID: %s, PlayerID: %s", gameID, game.PlayerID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Игра завершена!", Flags: discordgo.MessageFlagsEphemeral},
		})
		r.mu.Unlock()
		return
	}

	deck := r.generateDeck()
	newCard := deck[len(game.PlayerCards)+len(game.DealerCards)]
	game.PlayerCards = append(game.PlayerCards, newCard)
	game.LastActivity = time.Now() // Обновляем LastActivity
	playerSum := r.calculateHand(game.PlayerCards)

	embed := &discordgo.MessageEmbed{
		Title: "♠️ Блэкджек 🎲",
		Color: game.Color,
	}
	var components []discordgo.MessageComponent
	if playerSum > 21 {
		game.Active = false
		embed.Description = fmt.Sprintf("Ты взял карту: %s\n**🃏 Твои карты:** %s (Сумма: %d)\n**🃏 Карты дилера:** %s [Скрытая]\n\n❌ Перебор! Ты проиграл! 💥", r.cardToString(newCard), r.cardsToString(game.PlayerCards), playerSum, r.cardToString(game.DealerCards[0]))
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Не повезло! 😢"}
		components = []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Label:    "Сыграть снова 🎮",
						Style:    discordgo.PrimaryButton,
						CustomID: fmt.Sprintf("blackjack_replay_%s_%s", game.PlayerID, game.MenuMessageID),
					},
				},
			},
		}
		// Удаляем игру из blackjackGames
		delete(r.blackjackGames, gameID)
	} else {
		embed.Description = fmt.Sprintf("Ты взял карту: %s\n**🃏 Твои карты:** %s (Сумма: %d)\n**🃏 Карты дилера:** %s [Скрытая]", r.cardToString(newCard), r.cardsToString(game.PlayerCards), playerSum, r.cardToString(game.DealerCards[0]))
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Продолжаем! 🍀"}
		components = []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{Label: "Взять карту 🃏", Style: discordgo.PrimaryButton, CustomID: fmt.Sprintf("blackjack_hit_%s", game.GameID)},
					discordgo.Button{Label: "Остановиться ⏹️", Style: discordgo.SecondaryButton, CustomID: fmt.Sprintf("blackjack_stand_%s", game.GameID)},
				},
			},
		}
	}
	r.mu.Unlock()

	_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    i.ChannelID,
		ID:         game.MenuMessageID,
		Embed:      embed,
		Components: &components,
	})
	if err != nil {
		log.Printf("Failed to update Blackjack message: %v", err)
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate})
}

func (r *Ranking) HandleBlackjackStand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	parts := strings.Split(i.MessageComponentData().CustomID, "_")
	if len(parts) < 3 {
		log.Printf("Invalid CustomID format: %s", i.MessageComponentData().CustomID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Ошибка: неверный формат кнопки!", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}
	// Объединяем все части после "blackjack_stand_"
	gameID := strings.Join(parts[2:], "_")

	r.mu.Lock()
	game, theGameExists := r.blackjackGames[gameID]
	if !theGameExists {
		log.Printf("Game not found for GameID: %s", gameID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Игра не найдена!", Flags: discordgo.MessageFlagsEphemeral},
		})
		r.mu.Unlock()
		return
	}
	if !game.Active {
		log.Printf("Game is not active for GameID: %s, PlayerID: %s", gameID, game.PlayerID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Игра завершена!", Flags: discordgo.MessageFlagsEphemeral},
		})
		r.mu.Unlock()
		return
	}

	game.LastActivity = time.Now() // Обновляем LastActivity
	playerSum := r.calculateHand(game.PlayerCards)
	dealerSum := r.calculateHand(game.DealerCards)

	deck := r.generateDeck()
	cardIndex := len(game.PlayerCards) + len(game.DealerCards)
	for dealerSum < 17 && cardIndex < len(deck) {
		game.DealerCards = append(game.DealerCards, deck[cardIndex])
		dealerSum = r.calculateHand(game.DealerCards)
		cardIndex++
	}

	embed := &discordgo.MessageEmbed{
		Title:       "♠️ Блэкджек 🎲",
		Description: fmt.Sprintf("**🃏 Твои карты:** %s (Сумма: %d)\n**🃏 Карты дилера:** %s (Сумма: %d)", r.cardsToString(game.PlayerCards), playerSum, r.cardsToString(game.DealerCards), dealerSum),
		Color:       game.Color,
	}

	var result string
	if dealerSum > 21 {
		winnings := game.Bet * 2
		r.UpdateRating(game.PlayerID, winnings)
		result = fmt.Sprintf("✅ Дилер перебрал! Ты выиграл %d кредитов! 🎉", winnings)
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Победа! 🏆"}
	} else if playerSum > dealerSum {
		winnings := game.Bet * 2
		r.UpdateRating(game.PlayerID, winnings)
		result = fmt.Sprintf("✅ Ты выиграл! %d кредитов твои! 🎉", winnings)
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Победа! 🏆"}
	} else if playerSum == dealerSum {
		r.UpdateRating(game.PlayerID, game.Bet)
		result = "🤝 Ничья! Твоя ставка возвращена. 🔄"
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Ничья! 🤝"}
	} else {
		result = "❌ Дилер победил! 💥"
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Не повезло! 😢"}
	}

	embed.Description += fmt.Sprintf("\n\n%s", result)

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Сыграть снова 🎮",
					Style:    discordgo.PrimaryButton,
					CustomID: fmt.Sprintf("blackjack_replay_%s_%s", game.PlayerID, game.MenuMessageID),
				},
			},
		},
	}

	game.Active = false
	// Удаляем игру из blackjackGames
	delete(r.blackjackGames, gameID)
	r.mu.Unlock()

	_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    i.ChannelID,
		ID:         game.MenuMessageID,
		Embed:      embed,
		Components: &components,
	})
	if err != nil {
		log.Printf("Failed to update Blackjack message: %v", err)
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate})
}

func (r *Ranking) HandleBlackjackReplay(s *discordgo.Session, i *discordgo.InteractionCreate) {
	parts := strings.Split(i.MessageComponentData().CustomID, "_")
	if len(parts) != 4 {
		log.Printf("Invalid CustomID format: %s", i.MessageComponentData().CustomID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Ошибка: неверный формат кнопки!", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}
	playerID := parts[2]
	menuMessageID := parts[3]

	newGameID := generateGameID(playerID)
	newColor := randomColor()
	game := &BlackjackGame{
		GameID:        newGameID,
		PlayerID:      playerID,
		Active:        true,
		LastActivity:  time.Now(),
		Color:         newColor,
		ChannelID:     i.ChannelID,
		MenuMessageID: menuMessageID, // Используем переданный MenuMessageID
	}

	r.mu.Lock()
	r.blackjackGames[newGameID] = game
	r.mu.Unlock()

	embed := &discordgo.MessageEmbed{
		Title:       "♠️ Казино: Блэкджек 🎰",
		Description: fmt.Sprintf("Добро пожаловать, <@%s>! 🎉\nСделай ставку, чтобы начать игру.\n\n**💰 Твой баланс:** %d кредитов\n\nНапиши: `!blackjack <сумма>`", playerID, r.GetRating(playerID)),
		Color:       newColor,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Играй с умом! 🍀",
		},
	}

	_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    i.ChannelID,
		ID:         menuMessageID,
		Embed:      embed,
		Components: &[]discordgo.MessageComponent{}, // Убираем кнопки
	})
	if err != nil {
		log.Printf("Failed to update Blackjack menu: %v", err)
	}

	go r.blackjackTimeout(s, newGameID)

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate})
}

func (r *Ranking) HandleRBReplay(s *discordgo.Session, i *discordgo.InteractionCreate) {
	log.Printf("HandleRBReplay called, CustomID: %s", i.MessageComponentData().CustomID)

	// Сначала отвечаем на взаимодействие, чтобы избежать тайм-аута
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})
	if err != nil {
		log.Printf("Failed to respond to interaction: %v", err)
		return
	}
	log.Printf("Interaction response sent for player %s", i.Member.User.ID)

	// Разбираем CustomID
	parts := strings.Split(i.MessageComponentData().CustomID, "_")
	if len(parts) != 4 { // Ожидаем rb_replay_<playerID>_<timestamp>
		log.Printf("Invalid CustomID format: %s, expected 4 parts, got %d", i.MessageComponentData().CustomID, len(parts))
		_, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "❌ Ошибка: кнопка сломана! Император гневен! 😡",
			Flags:   discordgo.MessageFlagsEphemeral,
		})
		if err != nil {
			log.Printf("Failed to send followup message: %v", err)
		}
		return
	}
	playerID := parts[2]
	// timestamp := parts[3] // Не используем, но оставляем для формата
	log.Printf("Parsed playerID: %s", playerID)

	// Проверяем, совпадает ли playerID с пользователем, который нажал кнопку
	if playerID != i.Member.User.ID {
		log.Printf("PlayerID mismatch: expected %s, got %s", playerID, i.Member.User.ID)
		_, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "❌ Кнопка не твоя! Император не позволит! 👑",
			Flags:   discordgo.MessageFlagsEphemeral,
		})
		if err != nil {
			log.Printf("Failed to send followup message: %v", err)
		}
		return
	}

	// Создаём новую игру
	newGameID := generateGameID(playerID)
	newColor := randomColor()
	game := &RedBlackGame{
		GameID:   newGameID,
		PlayerID: playerID,
		Active:   true,
		Color:    newColor,
	}
	r.mu.Lock()
	r.redBlackGames[newGameID] = game
	r.mu.Unlock()
	log.Printf("Created new RB game with ID %s for player %s", newGameID, playerID)

	// Редактируем существующее сообщение вместо создания нового
	embed := &discordgo.MessageEmbed{
		Title:       "🎰 Игра: Красный-Чёрный",
		Description: fmt.Sprintf("Велком снова, <@%s>! 🥳\nИмператор даёт шанс: выбирать цвет и ставка делай!\n\n**💰 Баланса твоя:** %d кредитов\n\nПиши вот: `!rb <red/black> <сумма>`\nНапример: `!rb red 50`\nИмператор следит за тобой! 👑", playerID, r.GetRating(playerID)),
		Color:       newColor,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Славь Императора и везёт тебе! 🍀",
		},
	}
	log.Printf("Editing existing RB embed for player %s, message ID: %s", playerID, i.Message.ID)

	// Удаляем кнопки, чтобы пользователь не мог нажать "Сыграть снова" во время новой игры
	emptyComponents := []discordgo.MessageComponent{}
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    i.ChannelID,
		ID:         i.Message.ID, // Используем ID текущего сообщения
		Embed:      embed,
		Components: &emptyComponents, // Убираем кнопки
	})
	if err != nil {
		log.Printf("Failed to edit RB menu: %v", err)
		_, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "❌ Ошибка! Игру не обновить! Император гневен! Проверь права бота! 😡",
			Flags:   discordgo.MessageFlagsEphemeral,
		})
		if err != nil {
			log.Printf("Failed to send followup message: %v", err)
		}
		return
	}
	log.Printf("RB embed edited successfully for player %s, message ID: %s", playerID, i.Message.ID)

	// Обновляем MenuMessageID
	r.mu.Lock()
	game.MenuMessageID = i.Message.ID // ID сообщения не меняется, но обновляем для консистентности
	r.mu.Unlock()

	// Запускаем таймер для отключения игры через 15 минут
	go func(messageID string, channelID string) {
		time.Sleep(15 * time.Minute)
		r.mu.Lock()
		if g, exists := r.redBlackGames[newGameID]; exists && g.Active {
			g.Active = false
			delete(r.redBlackGames, newGameID)
			timeoutEmbed := &discordgo.MessageEmbed{
				Title:       "🎰 Игра: Красный-Чёрный",
				Description: fmt.Sprintf("Игра закончи, <@%s>! Время нету. ⏰\nИмператор недоволен! 😡", playerID),
				Color:       newColor,
				Footer: &discordgo.MessageEmbedFooter{
					Text: "Время вышло! Император гневен! ⏰",
				},
			}
			_, err := s.ChannelMessageEditEmbed(channelID, messageID, timeoutEmbed)
			if err != nil {
				log.Printf("Failed to update RB message on timeout: %v", err)
			}
		}
		r.mu.Unlock()
	}(i.Message.ID, i.ChannelID)
}

func (r *Ranking) HandleEndBlackjackCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !endblackjack: %s from %s", command, m.Author.ID)

	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут завершать игры! 🔒")
		return
	}

	parts := strings.Fields(command)
	if len(parts) != 2 {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!endblackjack @id`")
		return
	}

	targetID := strings.TrimPrefix(parts[1], "<@")
	targetID = strings.TrimSuffix(targetID, ">")
	targetID = strings.TrimPrefix(targetID, "!")

	r.mu.Lock()
	var game *BlackjackGame
	for _, g := range r.blackjackGames {
		if g.PlayerID == targetID && g.Active {
			game = g
			break
		}
	}
	if game == nil {
		s.ChannelMessageSend(m.ChannelID, "❌ У этого пользователя нет активной игры в блэкджек! ♠️")
		r.mu.Unlock()
		return
	}

	game.Active = false
	// Удаляем игру из blackjackGames
	delete(r.blackjackGames, game.GameID)
	// Удаляем кнопки
	emptyComponents := []discordgo.MessageComponent{}
	emptyComponentsPtr := &emptyComponents
	_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    m.ChannelID,
		ID:         game.MenuMessageID,
		Embed:      &discordgo.MessageEmbed{Title: "♠️ Блэкджек 🎲", Description: fmt.Sprintf("Игра завершена админом: <@%s>! 🚫", targetID), Color: 0xFFD700, Footer: &discordgo.MessageEmbedFooter{Text: "Игра завершена! ⏹️"}},
		Components: emptyComponentsPtr,
	})
	if err != nil {
		log.Printf("Failed to remove buttons on admin end: %v", err)
	}
	r.mu.Unlock()

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("♠️ Игра в блэкджек для <@%s> завершена! Ставка не возвращена. 💸", targetID))
	log.Printf("Blackjack game for %s ended by admin %s", targetID, m.Author.ID)
}

func (r *Ranking) generateDeck() []Card {
	suits := []string{"♠", "♥", "♦", "♣"}
	values := []string{"2", "3", "4", "5", "6", "7", "8", "9", "10", "J", "Q", "K", "A"}
	deck := make([]Card, 0, 52)
	for _, suit := range suits {
		for _, value := range values {
			deck = append(deck, Card{Suit: suit, Value: value})
		}
	}
	rand.Shuffle(len(deck), func(i, j int) { deck[i], deck[j] = deck[j], deck[i] })
	return deck
}

func (r *Ranking) HandleChinaGive(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !china give: %s from %s", command, m.Author.ID)

	parts := strings.Fields(command)
	if len(parts) < 4 || parts[1] != "give" {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!china give @id <сумма> [причина]`")
		return
	}

	targetID := strings.TrimPrefix(parts[2], "<@")
	targetID = strings.TrimSuffix(targetID, ">")
	targetID = strings.TrimPrefix(targetID, "!")

	amount, err := strconv.Atoi(parts[3])
	if err != nil || amount <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть положительным числом! 💸")
		return
	}

	senderRating := r.GetRating(m.Author.ID)
	if senderRating < amount {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Недостаточно кредитов! Твой баланс: %d 💰", senderRating))
		return
	}

	r.UpdateRating(m.Author.ID, -amount)
	r.UpdateRating(targetID, amount)

	reason := ""
	if len(parts) > 4 {
		reason = strings.Join(parts[4:], " ")
	}

	response := fmt.Sprintf("✅ <@%s> передал %d кредитов <@%s>! 💸", m.Author.ID, amount, targetID)
	if reason != "" {
		response += fmt.Sprintf(" | 📜 Причина: %s", reason)
	}
	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("User %s gave %d credits to %s. Reason: %s", m.Author.ID, amount, targetID, reason)
}

func (r *Ranking) HandleChinaRating(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !china rating: %s from %s", command, m.Author.ID)

	parts := strings.Fields(command)
	if len(parts) < 3 || parts[1] != "rating" {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!china rating @id`")
		return
	}

	targetID := strings.TrimPrefix(parts[2], "<@")
	targetID = strings.TrimSuffix(targetID, ">")
	targetID = strings.TrimPrefix(targetID, "!")

	rating := r.GetRating(targetID)
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("💰 Рейтинг <@%s>: %d кредитов 📈", targetID, rating))
	log.Printf("Rating for %s requested by %s: %d", targetID, m.Author.ID, rating)
}

// !china clear coins
func (r *Ranking) HandleClearCoinsCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Processing !china clear coins: %s from %s", m.Content, m.Author.ID)

	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут очищать кредиты! 🔒")
		return
	}

	// Очищаем все ключи в Redis
	keys, err := r.redis.Keys(r.ctx, "user:*").Result()
	if err != nil {
		log.Printf("Failed to get user keys from Redis: %v", err)
		s.ChannelMessageSend(m.ChannelID, "❌ Ошибка при очистке кредитов! 🚫")
		return
	}

	for _, key := range keys {
		r.redis.Del(r.ctx, key)
	}

	s.ChannelMessageSend(m.ChannelID, "✅ Все кредиты обнулены! 🧹")
	log.Printf("All credits cleared by %s", m.Author.ID)
}

// !china gift all <amount>
func (r *Ranking) HandleGiftAllCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !china gift all: %s from %s", command, m.Author.ID)

	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут раздавать кредиты! 🔒")
		return
	}

	parts := strings.Fields(command)
	if len(parts) != 4 || parts[1] != "gift" || parts[2] != "all" {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!china gift all <сумма>`")
		return
	}

	amount, err := strconv.Atoi(parts[3])
	if err != nil || amount <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть положительным числом! 💸")
		return
	}

	// Получаем всех пользователей из Redis
	keys, err := r.redis.Keys(r.ctx, "user:*").Result()
	if err != nil {
		log.Printf("Failed to get user keys from Redis: %v", err)
		s.ChannelMessageSend(m.ChannelID, "❌ Ошибка при раздаче кредитов! 🚫")
		return
	}

	for _, key := range keys {
		userID := strings.TrimPrefix(key, "user:")
		r.UpdateRating(userID, amount)
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("✅ Каждый получил %d кредитов! 🎁", amount))
	log.Printf("Admin %s gifted %d credits to all users", m.Author.ID, amount)
}

func (r *Ranking) HandleAdminGive(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !admin give: %s from %s", command, m.Author.ID)

	parts := strings.Fields(command)
	if len(parts) < 4 || parts[1] != "give" {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!admin give @id <сумма> [причина]`")
		return
	}

	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут это делать! 🔒")
		return
	}

	targetID := strings.TrimPrefix(parts[2], "<@")
	targetID = strings.TrimSuffix(targetID, ">")
	targetID = strings.TrimPrefix(targetID, "!")

	amount, err := strconv.Atoi(parts[3])
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть числом! 💸")
		return
	}

	r.UpdateRating(targetID, amount)

	reason := ""
	if len(parts) > 4 {
		reason = strings.Join(parts[4:], " ")
	}

	verb := "повысил"
	if amount < 0 {
		verb = "понизил"
		amount = -amount
	}
	response := fmt.Sprintf("✅ Админ <@%s> %s рейтинг <@%s> на %d кредитов! ⚙️", m.Author.ID, verb, targetID, amount)
	if reason != "" {
		response += fmt.Sprintf(" | 📜 Причина: %s", reason)
	}
	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("Admin %s changed rating of %s by %d. Reason: %s", m.Author.ID, targetID, amount, reason)
}

func (r *Ranking) HandleHelpCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Processing !chelp: %s from %s", m.Content, m.Author.ID)

	response := "📜 **Команды бота:**\n" +
		"🎉 **!cpoll Вопрос [Вариант1] [Вариант2] ...** - (Админ) Создать опрос\n" +
		"💸 **!dep <ID_опроса> <номер_варианта> <сумма>** - Сделать ставку\n" +
		"🔒 **!closedep <ID_опроса> <номер>** - (Админ) Закрыть опрос\n" +
		"📊 **!polls** - Показать активные опросы и ставки\n" +
		"🎰 **!rb** - Начать игру в Красное-Чёрное\n" + // Изменено с "!double" на "!rb"
		"♠️ **!blackjack** - Начать игру в блэкджек\n" +
		"🚫 **!endblackjack @id** - (Админ) Завершить игру в блэкджек\n" +
		"💰 **!china give @id <сумма> [причина]** - Передать кредиты\n" +
		"📈 **!china rating @id** - Проверить рейтинг\n" +
		"🧹 **!china clear coins** - (Админ) Обнулить кредиты у всех\n" +
		"🎁 **!china gift all <сумма>** - (Админ) Раздать кредиты всем\n" +
		"⚙️ **!admin give @id <сумма> [причина]** - (Админ) Выдать/забрать кредиты\n" +
		"❓ **!chelp** - Показать помощь\n" +
		"🏆 **!top5** - Топ-5 по рейтингу"
	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("Help sent to %s", m.Author.ID)
}

func splitCommand(command string) []string {
	var parts []string
	var current string
	inQuotes := false
	inBrackets := false

	for _, char := range command {
		if char == '"' {
			inQuotes = !inQuotes
			current += string(char)
			continue
		}
		if char == '[' {
			inBrackets = true
			current += string(char)
			continue
		}
		if char == ']' {
			inBrackets = false
			current += string(char)
			continue
		}
		if char == ' ' && !inQuotes && !inBrackets {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
			continue
		}
		current += string(char)
	}
	if current != "" {
		parts = append(parts, current)
	}
	log.Printf("Split command: %v", parts)
	return parts
}

func (r *Ranking) TrackVoiceActivity(s *discordgo.Session) {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			r.mu.Lock()
			now := int(time.Now().Unix())
			for userID, lastTime := range r.voiceAct {
				if now-lastTime >= 60 {
					guilds, err := s.UserGuilds(100, "", "", false)
					if err != nil {
						log.Printf("Error getting guilds for %s: %v", userID, err)
						continue
					}
					inChannel := false
					for _, guild := range guilds {
						guildState, err := s.State.Guild(guild.ID)
						if err != nil {
							continue
						}
						for _, vs := range guildState.VoiceStates {
							if vs.UserID == userID && vs.ChannelID != "" {
								inChannel = true
								break
							}
						}
						if inChannel {
							break
						}
					}
					if inChannel {
						r.UpdateRating(userID, 1)
						r.voiceAct[userID] = now
						log.Printf("User %s earned 5 credits for voice activity", userID)
					} else {
						delete(r.voiceAct, userID)
					}
				}
			}
			r.mu.Unlock()
		}
	}()

	s.AddHandler(func(s *discordgo.Session, v *discordgo.VoiceStateUpdate) {
		if v.UserID == s.State.User.ID {
			return
		}
		r.mu.Lock()
		if v.ChannelID != "" {
			r.UpdateRating(v.UserID, 0)
			r.voiceAct[v.UserID] = int(time.Now().Unix())
		} else {
			delete(r.voiceAct, v.UserID)
		}
		r.mu.Unlock()
	})
}
