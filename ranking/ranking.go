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

type Ranking struct {
	mu       sync.Mutex
	admins   map[string]bool  // Список админов
	polls    map[string]*Poll // Активные опросы
	redis    *redis.Client    // Клиент Redis
	ctx      context.Context  // Контекст для Redis
	voiceAct map[string]int   // Время последней активности в голосе
}

func NewRanking(adminFilePath, redisAddr string) (*Ranking, error) {
	r := &Ranking{
		admins:   make(map[string]bool),
		polls:    make(map[string]*Poll),
		voiceAct: make(map[string]int),
		ctx:      context.Background(),
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

	r.UpdateRating(m.Author.ID, -amount)
	poll.Bets[m.Author.ID] = amount
	poll.Choices[m.Author.ID] = option
	r.mu.Unlock()

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🎲 <@%s> поставил %d кредитов на [%s] в опросе %s", m.Author.ID, amount, poll.Options[option-1], poll.Question))
	log.Printf("User %s bet %d on option %d in poll %s", m.Author.ID, amount, option, pollID)
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
		"**!china give @id <сумма> [причина]** - Передать кредиты\n" +
		"**!china rating @id** - Проверить рейтинг\n" +
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
