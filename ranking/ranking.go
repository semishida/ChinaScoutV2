package ranking

import (
	"encoding/json"
	"fmt"
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
	Question string            // Вопрос опроса
	Options  []string          // Варианты ответа (например, "Да", "Нет")
	Bets     map[string]int    // Ставки: userID -> сумма ставки
	Choice   map[string]string // Выбор: userID -> выбранный вариант
	Active   bool              // Активен ли опрос
}

type Ranking struct {
	mu       sync.Mutex
	users    map[string]*User // Локальный кэш для ускорения
	admins   map[string]bool
	redis    *RedisClient
	poll     *Poll          // Текущий опрос
	voiceAct map[string]int // Последнее время начисления кредитов (unix timestamp)
}

func NewRanking(adminFilePath, redisAddr string) (*Ranking, error) {
	r := &Ranking{
		users:    make(map[string]*User),
		admins:   make(map[string]bool),
		poll:     &Poll{Active: false},
		voiceAct: make(map[string]int),
	}

	redisClient, err := NewRedisClient(redisAddr)
	if err != nil {
		return nil, err
	}
	r.redis = redisClient

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

	if err := r.LoadAllFromRedis(); err != nil {
		log.Printf("Failed to load users from Redis: %v", err)
	}

	log.Printf("Initialized ranking with %d admins and %d users from Redis", len(r.admins), len(r.users))
	return r, nil
}

func (r *Ranking) LoadAllFromRedis() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	keys, err := r.redis.client.Keys(r.redis.ctx, "*").Result()
	if err != nil {
		return fmt.Errorf("failed to get keys from Redis: %v", err)
	}

	for _, key := range keys {
		user, err := r.redis.LoadUser(key)
		if err != nil {
			log.Printf("Failed to load user %s from Redis: %v", key, err)
			continue
		}
		r.users[user.ID] = user
	}

	return nil
}

func (r *Ranking) PeriodicSave() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		r.mu.Lock()
		for _, user := range r.users {
			if err := r.redis.SaveUser(user); err != nil {
				log.Printf("Failed to save user %s to Redis: %v", user.ID, err)
			}
		}
		r.mu.Unlock()
	}
}

func (r *Ranking) AddUser(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.users[id]; !exists {
		user := &User{ID: id, Rating: 0}
		r.users[id] = user
		if err := r.redis.SaveUser(user); err != nil {
			log.Printf("Failed to save new user %s to Redis: %v", id, err)
		}
	}
}

func (r *Ranking) UpdateRating(id string, points int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	user, err := r.redis.LoadUser(id)
	if err != nil {
		user = &User{ID: id, Rating: 0}
	}
	user.Rating += points
	if user.Rating < 0 {
		user.Rating = 0 // Баланс не может быть отрицательным
	}
	r.users[id] = user
	if err := r.redis.SaveUser(user); err != nil {
		log.Printf("Failed to update rating for user %s in Redis: %v", id, err)
	}
}

func (r *Ranking) GetRating(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	user, err := r.redis.LoadUser(id)
	if err != nil {
		return 0
	}
	r.users[id] = user // Обновляем локальный кэш
	return user.Rating
}

func (r *Ranking) GetTop5() []User {
	r.mu.Lock()
	defer r.mu.Unlock()

	keys, err := r.redis.client.Keys(r.redis.ctx, "*").Result()
	if err != nil {
		log.Printf("Failed to get keys from Redis for top5: %v", err)
		return nil
	}

	users := make([]User, 0, len(keys))
	for _, key := range keys {
		user, err := r.redis.LoadUser(key)
		if err != nil {
			log.Printf("Failed to load user %s for top5: %v", key, err)
			continue
		}
		r.users[user.ID] = user
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
	_, isAdmin := r.admins[userID]
	return isAdmin
}

func (r *Ranking) HandleChinaCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Received !china command from %s: %s", m.Author.ID, command)

	userID := strings.TrimPrefix(m.Author.ID, "<@")
	userID = strings.TrimSuffix(userID, ">")
	userID = strings.TrimPrefix(userID, "!")

	parts := strings.Fields(command)
	if len(parts) < 3 {
		if _, err := s.ChannelMessageSend(m.ChannelID, "❌ Глупый Китайский брат! Используй правильно: !china @id +10 [причина]"); err != nil {
			log.Printf("Failed to send usage message to %s: %v", userID, err)
		}
		log.Printf("Invalid !china command format from %s", userID)
		return
	}

	targetID := strings.TrimPrefix(parts[1], "<@")
	targetID = strings.TrimSuffix(targetID, ">")
	targetID = strings.TrimPrefix(targetID, "!")

	points, err := strconv.Atoi(parts[2])
	if err != nil || points <= 0 {
		if _, err := s.ChannelMessageSend(m.ChannelID, "❌ Глупый Китайский брат! Укажи положительное число кредитов!"); err != nil {
			log.Printf("Failed to send invalid points message: %v", err)
		}
		log.Printf("Invalid points format in !china from %s: %s", userID, parts[2])
		return
	}

	senderRating := r.GetRating(userID)
	if senderRating < points {
		if _, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ У тебя недостаточно кредитов! Твой баланс: %d", senderRating)); err != nil {
			log.Printf("Failed to send insufficient balance message: %v", err)
		}
		log.Printf("User %s has insufficient balance for transfer: %d < %d", userID, senderRating, points)
		return
	}

	r.UpdateRating(userID, -points)  // Снимаем с отправителя
	r.UpdateRating(targetID, points) // Начисляем получателю

	reason := ""
	if len(parts) > 3 {
		reason = strings.Join(parts[3:], " ")
	}

	response := fmt.Sprintf("✅ <@%s> передал %d кредитов пользователю <@%s>.", userID, points, targetID)
	if reason != "" {
		response += fmt.Sprintf(" По причине: %s", reason)
	}
	if _, err := s.ChannelMessageSend(m.ChannelID, response); err != nil {
		log.Printf("Failed to send transfer confirmation: %v", err)
	}
	log.Printf("User %s transferred %d credits to %s. Reason: %s", userID, points, targetID, reason)
}

func (r *Ranking) HandleChinaCommandAdmin(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Received !china adm command from %s: %s", m.Author.ID, command)

	userID := strings.TrimPrefix(m.Author.ID, "<@")
	userID = strings.TrimSuffix(userID, ">")
	userID = strings.TrimPrefix(userID, "!")

	if !r.IsAdmin(userID) {
		if _, err := s.ChannelMessageSend(m.ChannelID, "❌ Глупый Китайский мальчик хочет использовать привилегии Китай-Партии!"); err != nil {
			log.Printf("Failed to send admin rejection to %s: %v", userID, err)
		}
		log.Printf("User %s is not an admin, adm command rejected", userID)
		return
	}

	parts := strings.Fields(command)
	if len(parts) < 4 {
		if _, err := s.ChannelMessageSend(m.ChannelID, "❌ Используй правильно: !china adm @id +1000 [причина]"); err != nil {
			log.Printf("Failed to send adm usage message to %s: %v", userID, err)
		}
		log.Printf("Invalid !china adm format from %s", userID)
		return
	}

	targetID := strings.TrimPrefix(parts[2], "<@")
	targetID = strings.TrimSuffix(targetID, ">")
	targetID = strings.TrimPrefix(targetID, "!")

	points, err := strconv.Atoi(parts[3])
	if err != nil || points <= 0 {
		if _, err := s.ChannelMessageSend(m.ChannelID, "❌ Укажи положительное число кредитов!"); err != nil {
			log.Printf("Failed to send invalid points message: %v", err)
		}
		log.Printf("Invalid points format in !china adm from %s: %s", userID, parts[3])
		return
	}

	r.UpdateRating(targetID, points) // Начисляем без снятия с админа

	reason := ""
	if len(parts) > 4 {
		reason = strings.Join(parts[4:], " ")
	}

	response := fmt.Sprintf("✅ Админ <@%s> выдал %d кредитов пользователю <@%s>.", userID, points, targetID)
	if reason != "" {
		response += fmt.Sprintf(" По причине: %s", reason)
	}
	if _, err := s.ChannelMessageSend(m.ChannelID, response); err != nil {
		log.Printf("Failed to send admin transfer confirmation: %v", err)
	}
	log.Printf("Admin %s issued %d credits to %s. Reason: %s", userID, points, targetID, reason)
}

func (r *Ranking) HandleDepCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Received !dep command from %s: %s", m.Author.ID, command)

	// Разбиваем команду вручную, чтобы корректно обрабатывать кавычки
	parts := splitCommand(command)
	if len(parts) < 2 {
		if _, err := s.ChannelMessageSend(m.ChannelID, "❌ Используй правильно: !dep <сумма> <вариант> или !dep poll \"Тема\" \"Вариант1\" \"Вариант2\""); err != nil {
			log.Printf("Failed to send dep usage message: %v", err)
		}
		log.Printf("Invalid !dep command format from %s", m.Author.ID)
		return
	}

	// Создание опроса: !dep poll "Тема" "Вариант1" "Вариант2"
	if parts[1] == "poll" && len(parts) >= 5 {
		if !r.IsAdmin(m.Author.ID) {
			if _, err := s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут создавать опросы!"); err != nil {
				log.Printf("Failed to send admin rejection for poll: %v", err)
			}
			log.Printf("User %s is not an admin, poll creation rejected", m.Author.ID)
			return
		}

		question := strings.Trim(parts[2], "\"")
		options := parts[3:]
		for i, opt := range options {
			options[i] = strings.Trim(opt, "\"")
		}

		r.mu.Lock()
		r.poll = &Poll{
			Question: question,
			Options:  options,
			Bets:     make(map[string]int),
			Choice:   make(map[string]string),
			Active:   true,
		}
		r.mu.Unlock()

		response := fmt.Sprintf("🎉 **Новый опрос запущен!**\nВопрос: \"%s\"\nВарианты ставок:\n", question)
		for i, opt := range options {
			response += fmt.Sprintf("%d. %s\n", i+1, opt)
		}
		response += "Делайте ставки командой: `!dep <сумма> <вариант>`"
		if _, err := s.ChannelMessageSend(m.ChannelID, response); err != nil {
			log.Printf("Failed to send poll creation message: %v", err)
		}
		log.Printf("Poll created by %s: %s with options %v", m.Author.ID, question, options)
		return
	}

	// Завершение опроса: !dep depres "выигравший_вариант"
	if parts[1] == "depres" && len(parts) >= 3 {
		if !r.IsAdmin(m.Author.ID) {
			if _, err := s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут завершать опросы!"); err != nil {
				log.Printf("Failed to send admin rejection for depres: %v", err)
			}
			log.Printf("User %s is not an admin, depres rejected", m.Author.ID)
			return
		}

		r.mu.Lock()
		if !r.poll.Active {
			if _, err := s.ChannelMessageSend(m.ChannelID, "❌ Нет активного опроса для завершения!"); err != nil {
				log.Printf("Failed to send no active poll message: %v", err)
			}
			log.Printf("No active poll to resolve by %s", m.Author.ID)
			r.mu.Unlock()
			return
		}

		winningOption := strings.Trim(parts[2], "\"")
		winningOptionLower := strings.ToLower(winningOption)
		validOption := false
		for _, opt := range r.poll.Options {
			if strings.ToLower(opt) == winningOptionLower {
				validOption = true
				winningOption = opt
				break
			}
		}
		if !validOption {
			if _, err := s.ChannelMessageSend(m.ChannelID, "❌ Неверный вариант результата! Доступные варианты: "+strings.Join(r.poll.Options, ", ")); err != nil {
				log.Printf("Failed to send invalid option message: %v", err)
			}
			log.Printf("Invalid winning option '%s' by %s", winningOption, m.Author.ID)
			r.mu.Unlock()
			return
		}

		totalBet := 0
		winnersBet := 0
		for _, bet := range r.poll.Bets {
			totalBet += bet
		}
		for userID, choice := range r.poll.Choice {
			if strings.ToLower(choice) == winningOptionLower {
				winnersBet += r.poll.Bets[userID]
			}
		}

		var coefficient float64
		if winnersBet == 0 {
			coefficient = 0
		} else {
			coefficient = float64(totalBet) / float64(winnersBet)
		}

		for userID, choice := range r.poll.Choice {
			if strings.ToLower(choice) == winningOptionLower {
				winnings := int(float64(r.poll.Bets[userID]) * coefficient)
				r.UpdateRating(userID, winnings)
				if _, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🏆 <@%s> выиграл %d кредитов (ставка: %d, коэффициент: %.2f)", userID, winnings, r.poll.Bets[userID], coefficient)); err != nil {
					log.Printf("Failed to send winnings message for %s: %v", userID, err)
				}
			}
		}

		r.poll.Active = false
		r.mu.Unlock()
		if _, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("✅ Опрос завершён! Победил вариант: **%s**. Итоговый коэффициент: %.2f", winningOption, coefficient)); err != nil {
			log.Printf("Failed to send poll resolved message: %v", err)
		}
		log.Printf("Poll resolved by %s: %s won with coefficient %.2f", m.Author.ID, winningOption, coefficient)
		return
	}

	// Ставка: !dep <сумма> <вариант>
	if len(parts) < 3 {
		if _, err := s.ChannelMessageSend(m.ChannelID, "❌ Используй правильно: !dep <сумма> <вариант>"); err != nil {
			log.Printf("Failed to send dep usage message: %v", err)
		}
		log.Printf("Invalid !dep command format from %s", m.Author.ID)
		return
	}

	amount, err := strconv.Atoi(parts[1])
	if err != nil || amount <= 0 {
		if _, err := s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть положительным числом!"); err != nil {
			log.Printf("Failed to send invalid amount message: %v", err)
		}
		log.Printf("Invalid amount in !dep from %s: %s", m.Author.ID, parts[1])
		return
	}

	choice := strings.Join(parts[2:], " ")
	choice = strings.Trim(choice, "\"") // Убираем кавычки, если они есть
	choiceLower := strings.ToLower(choice)

	r.mu.Lock()
	if !r.poll.Active {
		if _, err := s.ChannelMessageSend(m.ChannelID, "❌ Нет активного опроса для ставок!"); err != nil {
			log.Printf("Failed to send no active poll message: %v", err)
		}
		log.Printf("No active poll for bet by %s", m.Author.ID)
		r.mu.Unlock()
		return
	}

	validChoice := false
	for _, opt := range r.poll.Options {
		if strings.ToLower(opt) == choiceLower {
			choice = opt // Сохраняем оригинальный регистр для вывода
			validChoice = true
			break
		}
	}
	if !validChoice {
		if _, err := s.ChannelMessageSend(m.ChannelID, "❌ Неверный вариант опроса! Доступные варианты: "+strings.Join(r.poll.Options, ", ")); err != nil {
			log.Printf("Failed to send invalid option message: %v", err)
		}
		log.Printf("Invalid choice '%s' in !dep by %s", choice, m.Author.ID)
		r.mu.Unlock()
		return
	}

	userRating := r.GetRating(m.Author.ID)
	if userRating < amount {
		if _, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Недостаточно кредитов для ставки! Твой баланс: %d", userRating)); err != nil {
			log.Printf("Failed to send insufficient balance message: %v", err)
		}
		log.Printf("User %s has insufficient balance for bet: %d < %d", m.Author.ID, userRating, amount)
		r.mu.Unlock()
		return
	}

	totalBet := 0
	for _, bet := range r.poll.Bets {
		totalBet += bet
	}
	totalBet += amount
	choiceBet := 0
	for userID, ch := range r.poll.Choice {
		if strings.ToLower(ch) == choiceLower {
			choiceBet += r.poll.Bets[userID]
		}
	}
	coefficient := float64(totalBet) / float64(choiceBet+amount)
	if choiceBet == 0 {
		coefficient = float64(totalBet)
	}

	r.UpdateRating(m.Author.ID, -amount)
	r.poll.Bets[m.Author.ID] = amount
	r.poll.Choice[m.Author.ID] = choice
	r.mu.Unlock()

	if _, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🎲 <@%s> поставил %d кредитов на \"%s\" с коэффициентом %.2f!", m.Author.ID, amount, choice, coefficient)); err != nil {
		log.Printf("Failed to send bet confirmation: %v", err)
	}
	log.Printf("User %s placed a bet of %d credits on %s with coefficient %.2f", m.Author.ID, amount, choice, coefficient)
}

// Вспомогательная функция для корректного разбиения команды с кавычками
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
				if now-lastTime >= 60 { // Прошла минута с последнего начисления
					guilds, err := s.UserGuilds(100, "", "", false)
					if err != nil {
						log.Printf("Error tracking voice activity for %s: failed to get guilds: %v", userID, err)
						continue
					}

					inChannel := false
					for _, guild := range guilds {
						guildState, err := s.State.Guild(guild.ID)
						if err != nil {
							log.Printf("Error tracking voice activity for %s: failed to get guild state %s: %v", userID, guild.ID, err)
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
						log.Printf("User %s received 1 credit for voice activity.", userID)
					} else {
						delete(r.voiceAct, userID)
						log.Printf("User %s stopped voice activity and was removed from tracking.", userID)
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
			r.AddUser(v.UserID)
			if _, exists := r.voiceAct[v.UserID]; !exists {
				r.voiceAct[v.UserID] = int(time.Now().Unix())
				log.Printf("User %s started voice activity.", v.UserID)
			}
		} else {
			delete(r.voiceAct, v.UserID)
			log.Printf("User %s stopped voice activity.", v.UserID)
		}
		r.mu.Unlock()
	})
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if strings.ToLower(s) == strings.ToLower(item) {
			return true
		}
	}
	return false
}
