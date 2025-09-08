package ranking

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/go-redis/redis/v8"
)

// CinemaOption представляет вариант фильма на аукционе.
type CinemaOption struct {
	Name  string         `json:"name"`
	Total int            `json:"total"`
	Bets  map[string]int `json:"bets"` // userID: amount
}

// PendingCinemaBid представляет pending ставку для подтверждения.
type PendingCinemaBid struct {
	UserID string
	IsNew  bool
	Name   string // для новых
	Index  int    // для существующих (0-based)
	Amount int
}

// LoadCinemaOptions загружает варианты аукциона из Redis.
func (r *Ranking) LoadCinemaOptions() {
	data, err := r.redis.Get(r.ctx, "cinema:options").Result()
	if err == redis.Nil {
		r.cinemaOptions = []CinemaOption{}
		return
	} else if err != nil {
		log.Printf("Не удалось загрузить cinema options из Redis: %v", err)
		r.cinemaOptions = []CinemaOption{}
		return
	}
	if err := json.Unmarshal([]byte(data), &r.cinemaOptions); err != nil {
		log.Printf("Не удалось разобрать cinema options: %v", err)
		r.cinemaOptions = []CinemaOption{}
	}
}

// SaveCinemaOptions сохраняет варианты аукциона в Redis.
func (r *Ranking) SaveCinemaOptions() {
	dataBytes, err := json.Marshal(r.cinemaOptions)
	if err != nil {
		log.Printf("Не удалось сериализовать cinema options: %v", err)
		return
	}
	if err := r.redis.Set(r.ctx, "cinema:options", dataBytes, 0).Err(); err != nil {
		log.Printf("Не удалось сохранить cinema options в Redis: %v", err)
	}
}

// HandleMessageReactionAdd обрабатывает реакции на сообщения подтверждения ставок.
func (r *Ranking) HandleMessageReactionAdd(s *discordgo.Session, rea *discordgo.MessageReactionAdd) {
	if rea.UserID == s.State.User.ID {
		return
	}
	if rea.Emoji.Name != "✅" && rea.Emoji.Name != "❌" {
		return
	}

	r.mu.Lock()
	bid, ok := r.pendingCinemaBids[rea.MessageID]
	r.mu.Unlock()
	if !ok {
		return
	}
	if rea.UserID != bid.UserID {
		return
	}

	if rea.Emoji.Name == "❌" {
		r.mu.Lock()
		delete(r.pendingCinemaBids, rea.MessageID)
		r.mu.Unlock()
		s.ChannelMessageEdit(rea.ChannelID, rea.MessageID, "Ставка отменена.")
		return
	}

	// Подтверждение ✅
	currentRating := r.GetRating(bid.UserID)
	if currentRating < bid.Amount {
		s.ChannelMessageEdit(rea.ChannelID, rea.MessageID, "Теперь недостаточно кредитов!")
		r.mu.Lock()
		delete(r.pendingCinemaBids, rea.MessageID)
		r.mu.Unlock()
		return
	}

	r.UpdateRating(bid.UserID, -bid.Amount)

	r.mu.Lock()
	var msg string
	if bid.IsNew {
		option := CinemaOption{
			Name:  bid.Name,
			Total: bid.Amount,
			Bets:  map[string]int{bid.UserID: bid.Amount},
		}
		r.cinemaOptions = append(r.cinemaOptions, option)
		msg = fmt.Sprintf("<@%s> сделал ставку на аукцион \"%s\" %d", bid.UserID, bid.Name, bid.Amount)
	} else {
		if bid.Index < 0 || bid.Index >= len(r.cinemaOptions) {
			r.mu.Unlock()
			s.ChannelMessageEdit(rea.ChannelID, rea.MessageID, "Ошибка: вариант не найден.")
			return
		}
		option := &r.cinemaOptions[bid.Index]
		option.Bets[bid.UserID] = option.Bets[bid.UserID] + bid.Amount
		option.Total += bid.Amount
		msg = fmt.Sprintf("<@%s> добавил ставку %d на \"%s\" (total %d)", bid.UserID, bid.Amount, option.Name, option.Total)
	}
	r.SaveCinemaOptions()
	delete(r.pendingCinemaBids, rea.MessageID)
	r.mu.Unlock()

	_, err := s.ChannelMessageSend(r.cinemaChannelID, msg)
	if err != nil {
		log.Printf("Failed to send to cinema channel: %v", err)
	}
	s.ChannelMessageEdit(rea.ChannelID, rea.MessageID, "Ставка подтверждена и обработана.")
}

// HandleCinemaCommand обрабатывает !cinema <название> <сумма>.
func (r *Ranking) HandleCinemaCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Обработка !cinema: %s от %s", command, m.Author.ID)
	parts := strings.Fields(command)
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "Используй: !cinema <название> <сумма>")
		return
	}
	amountStr := parts[len(parts)-1]
	amount, err := strconv.Atoi(amountStr)
	if err != nil || amount <= 0 {
		s.ChannelMessageSend(m.ChannelID, "Сумма должна быть положительным числом!")
		return
	}
	name := strings.Join(parts[1:len(parts)-1], " ")
	if len(name) > 255 {
		s.ChannelMessageSend(m.ChannelID, "Название слишком длинное (max 255 символов)!")
		return
	}

	userRating := r.GetRating(m.Author.ID)
	if userRating < amount {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Недостаточно кредитов! Твой баланс: %d", userRating))
		return
	}

	confirmText := fmt.Sprintf("Отправить админу на киноаук вариант \"%s\" со ставкой %d?", name, amount)
	confirmMsg, err := s.ChannelMessageSend(m.ChannelID, confirmText)
	if err != nil {
		log.Printf("Не удалось отправить подтверждение: %v", err)
		return
	}

	err = s.MessageReactionAdd(confirmMsg.ChannelID, confirmMsg.ID, "✅")
	if err != nil {
		log.Printf("Failed to add ✅: %v", err)
	}
	err = s.MessageReactionAdd(confirmMsg.ChannelID, confirmMsg.ID, "❌")
	if err != nil {
		log.Printf("Failed to add ❌: %v", err)
	}

	r.mu.Lock()
	r.pendingCinemaBids[confirmMsg.ID] = PendingCinemaBid{
		UserID: m.Author.ID,
		IsNew:  true,
		Name:   name,
		Amount: amount,
	}
	r.mu.Unlock()
}

// HandleBetCinemaCommand обрабатывает !betcinema <номер> <сумма>.
func (r *Ranking) HandleBetCinemaCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Обработка !betcinema: %s от %s", command, m.Author.ID)
	parts := strings.Fields(command)
	if len(parts) != 3 {
		s.ChannelMessageSend(m.ChannelID, "Используй: !betcinema <номер> <сумма>")
		return
	}
	indexStr := parts[1]
	amountStr := parts[2]
	index, err := strconv.Atoi(indexStr)
	if err != nil || index <= 0 {
		s.ChannelMessageSend(m.ChannelID, "Неверный номер!")
		return
	}
	index-- // 0-based
	amount, err := strconv.Atoi(amountStr)
	if err != nil || amount <= 0 {
		s.ChannelMessageSend(m.ChannelID, "Сумма должна быть положительным числом!")
		return
	}

	r.mu.Lock()
	if index >= len(r.cinemaOptions) {
		r.mu.Unlock()
		s.ChannelMessageSend(m.ChannelID, "Неверный номер варианта!")
		return
	}
	name := r.cinemaOptions[index].Name
	r.mu.Unlock()

	userRating := r.GetRating(m.Author.ID)
	if userRating < amount {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Недостаточно кредитов! Твой баланс: %d", userRating))
		return
	}

	confirmText := fmt.Sprintf("Добавить ставку %d на вариант \"%s\"?", amount, name)
	confirmMsg, err := s.ChannelMessageSend(m.ChannelID, confirmText)
	if err != nil {
		log.Printf("Не удалось отправить подтверждение: %v", err)
		return
	}

	err = s.MessageReactionAdd(confirmMsg.ChannelID, confirmMsg.ID, "✅")
	if err != nil {
		log.Printf("Failed to add ✅: %v", err)
	}
	err = s.MessageReactionAdd(confirmMsg.ChannelID, confirmMsg.ID, "❌")
	if err != nil {
		log.Printf("Failed to add ❌: %v", err)
	}

	r.mu.Lock()
	r.pendingCinemaBids[confirmMsg.ID] = PendingCinemaBid{
		UserID: m.Author.ID,
		IsNew:  false,
		Index:  index,
		Amount: amount,
	}
	r.mu.Unlock()
}

// HandleCinemaListCommand обрабатывает !cinemalist (для всех).
func (r *Ranking) HandleCinemaListCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Обработка !cinemalist от %s", m.Author.ID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cinemaOptions) == 0 {
		s.ChannelMessageSend(m.ChannelID, "Пока нет вариантов на аукционе! 😢")
		return
	}
	response := "Актуальные варианты киноаукциона:\n"
	for i, opt := range r.cinemaOptions {
		response += fmt.Sprintf("%d. %s — %d кредитов\n", i+1, opt.Name, opt.Total)
	}
	s.ChannelMessageSend(m.ChannelID, response)
}

// HandleAdminCinemaListCommand обрабатывает !admincinemalist (детальный для админов).
func (r *Ranking) HandleAdminCinemaListCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Обработка !admincinemalist от %s", m.Author.ID)
	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут просматривать детальный список! 🔒")
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cinemaOptions) == 0 {
		s.ChannelMessageSend(m.ChannelID, "Пока нет вариантов на аукционе! 😢")
		return
	}
	response := "Детальный список вариантов киноаукциона:\n"
	for i, opt := range r.cinemaOptions {
		response += fmt.Sprintf("%d. %s — %d кредитов\n", i+1, opt.Name, opt.Total)
		for userID, amt := range opt.Bets {
			response += fmt.Sprintf("  - <@%s>: %d\n", userID, amt)
		}
	}
	s.ChannelMessageSend(m.ChannelID, response)
}

// HandleRemoveLowestCommand обрабатывает !removelowest <число>.
func (r *Ranking) HandleRemoveLowestCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Обработка !removelowest: %s от %s", command, m.Author.ID)
	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут удалять варианты! 🔒")
		return
	}
	parts := strings.Fields(command)
	if len(parts) != 2 {
		s.ChannelMessageSend(m.ChannelID, "Используй: !removelowest <число>")
		return
	}
	num, err := strconv.Atoi(parts[1])
	if err != nil || num <= 0 {
		s.ChannelMessageSend(m.ChannelID, "Число должно быть положительным!")
		return
	}

	r.mu.Lock()
	if num >= len(r.cinemaOptions) {
		r.mu.Unlock()
		s.ChannelMessageSend(m.ChannelID, "Слишком много — нельзя удалить все!")
		return
	}

	// Сортируем по total ascending
	sort.Slice(r.cinemaOptions, func(i, j int) bool {
		return r.cinemaOptions[i].Total < r.cinemaOptions[j].Total
	})

	removed := r.cinemaOptions[:num]
	r.cinemaOptions = r.cinemaOptions[num:]

	// Рефанд ставок
	for _, opt := range removed {
		for userID, amt := range opt.Bets {
			r.UpdateRating(userID, amt)
			_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Возврат %d кредитов <@%s> за удаленный вариант \"%s\"", amt, userID, opt.Name))
			if err != nil {
				log.Printf("Failed to notify refund: %v", err)
			}
		}
	}
	r.SaveCinemaOptions()
	r.mu.Unlock()

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("✅ Удалено %d самых низких вариантов.", num))
}

// HandleAdjustCinemaCommand обрабатывает !adjustcinema <номер> <+/-сумма>.
func (r *Ranking) HandleAdjustCinemaCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Обработка !adjustcinema: %s от %s", command, m.Author.ID)
	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут корректировать ставки! 🔒")
		return
	}
	parts := strings.Fields(command)
	if len(parts) != 3 {
		s.ChannelMessageSend(m.ChannelID, "Используй: !adjustcinema <номер> <+/-сумма>")
		return
	}
	indexStr := parts[1]
	adjustStr := parts[2]
	index, err := strconv.Atoi(indexStr)
	if err != nil || index <= 0 {
		s.ChannelMessageSend(m.ChannelID, "Неверный номер!")
		return
	}
	index--
	adjust, err := strconv.Atoi(adjustStr)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "Сумма должна быть числом (с + или -)!")
		return
	}

	r.mu.Lock()
	if index >= len(r.cinemaOptions) {
		r.mu.Unlock()
		s.ChannelMessageSend(m.ChannelID, "Неверный номер варианта!")
		return
	}
	opt := &r.cinemaOptions[index]
	opt.Total += adjust
	if opt.Total < 0 {
		opt.Total = 0
	}
	r.SaveCinemaOptions()
	r.mu.Unlock()

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("✅ Обновлен total для \"%s\": %d", opt.Name, opt.Total))
}
