package ranking

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/redis/go-redis/v9"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type User struct {
	ID     string `json:"id"`
	Rating int    `json:"rating"`
}

type Poll struct {
	ID       int            // Уникальный номер опроса
	Question string         // Вопрос опроса
	Options  []string       // Варианты ответа
	Bets     map[string]int // Ставки: userID -> сумма ставки
	Choices  map[string]int // Выбор: userID -> номер варианта (1, 2, ...)
	Active   bool           // Активен ли опрос
	Created  time.Time      // Время создания
}

type Ranking struct {
	mu       sync.Mutex
	users    map[string]*User // Локальный кэш пользователей
	admins   map[string]bool  // Список админов
	polls    map[int]*Poll    // Активные опросы
	pollSeq  int              // Счётчик для ID опросов
	redis    *redis.Client    // Клиент Redis
	ctx      context.Context  // Контекст для Redis
	voiceAct map[string]int   // Время последней активности в голосе
}

func NewRanking(adminFilePath, redisAddr string) (*Ranking, error) {
	r := &Ranking{
		users:    make(map[string]*User),
		admins:   make(map[string]bool),
		polls:    make(map[int]*Poll),
		pollSeq:  0,
		voiceAct: make(map[string]int),
		ctx:      context.Background(),
	}

	// Инициализация Redis
	r.redis = redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})
	if _, err := r.redis.Ping(r.ctx).Result(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %v", err)
	}

	// Загрузка админов
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

	// Загрузка пользователей из Redis
	if err := r.loadUsersFromRedis(); err != nil {
		log.Printf("Failed to load users from Redis: %v", err)
	}

	log.Printf("Initialized ranking with %d admins", len(r.admins))
	go r.PeriodicSave()
	return r, nil
}

func (r *Ranking) loadUsersFromRedis() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	keys, err := r.redis.Keys(r.ctx, "user:*").Result()
	if err != nil {
		return fmt.Errorf("failed to get user keys from Redis: %v", err)
	}

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
		r.users[user.ID] = &user
	}
	return nil
}

func (r *Ranking) PeriodicSave() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		r.mu.Lock()
		for _, user := range r.users {
			data, err := json.Marshal(user)
			if err != nil {
				log.Printf("Failed to marshal user %s: %v", user.ID, err)
				continue
			}
			if err := r.redis.Set(r.ctx, "user:"+user.ID, data, 0).Err(); err != nil {
				log.Printf("Failed to save user %s to Redis: %v", user.ID, err)
			}
		}
		r.mu.Unlock()
	}
}

func (r *Ranking) GetRating(userID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	if user, exists := r.users[userID]; exists {
		return user.Rating
	}
	return 0
}

func (r *Ranking) UpdateRating(userID string, points int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	user, exists := r.users[userID]
	if !exists {
		user = &User{ID: userID, Rating: 0}
		r.users[userID] = user
	}
	user.Rating += points
	if user.Rating < 0 {
		user.Rating = 0
	}
}

func (r *Ranking) GetTop5() []User {
	r.mu.Lock()
	defer r.mu.Unlock()

	users := make([]User, 0, len(r.users))
	for _, user := range r.users {
		users = append(users, *user)
	}

	sort.Slice(users, func(i, j int) bool {
		return users[i].Rating > users[j].Rating
	})

	if len(users) > 5 {
		return users[:5]
	}
	return users
}

func (r *Ranking) IsAdmin(userID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	isAdmin := r.admins[userID]
	log.Printf("Checking if %s is admin: %v", userID, isAdmin)
	return isAdmin
}

// !poll "Вопрос" "Вариант1" "Вариант2" ...
func (r *Ranking) HandlePollCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !poll: %s from %s", command, m.Author.ID)

	parts := splitCommand(command)
	if len(parts) < 4 {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!poll \"Вопрос\" \"Вариант1\" \"Вариант2\" ...`")
		return
	}

	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут создавать опросы!")
		return
	}

	question := strings.Trim(parts[1], "\"")
	options := parts[2:]
	for i, opt := range options {
		options[i] = strings.Trim(opt, "\"")
	}

	r.mu.Lock()
	r.pollSeq++
	pollID := r.pollSeq
	r.polls[pollID] = &Poll{
		ID:       pollID,
		Question: question,
		Options:  options,
		Bets:     make(map[string]int),
		Choices:  make(map[string]int),
		Active:   true,
		Created:  time.Now(),
	}
	r.mu.Unlock()

	response := fmt.Sprintf("🎉 **Опрос #%d запущен!**\n**Вопрос:** \"%s\"\n**Варианты:**\n", pollID, question)
	for i, opt := range options {
		response += fmt.Sprintf("%d. %s\n", i+1, opt)
	}
	response += "Ставьте: `!dep <номер_варианта> <сумма>`\nЗакрытие: `!poll #%d close <номер>`"
	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("Poll #%d created by %s: %s with options %v", pollID, m.Author.ID, question, options)
}

// !poll #<id> close <winning_option>
func (r *Ranking) HandlePollClose(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !poll close: %s from %s", command, m.Author.ID)

	parts := strings.Fields(command)
	if len(parts) < 4 || parts[2] != "close" {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!poll #<id> close <номер_варианта>`")
		return
	}

	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут закрывать опросы!")
		return
	}

	pollID, err := strconv.Atoi(strings.TrimPrefix(parts[1], "#"))
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Неверный ID опроса!")
		return
	}

	winningOption, err := strconv.Atoi(parts[3])
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Неверный номер варианта!")
		return
	}

	r.mu.Lock()
	poll, exists := r.polls[pollID]
	if !exists || !poll.Active {
		s.ChannelMessageSend(m.ChannelID, "❌ Опрос не найден или уже закрыт!")
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

	response := fmt.Sprintf("✅ **Опрос #%d завершён!**\nПобедил вариант: **%s** (№%d)\nКоэффициент: %.2f\n**Победители:**\n", pollID, poll.Options[winningOption-1], winningOption, coefficient)
	for userID, choice := range poll.Choices {
		if choice == winningOption {
			winnings := int(float64(poll.Bets[userID]) * coefficient)
			r.UpdateRating(userID, winnings)
			response += fmt.Sprintf("<@%s>: %d кредитов (ставка: %d)\n", userID, winnings, poll.Bets[userID])
		}
	}
	if winnersBet == 0 {
		response += "Никто не выиграл :("
	}

	poll.Active = false
	r.mu.Unlock()

	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("Poll #%d closed by %s, winner: %s, coefficient: %.2f", pollID, m.Author.ID, poll.Options[winningOption-1], coefficient)
}

// !dep <option_number> <amount>
func (r *Ranking) HandleDepCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !dep: %s from %s", command, m.Author.ID)

	parts := strings.Fields(command)
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!dep <номер_варианта> <сумма>`")
		return
	}

	option, err := strconv.Atoi(parts[1])
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Номер варианта должен быть числом!")
		return
	}

	amount, err := strconv.Atoi(parts[2])
	if err != nil || amount <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть положительным числом!")
		return
	}

	r.mu.Lock()
	var activePoll *Poll
	for _, poll := range r.polls {
		if poll.Active {
			activePoll = poll
			break
		}
	}
	if activePoll == nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Нет активных опросов!")
		r.mu.Unlock()
		return
	}

	if option < 1 || option > len(activePoll.Options) {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Номер варианта должен быть от 1 до %d!", len(activePoll.Options)))
		r.mu.Unlock()
		return
	}

	userRating := r.GetRating(m.Author.ID)
	if userRating < amount {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Недостаточно кредитов! Твой баланс: %d", userRating))
		r.mu.Unlock()
		return
	}

	r.UpdateRating(m.Author.ID, -amount)
	activePoll.Bets[m.Author.ID] = amount
	activePoll.Choices[m.Author.ID] = option
	r.mu.Unlock()

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🎲 <@%s> поставил %d кредитов на \"%s\" (Опрос #%d)", m.Author.ID, amount, activePoll.Options[option-1], activePoll.ID))
	log.Printf("User %s bet %d on option %d in poll #%d", m.Author.ID, amount, option, activePoll.ID)
}

// !admin give @id <amount> [reason]
func (r *Ranking) HandleAdminCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !admin: %s from %s", command, m.Author.ID)

	parts := strings.Fields(command)
	if len(parts) < 4 || parts[1] != "give" {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!admin give @id <сумма> [причина]`")
		return
	}

	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут выдавать кредиты!")
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

	r.UpdateRating(targetID, amount)
	reason := ""
	if len(parts) > 4 {
		reason = strings.Join(parts[4:], " ")
	}

	response := fmt.Sprintf("✅ Админ <@%s> выдал %d кредитов <@%s>", m.Author.ID, amount, targetID)
	if reason != "" {
		response += fmt.Sprintf(" | Причина: %s", reason)
	}
	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("Admin %s gave %d credits to %s. Reason: %s", m.Author.ID, amount, targetID, reason)
}

// !china give @id <amount> [reason]
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

	response := fmt.Sprintf("✅ <@%s> передал %d кредитов <@%s>", m.Author.ID, amount, targetID)
	if reason != "" {
		response += fmt.Sprintf(" | Причина: %s", reason)
	}
	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("User %s gave %d credits to %s. Reason: %s", m.Author.ID, amount, targetID, reason)
}

// !china rating @id
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
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("💰 <@%s>: %d кредитов", targetID, rating))
	log.Printf("Rating for %s requested by %s: %d", targetID, m.Author.ID, rating)
}

// !china help
func (r *Ranking) HandleChinaHelp(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Processing !china help: %s from %s", command, m.Author.ID)

	parts := strings.Fields(command)
	if len(parts) < 2 || parts[1] != "help" {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!china help`")
		return
	}

	response := "📜 **Команды бота:**\n" +
		"**!poll \"Вопрос\" \"Вариант1\" \"Вариант2\" ...** - (Админ) Создать опрос\n" +
		"**!poll #<id> close <номер>** - (Админ) Закрыть опрос\n" +
		"**!dep <номер_варианта> <сумма>** - Сделать ставку\n" +
		"**!admin give @id <сумма> [причина]** - (Админ) Выдать кредиты\n" +
		"**!china give @id <сумма> [причина]** - Передать свои кредиты\n" +
		"**!china rating @id** - Проверить рейтинг\n" +
		"**!china help** - Показать помощь\n" +
		"**!top5** - Топ-5 игроков"
	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("Help sent to %s", m.Author.ID)
}

func splitCommand(command string) []string {
	var parts []string
	var current string
	inQuotes := false

	for _, char := range command {
		if char == '"' {
			inQuotes = !inQuotes
			continue
		}
		if char == ' ' && !inQuotes {
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
							if vs.UserID == userID {
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
						log.Printf("User %s earned 1 credit for voice activity", userID)
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
			r.UpdateRating(v.UserID, 0) // Инициализация пользователя
			r.voiceAct[v.UserID] = int(time.Now().Unix())
		} else {
			delete(r.voiceAct, v.UserID)
		}
		r.mu.Unlock()
	})
}
