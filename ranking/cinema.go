package ranking

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

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
	UserID    string
	IsNew     bool
	Name      string // для новых
	Index     int    // для существующих (0-based)
	Amount    int
	MessageID string
}

// LoadCinemaOptions загружает варианты аукциона из Redis.
func (r *Ranking) LoadCinemaOptions() error {
	data, err := r.redis.Get(r.ctx, "cinema_options").Result()
	if err == redis.Nil {
		r.cinemaOptions = []CinemaOption{}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to load cinemaOptions from Redis: %v", err)
	}
	if err := json.Unmarshal([]byte(data), &r.cinemaOptions); err != nil {
		return fmt.Errorf("failed to unmarshal cinemaOptions: %v", err)
	}
	return nil
}

// SaveCinemaOptions сохраняет варианты аукциона в Redis.
func (r *Ranking) SaveCinemaOptions() error {
	data, err := json.Marshal(r.cinemaOptions)
	if err != nil {
		return fmt.Errorf("failed to marshal cinemaOptions: %v", err)
	}
	err = r.redis.Set(r.ctx, "cinema_options", data, 0).Err()
	if err != nil {
		return fmt.Errorf("failed to save cinemaOptions to Redis: %v", err)
	}
	return nil
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
	r.mu.Lock()
	defer r.mu.Unlock()

	args := strings.Fields(command)
	if len(args) < 3 {
		s.ChannelMessageSend(m.ChannelID, "Использование: !cinema <название> <сумма>")
		return
	}

	amount, err := strconv.Atoi(args[len(args)-1])
	if err != nil || amount <= 0 {
		s.ChannelMessageSend(m.ChannelID, "Сумма должна быть положительным числом")
		return
	}

	name := strings.Join(args[1:len(args)-1], " ")
	if name == "" {
		s.ChannelMessageSend(m.ChannelID, "Название фильма не может быть пустым")
		return
	}

	// Проверка баланса пользователя через GetRating
	balance := r.GetRating(m.Author.ID)
	if balance < amount {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Недостаточно кредитов. Ваш баланс: %d", balance))
		return
	}

	// Создаём pending ставку
	bidID := generateBidID(m.Author.ID)
	pendingBid := PendingCinemaBid{
		UserID:    m.Author.ID,
		IsNew:     true,
		Name:      name,
		Amount:    amount,
		MessageID: "",
	}

	// Отправляем сообщение с кнопками в CINEMA_CHANNEL_ID
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Подтвердить",
					Style:    discordgo.SuccessButton,
					CustomID: "cinema_confirm_" + bidID,
				},
				discordgo.Button{
					Label:    "Отклонить",
					Style:    discordgo.DangerButton,
					CustomID: "cinema_decline_" + bidID,
				},
			},
		},
	}

	msg, err := s.ChannelMessageSendComplex(r.cinemaChannelID, &discordgo.MessageSend{
		Content:    fmt.Sprintf("Новая ставка от <@%s>: фильм '%s', сумма %d. Подтвердите или отклоните:", m.Author.ID, name, amount),
		Components: components,
	})
	if err != nil {
		log.Printf("Ошибка отправки сообщения с кнопками: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Ошибка при создании ставки")
		return
	}

	pendingBid.MessageID = msg.ID

	// Сохраняем pending ставку в Redis
	bidData, err := json.Marshal(pendingBid)
	if err != nil {
		log.Printf("Ошибка сериализации ставки: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Ошибка при создании ставки")
		return
	}
	err = r.redis.Set(r.ctx, "pending_bid:"+bidID, bidData, 0).Err()
	if err != nil {
		log.Printf("Ошибка сохранения ставки в Redis: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Ошибка при создании ставки")
		return
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Ваша ставка на '%s' (%d кредитов) отправлена на подтверждение", name, amount))
}

// HandleBetCinemaCommand обрабатывает !betcinema <номер> <сумма>.
func (r *Ranking) HandleBetCinemaCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	args := strings.Fields(command)
	if len(args) != 3 {
		s.ChannelMessageSend(m.ChannelID, "Использование: !betcinema <номер> <сумма>")
		return
	}

	index, err := strconv.Atoi(args[1])
	if err != nil || index < 1 || index > len(r.cinemaOptions) {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Неверный номер варианта (доступно: 1-%d)", len(r.cinemaOptions)))
		return
	}

	amount, err := strconv.Atoi(args[2])
	if err != nil || amount <= 0 {
		s.ChannelMessageSend(m.ChannelID, "Сумма должна быть положительным числом")
		return
	}

	// Проверка баланса пользователя через GetRating
	balance := r.GetRating(m.Author.ID)
	if balance < amount {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Недостаточно кредитов. Ваш баланс: %d", balance))
		return
	}

	// Создаём pending ставку
	bidID := generateBidID(m.Author.ID)
	pendingBid := PendingCinemaBid{
		UserID:    m.Author.ID,
		IsNew:     false,
		Index:     index - 1,
		Amount:    amount,
		MessageID: "",
	}

	// Отправляем сообщение с кнопками в CINEMA_CHANNEL_ID
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Подтвердить",
					Style:    discordgo.SuccessButton,
					CustomID: "cinema_confirm_" + bidID,
				},
				discordgo.Button{
					Label:    "Отклонить",
					Style:    discordgo.DangerButton,
					CustomID: "cinema_decline_" + bidID,
				},
			},
		},
	}

	msg, err := s.ChannelMessageSendComplex(r.cinemaChannelID, &discordgo.MessageSend{
		Content:    fmt.Sprintf("Ставка от <@%s> на вариант #%d (%s), сумма %d. Подтвердите или отклоните:", m.Author.ID, index, r.cinemaOptions[index-1].Name, amount),
		Components: components,
	})
	if err != nil {
		log.Printf("Ошибка отправки сообщения с кнопками: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Ошибка при создании ставки")
		return
	}

	pendingBid.MessageID = msg.ID

	// Сохраняем pending ставку в Redis
	bidData, err := json.Marshal(pendingBid)
	if err != nil {
		log.Printf("Ошибка сериализации ставки: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Ошибка при создании ставки")
		return
	}
	err = r.redis.Set(r.ctx, "pending_bid:"+bidID, bidData, 0).Err()
	if err != nil {
		log.Printf("Ошибка сохранения ставки в Redis: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Ошибка при создании ставки")
		return
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Ваша ставка на '%s' (%d кредитов) отправлена на подтверждение", r.cinemaOptions[index-1].Name, amount))
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

func (r *Ranking) HandleCinemaButton(s *discordgo.Session, i *discordgo.InteractionCreate) {
	customID := i.MessageComponentData().CustomID
	bidID := strings.Split(customID, "_")[2]

	// Загружаем pending ставку
	bidData, err := r.redis.Get(r.ctx, "pending_bid:"+bidID).Result()
	if err == redis.Nil {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Ставка не найдена или уже обработана",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}
	if err != nil {
		log.Printf("Ошибка загрузки ставки %s: %v", bidID, err)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Ошибка при обработке ставки",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	var bid PendingCinemaBid
	if err := json.Unmarshal([]byte(bidData), &bid); err != nil {
		log.Printf("Ошибка десериализации ставки %s: %v", bidID, err)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Ошибка при обработке ставки",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	// Проверяем, что пользователь — автор ставки
	if i.Member.User.ID != bid.UserID {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Только автор ставки может подтвердить или отклонить её",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if strings.HasPrefix(customID, "cinema_confirm_") {
		// Подтверждение ставки
		if bid.IsNew {
			// Новая опция
			r.cinemaOptions = append(r.cinemaOptions, CinemaOption{
				Name:  bid.Name,
				Total: bid.Amount,
				Bets:  map[string]int{bid.UserID: bid.Amount},
			})
		} else {
			// Существующая опция
			if bid.Index >= len(r.cinemaOptions) {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "Вариант больше не существует",
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}
			r.cinemaOptions[bid.Index].Total += bid.Amount
			r.cinemaOptions[bid.Index].Bets[bid.UserID] += bid.Amount
		}

		// Снимаем кредиты через UpdateRating
		r.UpdateRating(bid.UserID, -bid.Amount)

		// Сохраняем обновлённые cinemaOptions
		if err := r.SaveCinemaOptions(); err != nil {
			log.Printf("Ошибка сохранения cinemaOptions: %v", err)
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "Ошибка при сохранении данных аукциона",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		// Удаляем pending ставку
		r.redis.Del(r.ctx, "pending_bid:"+bidID)

		// Обновляем сообщение
		content := fmt.Sprintf("Ставка от <@%s> на '%s' (%d кредитов) подтверждена", bid.UserID, bid.Name, bid.Amount)
		components := []discordgo.MessageComponent{}
		s.ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel:    r.cinemaChannelID,
			ID:         bid.MessageID,
			Content:    &content,
			Components: &components,
		})

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Ставка подтверждена",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})

		r.LogCreditOperation(s, fmt.Sprintf("Списано %d кредитов у <@%s> за ставку на '%s'", bid.Amount, bid.UserID, bid.Name))
	} else if strings.HasPrefix(customID, "cinema_decline_") {
		// Отклонение ставки
		r.redis.Del(r.ctx, "pending_bid:"+bidID)

		content := fmt.Sprintf("Ставка от <@%s> на '%s' (%d кредитов) отклонена", bid.UserID, bid.Name, bid.Amount)
		components := []discordgo.MessageComponent{}
		s.ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel:    r.cinemaChannelID,
			ID:         bid.MessageID,
			Content:    &content,
			Components: &components,
		})

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Ставка отклонена",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
	}
}

func generateBidID(userID string) string {
	return fmt.Sprintf("%s-%d", userID, time.Now().UnixNano())
}
