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
	PlayerID      string
	Bet           int
	Choice        string // "red" или "black"
	Active        bool
	MenuMessageID string // ID сообщения с меню
}

type Card struct {
	Suit  string // Масть (♠, ♥, ♦, ♣)
	Value string // Значение (2-10, J, Q, K, A)
}

type BlackjackGame struct {
	PlayerID      string
	Bet           int
	PlayerCards   []Card
	DealerCards   []Card
	Active        bool
	LastActivity  time.Time
	MenuMessageID string // ID сообщения с меню
	GameMessageID string // ID сообщения с игрой
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

	r.redis = redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})
	if _, err := r.redis.Ping(r.ctx).Result(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %v", err)
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
	data, err := r.redis.Get(r.ctx, "user:"+userID).Result()
	if err == redis.Nil {
		return 0
	}
	if err != nil {
		log.Printf("Failed to get rating for %s from Redis: %v", userID, err)
		return 0
	}

	var user User
	if err := json.Unmarshal([]byte(data), &user); err != nil {
		log.Printf("Failed to unmarshal user %s: %v", userID, err)
		return 0
	}
	return user.Rating
}

func (r *Ranking) UpdateRating(userID string, points int) {
	data, err := r.redis.Get(r.ctx, "user:"+userID).Result()
	user := User{ID: userID, Rating: 0}
	if err == nil {
		if err := json.Unmarshal([]byte(data), &user); err != nil {
			log.Printf("Failed to unmarshal user %s: %v", userID, err)
			return
		}
	} else if err != redis.Nil {
		log.Printf("Failed to get user %s from Redis: %v", userID, err)
		return
	}

	user.Rating += points
	if user.Rating < 0 {
		user.Rating = 0
	}

	dataBytes, err := json.Marshal(user)
	if err != nil {
		log.Printf("Failed to marshal user %s: %v", userID, err)
		return
	}

	if err := r.redis.Set(r.ctx, "user:"+userID, dataBytes, 0).Err(); err != nil {
		log.Printf("Failed to save user %s to Redis: %v", userID, err)
	}
	log.Printf("Updated rating for %s: %d (change: %d)", userID, user.Rating, points)
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
		s.ChannelMessageSend(m.ChannelID, "❌ Только товарищи-админы могут создавать опросы!")
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
		s.ChannelMessageSend(m.ChannelID, "❌ Вопрос не может быть пустым!")
		return
	}

	if len(options) < 2 {
		s.ChannelMessageSend(m.ChannelID, "❌ Нужно минимум 2 варианта ответа!")
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

	response := fmt.Sprintf("🎉 Опрос %s запущен! <@%s> создал опрос: %s\nВарианты:\n", pollID, m.Author.ID, question)
	for i, opt := range options {
		response += fmt.Sprintf("%d. [%s]\n", i+1, opt)
	}
	response += fmt.Sprintf("Ставьте: `!dep %s <номер_варианта> <сумма>`\nЗакрытие: `!closedep %s <номер>`", pollID, pollID)
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
		s.ChannelMessageSend(m.ChannelID, "❌ Номер варианта должен быть числом!")
		return
	}

	amount, err := strconv.Atoi(parts[3])
	if err != nil || amount <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть положительным числом!")
		return
	}

	r.mu.Lock()
	poll, exists := r.polls[pollID]
	if !exists || !poll.Active {
		s.ChannelMessageSend(m.ChannelID, "❌ Опрос не найден или уже закрыт!")
		r.mu.Unlock()
		return
	}

	if option < 1 || option > len(poll.Options) {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Номер варианта должен быть от 1 до %d!", len(poll.Options)))
		r.mu.Unlock()
		return
	}

	userRating := r.GetRating(m.Author.ID)
	if userRating < amount {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Недостаточно кредитов! Твой баланс: %d", userRating))
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

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🎲 <@%s> поставил %d кредитов на [%s] в опросе %s\nТекущий коэффициент: %.2f", m.Author.ID, amount, poll.Options[option-1], poll.Question, coefficient))
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
		s.ChannelMessageSend(m.ChannelID, "❌ Номер варианта должен быть числом!")
		return
	}

	r.mu.Lock()
	poll, exists := r.polls[pollID]
	if !exists {
		s.ChannelMessageSend(m.ChannelID, "❌ Опрос не найден!")
		r.mu.Unlock()
		return
	}
	if !poll.Active {
		s.ChannelMessageSend(m.ChannelID, "❌ Опрос уже закрыт!")
		r.mu.Unlock()
		return
	}

	if m.Author.ID != poll.Creator {
		s.ChannelMessageSend(m.ChannelID, "❌ Только создатель опроса может его закрыть!")
		r.mu.Unlock()
		return
	}

	if winningOption < 1 || winningOption > len(poll.Options) {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Номер варианта должен быть от 1 до %d!", len(poll.Options)))
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

	response := fmt.Sprintf("✅ Опрос %s завершён! Победил: [%s] (№%d)\nКоэффициент: %.2f\nПобедители:\n", pollID, poll.Options[winningOption-1], winningOption, coefficient)
	for userID, choice := range poll.Choices {
		if choice == winningOption {
			winnings := int(float64(poll.Bets[userID]) * coefficient)
			r.UpdateRating(userID, winnings+poll.Bets[userID])
			response += fmt.Sprintf("<@%s>: %d кредитов (ставка: %d)\n", userID, winnings+poll.Bets[userID], poll.Bets[userID])
		}
	}
	if winnersBet == 0 {
		response += "Никто не победил!"
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
		s.ChannelMessageSend(m.ChannelID, "📊 Нет активных опросов! Создай новый с помощью `!cpoll`!")
		return
	}

	response := "📊 **Активные опросы:**\n"
	for pollID, poll := range r.polls {
		if !poll.Active {
			continue
		}
		response += fmt.Sprintf("\n**Опрос %s: %s**\n", pollID, poll.Question)
		coefficients := poll.GetCoefficients()
		for i, option := range poll.Options {
			response += fmt.Sprintf("Вариант %d. [%s] (Коэффициент: %.2f)\n", i+1, option, coefficients[i])
			// Показываем, кто поставил на этот вариант
			for userID, choice := range poll.Choices {
				if choice == i+1 {
					bet := poll.Bets[userID]
					potentialWin := int(float64(bet) * coefficients[i])
					response += fmt.Sprintf("  - <@%s>: %d кредитов (Потенциальный выигрыш: %d)\n", userID, bet, potentialWin+bet)
				}
			}
		}
	}

	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("Polls list sent to %s", m.Author.ID)
}

// !redblack
func (r *Ranking) StartRedBlackGame(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Processing !redblack: %s from %s", m.Content, m.Author.ID)

	r.mu.Lock()
	// Проверяем, не играет ли пользователь уже
	if game, exists := r.redBlackGames[m.Author.ID]; exists && game.Active {
		s.ChannelMessageSend(m.ChannelID, "❌ Ты уже играешь! Заверши текущую игру или выбери ставку!")
		r.mu.Unlock()
		return
	}

	// Создаём новую игру
	game := &RedBlackGame{
		PlayerID: m.Author.ID,
		Active:   true,
	}
	r.redBlackGames[m.Author.ID] = game
	r.mu.Unlock()

	// Отправляем меню
	embed := &discordgo.MessageEmbed{
		Title:       "🎰 Казино: Красное-Чёрное",
		Description: fmt.Sprintf("Добро пожаловать, <@%s>!\nВыбери цвет и сделай ставку.\n\n**Твой баланс:** %d кредитов\n\nНапиши: `!redblack <red/black> <сумма>`\nПример: `!redblack red 50`", m.Author.ID, r.GetRating(m.Author.ID)),
		Color:       0xFFD700, // Золотой цвет
	}
	msg, err := s.ChannelMessageSendEmbed(m.ChannelID, embed)
	if err != nil {
		log.Printf("Failed to send RedBlack menu: %v", err)
		return
	}

	r.mu.Lock()
	game.MenuMessageID = msg.ID
	r.mu.Unlock()
}

// !redblack <red/black> <amount>
func (r *Ranking) HandleRedBlackCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !redblack: %s from %s", command, m.Author.ID)

	parts := strings.Fields(command)
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!redblack <red/black> <сумма>`\nПример: `!redblack red 50`")
		return
	}

	choice := strings.ToLower(parts[1])
	if choice != "red" && choice != "black" {
		s.ChannelMessageSend(m.ChannelID, "❌ Выбери `red` или `black`!")
		return
	}

	amount, err := strconv.Atoi(parts[2])
	if err != nil || amount <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть положительным числом!")
		return
	}

	userRating := r.GetRating(m.Author.ID)
	if userRating < amount {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Недостаточно кредитов! Твой баланс: %d", userRating))
		return
	}

	r.mu.Lock()
	game, exists := r.redBlackGames[m.Author.ID]
	if !exists || !game.Active {
		s.ChannelMessageSend(m.ChannelID, "❌ Начни игру с помощью `!redblack`!")
		r.mu.Unlock()
		return
	}

	// Обновляем игру
	game.Bet = amount
	game.Choice = choice
	r.mu.Unlock()

	// Снимаем ставку
	r.UpdateRating(m.Author.ID, -amount)

	// Удаляем меню
	if game.MenuMessageID != "" {
		s.ChannelMessageDelete(m.ChannelID, game.MenuMessageID)
	}

	// Анимация
	frames := []string{
		"⚫🔴⚫🔴⚫🔴⚫🔴",
		"🔴⚫🔴⚫🔴⚫🔴⚫",
		"⚫🔴⚫🔴⚫🔴⚫🔴",
		"🔴⚫🔴⚫🔴⚫🔴⚫",
	}
	embed := &discordgo.MessageEmbed{
		Title: "🎰 Красное-Чёрное: Анимация",
		Color: 0xFFD700,
	}
	msg, err := s.ChannelMessageSendEmbed(m.ChannelID, embed)
	if err != nil {
		log.Printf("Failed to send RedBlack animation: %v", err)
		return
	}

	for _, frame := range frames {
		embed.Description = fmt.Sprintf("<@%s> поставил %d кредитов на %s!\n\n%s", m.Author.ID, amount, choice, frame)
		_, err = s.ChannelMessageEditEmbed(m.ChannelID, msg.ID, embed)
		if err != nil {
			log.Printf("Failed to update RedBlack animation: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Генерируем результат
	rand.Seed(time.Now().UnixNano())
	result := "red"
	if rand.Intn(2) == 1 {
		result = "black"
	}

	// Визуал
	colorEmoji := "🔴"
	if result == "black" {
		colorEmoji = "⚫"
	}
	embed.Description = fmt.Sprintf("<@%s> поставил %d кредитов на %s!\n\nРезультат: %s", m.Author.ID, amount, choice, colorEmoji)

	// Проверяем результат
	if result == choice {
		winnings := amount * 2
		r.UpdateRating(m.Author.ID, winnings)
		embed.Description += fmt.Sprintf("\n\n✅ Победа! Ты выиграл %d кредитов!", winnings)
	} else {
		embed.Description += fmt.Sprintf("\n\n❌ Проигрыш! Потеряно: %d кредитов.", amount)
	}

	// Добавляем кнопку "Сыграть снова"
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Сыграть снова",
					Style:    discordgo.PrimaryButton,
					CustomID: fmt.Sprintf("redblack_replay_%s", m.Author.ID),
				},
			},
		},
	}

	// Исправлено: передаём указатель на слайс
	componentsPtr := &components
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    m.ChannelID,
		ID:         msg.ID,
		Embed:      embed,
		Components: componentsPtr,
	})
	if err != nil {
		log.Printf("Failed to add replay button for RedBlack: %v", err)
	}

	// Завершаем игру
	r.mu.Lock()
	game.Active = false
	r.mu.Unlock()

	log.Printf("RedBlack game for %s: bet %d on %s, result %s", m.Author.ID, amount, choice, result)
}

// !blackjack
func (r *Ranking) StartBlackjackGame(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Processing !blackjack: %s from %s", m.Content, m.Author.ID)

	r.mu.Lock()
	if game, exists := r.blackjackGames[m.Author.ID]; exists && game.Active {
		s.ChannelMessageSend(m.ChannelID, "❌ Ты уже играешь в блэкджек! Заверши текущую игру или выбери ставку!")
		r.mu.Unlock()
		return
	}

	// Создаём новую игру
	game := &BlackjackGame{
		PlayerID:     m.Author.ID,
		Active:       true,
		LastActivity: time.Now(),
	}
	r.blackjackGames[m.Author.ID] = game
	r.mu.Unlock()

	// Отправляем меню
	embed := &discordgo.MessageEmbed{
		Title:       "♠️ Казино: Блэкджек",
		Description: fmt.Sprintf("Добро пожаловать, <@%s>!\nСделай ставку, чтобы начать игру.\n\n**Твой баланс:** %d кредитов\n\nНапиши: `!blackjack <сумма>`\nПример: `!blackjack 50`", m.Author.ID, r.GetRating(m.Author.ID)),
		Color:       0xFFD700, // Золотой цвет
	}
	msg, err := s.ChannelMessageSendEmbed(m.ChannelID, embed)
	if err != nil {
		log.Printf("Failed to send Blackjack menu: %v", err)
		return
	}

	r.mu.Lock()
	game.MenuMessageID = msg.ID
	r.mu.Unlock()

	// Запускаем таймер неактивности
	go r.blackjackTimeout(s, m.Author.ID)
}

// !blackjack <amount>
func (r *Ranking) HandleBlackjackBet(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !blackjack bet: %s from %s", command, m.Author.ID)

	parts := strings.Fields(command)
	if len(parts) != 2 {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!blackjack <сумма>`\nПример: `!blackjack 50`")
		return
	}

	amount, err := strconv.Atoi(parts[1])
	if err != nil || amount <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть положительным числом!")
		return
	}

	userRating := r.GetRating(m.Author.ID)
	if userRating < amount {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Недостаточно кредитов! Твой баланс: %d", userRating))
		return
	}

	r.mu.Lock()
	game, exists := r.blackjackGames[m.Author.ID]
	if !exists || !game.Active {
		s.ChannelMessageSend(m.ChannelID, "❌ Начни игру с помощью `!blackjack`!")
		r.mu.Unlock()
		return
	}

	// Обновляем игру
	game.Bet = amount
	r.mu.Unlock()

	// Снимаем ставку
	r.UpdateRating(m.Author.ID, -amount)

	// Удаляем меню
	if game.MenuMessageID != "" {
		s.ChannelMessageDelete(m.ChannelID, game.MenuMessageID)
	}

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
	r.mu.Unlock()

	// Показываем карты
	embed := &discordgo.MessageEmbed{
		Title:       "♠️ Блэкджек",
		Description: fmt.Sprintf("<@%s> начал игру со ставкой %d кредитов!\n\n**Твои карты:** %s (Сумма: %d)\n**Карты дилера:** %s [Скрытая карта]", m.Author.ID, amount, r.cardsToString(playerCards), r.calculateHand(playerCards), r.cardToString(dealerCards[0])),
		Color:       0xFFD700,
	}

	// Добавляем кнопки Hit и Stand
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Hit",
					Style:    discordgo.PrimaryButton,
					CustomID: fmt.Sprintf("blackjack_hit_%s", m.Author.ID),
				},
				discordgo.Button{
					Label:    "Stand",
					Style:    discordgo.SecondaryButton,
					CustomID: fmt.Sprintf("blackjack_stand_%s", m.Author.ID),
				},
			},
		},
	}

	// Исправлено: передаём слайс components напрямую, без указателя
	msg, err := s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Embed:      embed,
		Components: components, // Убрали componentsPtr, так как MessageSend ожидает []discordgo.MessageComponent
	})
	if err != nil {
		log.Printf("Failed to send Blackjack game message: %v", err)
		return
	}

	r.mu.Lock()
	game.GameMessageID = msg.ID
	r.mu.Unlock()
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

func (r *Ranking) blackjackTimeout(s *discordgo.Session, playerID string) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		r.mu.Lock()
		game, exists := r.blackjackGames[playerID]
		if !exists || !game.Active {
			r.mu.Unlock()
			return
		}

		if time.Since(game.LastActivity) > 60*time.Second {
			game.Active = false
			// Удаляем кнопки
			emptyComponents := []discordgo.MessageComponent{}
			emptyComponentsPtr := &emptyComponents
			_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
				Channel:    r.floodChannelID,
				ID:         game.GameMessageID,
				Embed:      &discordgo.MessageEmbed{Title: "♠️ Блэкджек", Description: fmt.Sprintf("Игра завершена: <@%s> ушёл из-за стола!", playerID), Color: 0xFFD700},
				Components: emptyComponentsPtr, // Исправлено: передаём указатель на слайс
			})
			if err != nil {
				log.Printf("Failed to remove buttons on timeout: %v", err)
			}
			r.mu.Unlock()
			// Отправляем уведомление в чат
			s.ChannelMessageSend(r.floodChannelID, fmt.Sprintf("♠️ <@%s> ушёл из-за стола и потерял %d кредитов!", playerID, game.Bet))
			log.Printf("Blackjack game for %s timed out", playerID)
			return
		}
		r.mu.Unlock()
	}
}

func (r *Ranking) HandleBlackjackHit(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Исправлено: CustomID берём из MessageComponentData
	playerID := strings.Split(i.MessageComponentData().CustomID, "_")[2]

	r.mu.Lock()
	game, exists := r.blackjackGames[playerID]
	if !exists || !game.Active {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ У тебя нет активной игры в блэкджек! Начни новую с `!blackjack`!",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		r.mu.Unlock()
		return
	}

	// Добавляем карту игроку
	deck := r.generateDeck()
	newCard := deck[len(game.PlayerCards)+len(game.DealerCards)]
	game.PlayerCards = append(game.PlayerCards, newCard)
	game.LastActivity = time.Now()

	playerSum := r.calculateHand(game.PlayerCards)
	embed := &discordgo.MessageEmbed{
		Title: "♠️ Блэкджек",
		Color: 0xFFD700,
	}

	var components []discordgo.MessageComponent
	if playerSum > 21 {
		game.Active = false
		embed.Description = fmt.Sprintf("Ты взял карту: %s\n**Твои карты:** %s (Сумма: %d)\n**Карты дилера:** %s [Скрытая карта]\n\n❌ Перебор! Ты проиграл!", r.cardToString(newCard), r.cardsToString(game.PlayerCards), playerSum, r.cardToString(game.DealerCards[0]))
		// Добавляем кнопку "Сыграть снова"
		components = []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Label:    "Сыграть снова",
						Style:    discordgo.PrimaryButton,
						CustomID: fmt.Sprintf("blackjack_replay_%s", playerID),
					},
				},
			},
		}
	} else {
		embed.Description = fmt.Sprintf("Ты взял карту: %s\n**Твои карты:** %s (Сумма: %d)\n**Карты дилера:** %s [Скрытая карта]", r.cardToString(newCard), r.cardsToString(game.PlayerCards), playerSum, r.cardToString(game.DealerCards[0]))
		components = []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Label:    "Hit",
						Style:    discordgo.PrimaryButton,
						CustomID: fmt.Sprintf("blackjack_hit_%s", playerID),
					},
					discordgo.Button{
						Label:    "Stand",
						Style:    discordgo.SecondaryButton,
						CustomID: fmt.Sprintf("blackjack_stand_%s", playerID),
					},
				},
			},
		}
	}

	// Исправлено: передаём указатель на слайс
	componentsPtr := &components
	r.mu.Unlock()

	// Обновляем сообщение
	_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    i.ChannelID,
		ID:         game.GameMessageID,
		Embed:      embed,
		Components: componentsPtr,
	})
	if err != nil {
		log.Printf("Failed to update Blackjack message: %v", err)
	}

	// Отвечаем на взаимодействие
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})
}

func (r *Ranking) HandleBlackjackStand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Исправлено: CustomID берём из MessageComponentData
	playerID := strings.Split(i.MessageComponentData().CustomID, "_")[2]

	r.mu.Lock()
	game, exists := r.blackjackGames[playerID]
	if !exists || !game.Active {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ У тебя нет активной игры в блэкджек! Начни новую с `!blackjack`!",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		r.mu.Unlock()
		return
	}

	game.LastActivity = time.Now()
	playerSum := r.calculateHand(game.PlayerCards)
	dealerSum := r.calculateHand(game.DealerCards)

	// Дилер добирает карты
	deck := r.generateDeck()
	cardIndex := len(game.PlayerCards) + len(game.DealerCards)
	for dealerSum < 17 && cardIndex < len(deck) {
		game.DealerCards = append(game.DealerCards, deck[cardIndex])
		dealerSum = r.calculateHand(game.DealerCards)
		cardIndex++
	}

	embed := &discordgo.MessageEmbed{
		Title:       "♠️ Блэкджек",
		Description: fmt.Sprintf("**Твои карты:** %s (Сумма: %d)\n**Карты дилера:** %s (Сумма: %d)", r.cardsToString(game.PlayerCards), playerSum, r.cardsToString(game.DealerCards), dealerSum),
		Color:       0xFFD700,
	}

	var result string
	if dealerSum > 21 {
		winnings := game.Bet * 2
		r.UpdateRating(playerID, winnings)
		result = fmt.Sprintf("✅ Дилер перебрал! Ты выиграл %d кредитов!", winnings)
	} else if playerSum > dealerSum {
		winnings := game.Bet * 2
		r.UpdateRating(playerID, winnings)
		result = fmt.Sprintf("✅ Ты выиграл! %d кредитов твои!", winnings)
	} else if playerSum == dealerSum {
		r.UpdateRating(playerID, game.Bet)
		result = "🤝 Ничья! Твоя ставка возвращена."
	} else {
		result = "❌ Дилер победил!"
	}

	embed.Description += fmt.Sprintf("\n\n%s", result)

	// Добавляем кнопку "Сыграть снова"
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Сыграть снова",
					Style:    discordgo.PrimaryButton,
					CustomID: fmt.Sprintf("blackjack_replay_%s", playerID),
				},
			},
		},
	}

	// Исправлено: передаём указатель на слайс
	componentsPtr := &components
	game.Active = false
	r.mu.Unlock()

	// Обновляем сообщение
	_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    i.ChannelID,
		ID:         game.GameMessageID,
		Embed:      embed,
		Components: componentsPtr,
	})
	if err != nil {
		log.Printf("Failed to update Blackjack message: %v", err)
	}

	// Отвечаем на взаимодействие
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})
}

func (r *Ranking) HandleBlackjackReplay(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Исправлено: CustomID берём из MessageComponentData
	playerID := strings.Split(i.MessageComponentData().CustomID, "_")[2]

	r.mu.Lock()
	// Проверяем, не играет ли пользователь уже
	if game, exists := r.blackjackGames[playerID]; exists && game.Active {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Ты уже играешь в блэкджек! Заверши текущую игру или выбери ставку!",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		r.mu.Unlock()
		return
	}

	// Создаём новую игру
	game := &BlackjackGame{
		PlayerID:     playerID,
		Active:       true,
		LastActivity: time.Now(),
	}
	r.blackjackGames[playerID] = game
	r.mu.Unlock()

	// Отправляем меню
	embed := &discordgo.MessageEmbed{
		Title:       "♠️ Казино: Блэкджек",
		Description: fmt.Sprintf("Добро пожаловать, <@%s>!\nСделай ставку, чтобы начать игру.\n\n**Твой баланс:** %d кредитов\n\nНапиши: `!blackjack <сумма>`\nПример: `!blackjack 50`", playerID, r.GetRating(playerID)),
		Color:       0xFFD700,
	}
	msg, err := s.ChannelMessageSendEmbed(i.ChannelID, embed)
	if err != nil {
		log.Printf("Failed to send Blackjack menu: %v", err)
		return
	}

	r.mu.Lock()
	game.MenuMessageID = msg.ID
	r.mu.Unlock()

	// Запускаем таймер неактивности
	go r.blackjackTimeout(s, playerID)

	// Отвечаем на взаимодействие
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})
}

func (r *Ranking) HandleRedBlackReplay(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Исправлено: CustomID берём из MessageComponentData
	playerID := strings.Split(i.MessageComponentData().CustomID, "_")[2]

	r.mu.Lock()
	// Проверяем, не играет ли пользователь уже
	if game, exists := r.redBlackGames[playerID]; exists && game.Active {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Ты уже играешь! Заверши текущую игру или выбери ставку!",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		r.mu.Unlock()
		return
	}

	// Создаём новую игру
	game := &RedBlackGame{
		PlayerID: playerID,
		Active:   true,
	}
	r.redBlackGames[playerID] = game
	r.mu.Unlock()

	// Отправляем меню
	embed := &discordgo.MessageEmbed{
		Title:       "🎰 Казино: Красное-Чёрное",
		Description: fmt.Sprintf("Добро пожаловать, <@%s>!\nВыбери цвет и сделай ставку.\n\n**Твой баланс:** %d кредитов\n\nНапиши: `!redblack <red/black> <сумма>`\nПример: `!redblack red 50`", playerID, r.GetRating(playerID)),
		Color:       0xFFD700,
	}
	msg, err := s.ChannelMessageSendEmbed(i.ChannelID, embed)
	if err != nil {
		log.Printf("Failed to send RedBlack menu: %v", err)
		return
	}

	r.mu.Lock()
	game.MenuMessageID = msg.ID
	r.mu.Unlock()

	// Отвечаем на взаимодействие
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})
}

func (r *Ranking) HandleEndBlackjackCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !endblackjack: %s from %s", command, m.Author.ID)

	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут завершать игры!")
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
	game, exists := r.blackjackGames[targetID]
	if !exists || !game.Active {
		s.ChannelMessageSend(m.ChannelID, "❌ У этого пользователя нет активной игры в блэкджек!")
		r.mu.Unlock()
		return
	}

	game.Active = false
	// Удаляем кнопки
	emptyComponents := []discordgo.MessageComponent{}
	emptyComponentsPtr := &emptyComponents
	_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    m.ChannelID,
		ID:         game.GameMessageID,
		Embed:      &discordgo.MessageEmbed{Title: "♠️ Блэкджек", Description: fmt.Sprintf("Игра завершена админом: <@%s>!", targetID), Color: 0xFFD700},
		Components: emptyComponentsPtr, // Исправлено: передаём указатель на слайс
	})
	if err != nil {
		log.Printf("Failed to remove buttons on admin end: %v", err)
	}
	r.mu.Unlock()

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("♠️ Игра в блэкджек для <@%s> завершена! Ставка не возвращена.", targetID))
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
		s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть положительным числом!")
		return
	}

	senderRating := r.GetRating(m.Author.ID)
	if senderRating < amount {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Недостаточно кредитов! Твой баланс: %d", senderRating))
		return
	}

	r.UpdateRating(m.Author.ID, -amount)
	r.UpdateRating(targetID, amount)

	reason := ""
	if len(parts) > 4 {
		reason = strings.Join(parts[4:], " ")
	}

	response := fmt.Sprintf("✅ <@%s> передал %d кредитов <@%s>!", m.Author.ID, amount, targetID)
	if reason != "" {
		response += fmt.Sprintf(" | Причина: %s", reason)
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
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("💰 Рейтинг <@%s>: %d кредитов", targetID, rating))
	log.Printf("Rating for %s requested by %s: %d", targetID, m.Author.ID, rating)
}

// !china clear coins
func (r *Ranking) HandleClearCoinsCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Processing !china clear coins: %s from %s", m.Content, m.Author.ID)

	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут очищать кредиты!")
		return
	}

	// Очищаем все ключи в Redis
	keys, err := r.redis.Keys(r.ctx, "user:*").Result()
	if err != nil {
		log.Printf("Failed to get user keys from Redis: %v", err)
		s.ChannelMessageSend(m.ChannelID, "❌ Ошибка при очистке кредитов!")
		return
	}

	for _, key := range keys {
		r.redis.Del(r.ctx, key)
	}

	s.ChannelMessageSend(m.ChannelID, "✅ Все кредиты обнулены!")
	log.Printf("All credits cleared by %s", m.Author.ID)
}

// !china gift all <amount>
func (r *Ranking) HandleGiftAllCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !china gift all: %s from %s", command, m.Author.ID)

	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут раздавать кредиты!")
		return
	}

	parts := strings.Fields(command)
	if len(parts) != 4 || parts[1] != "gift" || parts[2] != "all" {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!china gift all <сумма>`")
		return
	}

	amount, err := strconv.Atoi(parts[3])
	if err != nil || amount <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть положительным числом!")
		return
	}

	// Получаем всех пользователей из Redis
	keys, err := r.redis.Keys(r.ctx, "user:*").Result()
	if err != nil {
		log.Printf("Failed to get user keys from Redis: %v", err)
		s.ChannelMessageSend(m.ChannelID, "❌ Ошибка при раздаче кредитов!")
		return
	}

	for _, key := range keys {
		userID := strings.TrimPrefix(key, "user:")
		r.UpdateRating(userID, amount)
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("✅ Каждый получил %d кредитов!", amount))
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
		s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут это делать!")
		return
	}

	targetID := strings.TrimPrefix(parts[2], "<@")
	targetID = strings.TrimSuffix(targetID, ">")
	targetID = strings.TrimPrefix(targetID, "!")

	amount, err := strconv.Atoi(parts[3])
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть числом!")
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
	response := fmt.Sprintf("✅ Админ <@%s> %s рейтинг <@%s> на %d кредитов!", m.Author.ID, verb, targetID, amount)
	if reason != "" {
		response += fmt.Sprintf(" | Причина: %s", reason)
	}
	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("Admin %s changed rating of %s by %d. Reason: %s", m.Author.ID, targetID, amount, reason)
}

func (r *Ranking) HandleHelpCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Processing !chelp: %s from %s", m.Content, m.Author.ID)

	response := "📜 **Команды бота:**\n" +
		"**!cpoll Вопрос [Вариант1] [Вариант2] ...** - (Админ) Создать опрос\n" +
		"**!dep <ID_опроса> <номер_варианта> <сумма>** - Сделать ставку\n" +
		"**!closedep <ID_опроса> <номер>** - (Админ) Закрыть опрос\n" +
		"**!polls** - Показать активные опросы и ставки\n" +
		"**!redblack** - Начать игру в Красное-Чёрное\n" +
		"**!blackjack** - Начать игру в блэкджек\n" +
		"**!endblackjack @id** - (Админ) Завершить игру в блэкджек\n" +
		"**!china give @id <сумма> [причина]** - Передать кредиты\n" +
		"**!china rating @id** - Проверить рейтинг\n" +
		"**!china clear coins** - (Админ) Обнулить кредиты у всех\n" +
		"**!china gift all <сумма>** - (Админ) Раздать кредиты всем\n" +
		"**!admin give @id <сумма> [причина]** - (Админ) Выдать/забрать кредиты\n" +
		"**!chelp** - Показать помощь\n" +
		"**!top5** - Топ-5 по рейтингу"
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
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			r.mu.Lock()
			now := int(time.Now().Unix())
			for userID, lastTime := range r.voiceAct {
				if now-lastTime >= 300 {
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
						r.UpdateRating(userID, 5)
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
