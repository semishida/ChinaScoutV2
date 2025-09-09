package ranking

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/go-redis/redis/v8"
)

// CinemaOption represents a movie option in the auction.
type CinemaOption struct {
	Name  string         `json:"name"`
	Total int            `json:"total"`
	Bets  map[string]int `json:"bets"` // userID: amount
}

// PendingCinemaBid represents a pending bid for confirmation.
type PendingCinemaBid struct {
	UserID         string
	IsNew          bool
	Name           string // for new movies
	Index          int    // for existing movies (0-based)
	Amount         int
	UserMessageID  string // ID of the message with buttons for the user
	AdminMessageID string // ID of the message with buttons for admins
}

func randomColor() int {
	colors := []int{0x1E90FF, 0x00FF00, 0xFFD700, 0xFF69B4, 0x00CED1}
	return colors[rand.Intn(len(colors))]
}

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

func (r *Ranking) HandleCinemaCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Начало обработки !cinema: %s от %s", command, m.Author.ID)
	r.mu.Lock()
	defer r.mu.Unlock()

	args := strings.Fields(command)
	if len(args) < 3 {
		log.Printf("Неверный формат команды: %s", command)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Неверный формат команды",
			Color:       0xFF0000,
			Fields: []*discordgo.MessageEmbedField{
				{Name: "Использование", Value: "`!cinema <название> <сумма>`\nПример: `!cinema Аватар 100`", Inline: false},
			},
			Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp: time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !cinema: %v", err)
		}
		return
	}

	amount, err := strconv.Atoi(args[len(args)-1])
	if err != nil || amount <= 0 {
		log.Printf("Неверная сумма: %s", args[len(args)-1])
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Сумма должна быть положительным числом",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !cinema: %v", err)
		}
		return
	}

	name := strings.Join(args[1:len(args)-1], " ")
	if name == "" {
		log.Printf("Пустое название фильма")
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Название фильма не может быть пустым",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !cinema: %v", err)
		}
		return
	}

	balance := r.GetRating(m.Author.ID)
	if balance < amount {
		log.Printf("Недостаточно кредитов для пользователя %s: баланс %d, требуется %d", m.Author.ID, balance, amount)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: fmt.Sprintf("❌ Недостаточно кредитов. Ваш баланс: %d", balance),
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !cinema: %v", err)
		}
		return
	}

	bidID := generateBidID(m.Author.ID)
	pendingBid := PendingCinemaBid{
		UserID: m.Author.ID,
		IsNew:  true,
		Name:   name,
		Amount: amount,
	}

	bidData, err := json.Marshal(pendingBid)
	if err != nil {
		log.Printf("Ошибка сериализации ставки: %v", err)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Ошибка при создании ставки",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !cinema: %v", err)
		}
		return
	}
	err = r.redis.Set(r.ctx, "pending_bid:"+bidID, bidData, 0).Err()
	if err != nil {
		log.Printf("Ошибка сохранения ставки в Redis: %v", err)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Ошибка при создании ставки",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !cinema: %v", err)
		}
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       "🎥 Подтверждение ставки на киноаукцион",
		Description: "Подтвердите вашу ставку",
		Color:       randomColor(),
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Фильм", Value: name, Inline: true},
			{Name: "Сумма", Value: fmt.Sprintf("%d кредитов", amount), Inline: true},
			{Name: "Пользователь", Value: fmt.Sprintf("<@%s>", m.Author.ID), Inline: true},
		},
		Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
		Timestamp: time.Now().Format(time.RFC3339),
	}

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{Label: "✅ Подтвердить", Style: discordgo.SuccessButton, CustomID: "user_confirm_" + bidID},
				discordgo.Button{Label: "❌ Отменить", Style: discordgo.DangerButton, CustomID: "user_decline_" + bidID},
			},
		},
	}

	msg, err := s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Embed:      embed,
		Components: components,
		Reference:  m.Reference(),
	})
	if err != nil {
		log.Printf("Ошибка отправки сообщения юзеру: %v", err)
		r.redis.Del(r.ctx, "pending_bid:"+bidID) // Удаляем ставку при ошибке
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Ошибка при создании ставки",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения об ошибке для !cinema: %v", err)
		}
		return
	}
	pendingBid.UserMessageID = msg.ID

	bidData, err = json.Marshal(pendingBid)
	if err != nil {
		log.Printf("Ошибка сериализации ставки после добавления UserMessageID: %v", err)
		r.redis.Del(r.ctx, "pending_bid:"+bidID)
		return
	}
	err = r.redis.Set(r.ctx, "pending_bid:"+bidID, bidData, 0).Err()
	if err != nil {
		log.Printf("Ошибка сохранения ставки в Redis после добавления UserMessageID: %v", err)
		r.redis.Del(r.ctx, "pending_bid:"+bidID)
		return
	}

	log.Printf("Ставка успешно создана, bidID: %s, фильм: %s, сумма: %d", bidID, name, amount)
}

func (r *Ranking) HandleBetCinemaCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Начало обработки !betcinema: %s от %s", command, m.Author.ID)
	r.mu.Lock()
	defer r.mu.Unlock()

	args := strings.Fields(command)
	if len(args) != 3 {
		log.Printf("Неверный формат команды: %s", command)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Неверный формат команды",
			Color:       0xFF0000,
			Fields: []*discordgo.MessageEmbedField{
				{Name: "Использование", Value: "`!betcinema <номер> <сумма>`\nПример: `!betcinema 1 50`", Inline: false},
			},
			Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp: time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !betcinema: %v", err)
		}
		return
	}

	// Создаем отсортированную копию для определения правильного индекса
	sortedOptions := make([]CinemaOption, len(r.cinemaOptions))
	copy(sortedOptions, r.cinemaOptions)
	sort.Slice(sortedOptions, func(i, j int) bool {
		return sortedOptions[i].Total > sortedOptions[j].Total
	})

	index, err := strconv.Atoi(args[1])
	if err != nil || index < 1 || index > len(sortedOptions) {
		log.Printf("Неверный номер варианта: %s, доступно: %d фильмов", args[1], len(sortedOptions))
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: fmt.Sprintf("❌ Неверный номер варианта (доступно: 1-%d)", len(sortedOptions)),
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !betcinema: %v", err)
		}
		return
	}

	// Находим соответствующий фильм в оригинальном массиве
	selectedFilm := sortedOptions[index-1]
	var originalIndex int = -1
	for i, option := range r.cinemaOptions {
		if option.Name == selectedFilm.Name && option.Total == selectedFilm.Total {
			originalIndex = i
			break
		}
	}

	if originalIndex == -1 {
		log.Printf("Не удалось найти фильм в оригинальном массиве: %s", selectedFilm.Name)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Ошибка: не удалось найти фильм для ставки",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !betcinema: %v", err)
		}
		return
	}

	amount, err := strconv.Atoi(args[2])
	if err != nil || amount <= 0 {
		log.Printf("Неверная сумма: %s", args[2])
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Сумма должна быть положительным числом",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !betcinema: %v", err)
		}
		return
	}

	balance := r.GetRating(m.Author.ID)
	if balance < amount {
		log.Printf("Недостаточно кредитов для пользователя %s: баланс %d, требуется %d", m.Author.ID, balance, amount)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: fmt.Sprintf("❌ Недостаточно кредитов. Ваш баланс: %d", balance),
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !betcinema: %v", err)
		}
		return
	}

	bidID := generateBidID(m.Author.ID)
	pendingBid := PendingCinemaBid{
		UserID: m.Author.ID,
		IsNew:  false,
		Index:  originalIndex, // Используем оригинальный индекс
		Amount: amount,
	}

	bidData, err := json.Marshal(pendingBid)
	if err != nil {
		log.Printf("Ошибка сериализации ставки: %v", err)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Ошибка при создании ставки",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !betcinema: %v", err)
		}
		return
	}
	err = r.redis.Set(r.ctx, "pending_bid:"+bidID, bidData, 0).Err()
	if err != nil {
		log.Printf("Ошибка сохранения ставки в Redis: %v", err)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Ошибка при создании ставки",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !betcinema: %v", err)
		}
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       "🎥 Подтверждение ставки на киноаукцион",
		Description: "Подтвердите вашу ставку",
		Color:       randomColor(),
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Фильм", Value: selectedFilm.Name, Inline: true},
			{Name: "Сумма", Value: fmt.Sprintf("%d кредитов", amount), Inline: true},
			{Name: "Пользователь", Value: fmt.Sprintf("<@%s>", m.Author.ID), Inline: true},
		},
		Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
		Timestamp: time.Now().Format(time.RFC3339),
	}

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{Label: "✅ Подтвердить", Style: discordgo.SuccessButton, CustomID: "user_confirm_" + bidID},
				discordgo.Button{Label: "❌ Отменить", Style: discordgo.DangerButton, CustomID: "user_decline_" + bidID},
			},
		},
	}

	msg, err := s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Embed:      embed,
		Components: components,
		Reference:  m.Reference(),
	})
	if err != nil {
		log.Printf("Ошибка отправки сообщения юзеру: %v", err)
		r.redis.Del(r.ctx, "pending_bid:"+bidID)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Ошибка при создании ставки",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения об ошибке для !betcinema: %v", err)
		}
		return
	}
	pendingBid.UserMessageID = msg.ID

	bidData, err = json.Marshal(pendingBid)
	if err != nil {
		log.Printf("Ошибка сериализации ставки после добавления UserMessageID: %v", err)
		r.redis.Del(r.ctx, "pending_bid:"+bidID)
		return
	}
	err = r.redis.Set(r.ctx, "pending_bid:"+bidID, bidData, 0).Err()
	if err != nil {
		log.Printf("Ошибка сохранения ставки в Redis после добавления UserMessageID: %v", err)
		r.redis.Del(r.ctx, "pending_bid:"+bidID)
		return
	}

	log.Printf("Ставка успешно создана, bidID: %s, фильм: %s, сумма: %d", bidID, selectedFilm.Name, amount)
}

func (r *Ranking) HandleCinemaButton(s *discordgo.Session, i *discordgo.InteractionCreate) {
	customID := i.MessageComponentData().CustomID
	parts := strings.Split(customID, "_")
	if len(parts) < 3 {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Ошибка: неверный формат кнопки",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}
	action := parts[0] + "_" + parts[1]
	bidID := strings.Join(parts[2:], "_")

	bidData, err := r.redis.Get(r.ctx, "pending_bid:"+bidID).Result()
	if err == redis.Nil {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Ставка не найдена или уже обработана",
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
				Content: "❌ Ошибка при обработке ставки",
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
				Content: "❌ Ошибка при обработке ставки",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	if strings.HasPrefix(action, "user_") {
		if i.Member.User.ID != bid.UserID {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Только автор ставки может подтвердить или отменить её",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}
	} else if strings.HasPrefix(action, "admin_") {
		if !r.IsAdmin(i.Member.User.ID) {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Только админы могут принимать или отклонять ставки",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}
	} else {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Неверный тип кнопки",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if action == "user_confirm" {
		// Проверка баланса
		balance := r.GetRating(bid.UserID)
		if balance < bid.Amount {
			r.redis.Del(r.ctx, "pending_bid:"+bidID)
			userEmbed := &discordgo.MessageEmbed{
				Title:       "🎥 Киноаукцион",
				Description: fmt.Sprintf("❌ Недостаточно кредитов для подтверждения. Ваш баланс: %d", balance),
				Color:       0xFF0000,
				Fields: []*discordgo.MessageEmbedField{
					{Name: "Фильм", Value: bid.Name, Inline: true},
					{Name: "Сумма", Value: fmt.Sprintf("%d кредитов", bid.Amount), Inline: true},
				},
				Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
				Timestamp: time.Now().Format(time.RFC3339),
			}
			s.ChannelMessageEditEmbed(i.ChannelID, bid.UserMessageID, userEmbed)
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Недостаточно кредитов",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		// Замораживаем кредиты
		r.UpdateRating(bid.UserID, -bid.Amount)

		// Уведомляем админов в админ-чате
		adminTags := ""
		for adminID := range r.admins {
			adminTags += fmt.Sprintf("<@%s> ", adminID)
		}
		adminEmbed := &discordgo.MessageEmbed{
			Title:       "🎥 Новая ставка на киноаукцион",
			Description: fmt.Sprintf("%s Пришла заявка от <@%s> на фильм \"%s\" %d кредитов", adminTags, bid.UserID, bid.Name, bid.Amount),
			Color:       randomColor(),
			Fields: []*discordgo.MessageEmbedField{
				{Name: "Фильм", Value: bid.Name, Inline: true},
				{Name: "Сумма", Value: fmt.Sprintf("%d кредитов", bid.Amount), Inline: true},
				{Name: "Пользователь", Value: fmt.Sprintf("<@%s>", bid.UserID), Inline: true},
			},
			Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp: time.Now().Format(time.RFC3339),
		}

		adminComponents := []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{Label: "✅ Принять", Style: discordgo.SuccessButton, CustomID: "admin_accept_" + bidID},
					discordgo.Button{Label: "❌ Отклонить", Style: discordgo.DangerButton, CustomID: "admin_reject_" + bidID},
				},
			},
		}

		adminMsg, err := s.ChannelMessageSendComplex(r.cinemaChannelID, &discordgo.MessageSend{
			Embed:      adminEmbed,
			Components: adminComponents,
		})
		if err != nil {
			log.Printf("Ошибка отправки сообщения админам: %v", err)
			r.UpdateRating(bid.UserID, bid.Amount) // Возвращаем кредиты
			r.redis.Del(r.ctx, "pending_bid:"+bidID)
			userEmbed := &discordgo.MessageEmbed{
				Title:       "🎥 Киноаукцион",
				Description: "❌ Ошибка при отправке ставки админам. Деньги возвращены.",
				Color:       0xFF0000,
				Fields: []*discordgo.MessageEmbedField{
					{Name: "Фильм", Value: bid.Name, Inline: true},
					{Name: "Сумма", Value: fmt.Sprintf("%d кредитов", bid.Amount), Inline: true},
				},
				Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
				Timestamp: time.Now().Format(time.RFC3339),
			}
			s.ChannelMessageEditEmbed(i.ChannelID, bid.UserMessageID, userEmbed)
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Ошибка при отправке ставки админам",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		bid.AdminMessageID = adminMsg.ID

		bidData, err := json.Marshal(bid)
		if err != nil {
			log.Printf("Ошибка сериализации ставки: %v", err)
			r.UpdateRating(bid.UserID, bid.Amount) // Возвращаем кредиты
			r.redis.Del(r.ctx, "pending_bid:"+bidID)
			return
		}
		err = r.redis.Set(r.ctx, "pending_bid:"+bidID, bidData, 0).Err()
		if err != nil {
			log.Printf("Ошибка сохранения ставки в Redis: %v", err)
			r.UpdateRating(bid.UserID, bid.Amount) // Возвращаем кредиты
			r.redis.Del(r.ctx, "pending_bid:"+bidID)
			return
		}

		userEmbed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "✅ Ставка подтверждена и отправлена админам. Кредиты заморожены.",
			Color:       0x00FF00,
			Fields: []*discordgo.MessageEmbedField{
				{Name: "Фильм", Value: bid.Name, Inline: true},
				{Name: "Сумма", Value: fmt.Sprintf("%d кредитов", bid.Amount), Inline: true},
				{Name: "Новый баланс", Value: fmt.Sprintf("%d кредитов", r.GetRating(bid.UserID)), Inline: true},
			},
			Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp: time.Now().Format(time.RFC3339),
		}

		s.ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel:    i.ChannelID,
			ID:         bid.UserMessageID,
			Embed:      userEmbed,
			Components: &[]discordgo.MessageComponent{},
		})

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "✅ Ставка подтверждена",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})

		r.LogCreditOperation(s, fmt.Sprintf("Заморожено %d кредитов у <@%s> за ставку на '%s'", bid.Amount, bid.UserID, bid.Name))
	} else if action == "user_decline" {
		r.redis.Del(r.ctx, "pending_bid:"+bidID)

		userEmbed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Ставка отменена",
			Color:       0xFF0000,
			Fields: []*discordgo.MessageEmbedField{
				{Name: "Фильм", Value: bid.Name, Inline: true},
				{Name: "Сумма", Value: fmt.Sprintf("%d кредитов", bid.Amount), Inline: true},
			},
			Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp: time.Now().Format(time.RFC3339),
		}

		s.ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel:    i.ChannelID,
			ID:         bid.UserMessageID,
			Embed:      userEmbed,
			Components: &[]discordgo.MessageComponent{},
		})

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Ставка отменена",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
	} else if action == "admin_accept" {
		if bid.IsNew {
			r.cinemaOptions = append(r.cinemaOptions, CinemaOption{
				Name:  bid.Name,
				Total: bid.Amount,
				Bets:  map[string]int{bid.UserID: bid.Amount},
			})
		} else {
			if bid.Index >= len(r.cinemaOptions) {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "❌ Вариант больше не существует",
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}
			r.cinemaOptions[bid.Index].Total += bid.Amount
			r.cinemaOptions[bid.Index].Bets[bid.UserID] += bid.Amount
		}

		if err := r.SaveCinemaOptions(); err != nil {
			log.Printf("Ошибка сохранения cinemaOptions: %v", err)
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Ошибка при сохранении данных аукциона",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		r.redis.Del(r.ctx, "pending_bid:"+bidID)

		adminEmbed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "✅ Ставка принята",
			Color:       0x00FF00,
			Fields: []*discordgo.MessageEmbedField{
				{Name: "Фильм", Value: bid.Name, Inline: true},
				{Name: "Сумма", Value: fmt.Sprintf("%d кредитов", bid.Amount), Inline: true},
				{Name: "Пользователь", Value: fmt.Sprintf("<@%s>", bid.UserID), Inline: true},
			},
			Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp: time.Now().Format(time.RFC3339),
		}

		s.ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel:    r.cinemaChannelID,
			ID:         bid.AdminMessageID,
			Embed:      adminEmbed,
			Components: &[]discordgo.MessageComponent{},
		})

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "✅ Ставка принята",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})

		userEmbed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: fmt.Sprintf("✅ Ваша ставка на '%s' (%d кредитов) принята админами!", bid.Name, bid.Amount),
			Color:       0x00FF00,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		s.ChannelMessageSendEmbed(r.floodChannelID, userEmbed)

		r.LogCreditOperation(s, fmt.Sprintf("Ставка %d кредитов от <@%s> на '%s' принята", bid.Amount, bid.UserID, bid.Name))
	} else if action == "admin_reject" {
		r.UpdateRating(bid.UserID, bid.Amount)
		r.redis.Del(r.ctx, "pending_bid:"+bidID)

		adminEmbed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Ставка отклонена, кредиты возвращены",
			Color:       0xFF0000,
			Fields: []*discordgo.MessageEmbedField{
				{Name: "Фильм", Value: bid.Name, Inline: true},
				{Name: "Сумма", Value: fmt.Sprintf("%d кредитов", bid.Amount), Inline: true},
				{Name: "Пользователь", Value: fmt.Sprintf("<@%s>", bid.UserID), Inline: true},
			},
			Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp: time.Now().Format(time.RFC3339),
		}

		s.ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel:    r.cinemaChannelID,
			ID:         bid.AdminMessageID,
			Embed:      adminEmbed,
			Components: &[]discordgo.MessageComponent{},
		})

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Ставка отклонена",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})

		userEmbed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: fmt.Sprintf("❌ Ваша ставка на '%s' (%d кредитов) отклонена админами. Кредиты возвращены.", bid.Name, bid.Amount),
			Color:       0xFF0000,
			Fields: []*discordgo.MessageEmbedField{
				{Name: "Новый баланс", Value: fmt.Sprintf("%d кредитов", r.GetRating(bid.UserID)), Inline: true},
			},
			Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp: time.Now().Format(time.RFC3339),
		}
		s.ChannelMessageSendEmbed(r.floodChannelID, userEmbed)

		r.LogCreditOperation(s, fmt.Sprintf("Возвращено %d кредитов <@%s> за отклонённую ставку на '%s'", bid.Amount, bid.UserID, bid.Name))
	}
}

func (r *Ranking) HandleCinemaListCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Начало обработки !cinemalist для пользователя %s", m.Author.ID)
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.cinemaOptions) == 0 {
		log.Printf("Список cinemaOptions пуст")
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "📋 Список фильмов пуст",
			Color:       randomColor(),
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !cinemalist: %v", err)
		}
		return
	}

	log.Printf("Формирование таблицы для %d фильмов", len(r.cinemaOptions))

	// Создаем копию для сортировки
	sortedOptions := make([]CinemaOption, len(r.cinemaOptions))
	copy(sortedOptions, r.cinemaOptions)

	// Сортируем по убыванию (от большего к меньшему)
	sort.Slice(sortedOptions, func(i, j int) bool {
		return sortedOptions[i].Total > sortedOptions[j].Total
	})

	// Создаем таблицу с двумя колонками
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("**🎬 Топ фильмов (%d)**\n\n", len(r.cinemaOptions)))

	// Разделяем на две колонки
	half := (len(sortedOptions) + 1) / 2

	for i := 0; i < half; i++ {
		// Левая колонка
		if i < len(sortedOptions) {
			option1 := sortedOptions[i]
			filmName1 := option1.Name
			if filmName1 == "" {
				filmName1 = "Неизвестный"
			}
			// Более короткое обрезание названий
			if len(filmName1) > 18 {
				filmName1 = filmName1[:15] + "..."
			}
			builder.WriteString(fmt.Sprintf("`%2d.` %-18s `%6d`", i+1, filmName1, option1.Total))
		}

		// Правая колонка (если есть)
		if i+half < len(sortedOptions) {
			option2 := sortedOptions[i+half]
			filmName2 := option2.Name
			if filmName2 == "" {
				filmName2 = "Неизвестный"
			}
			if len(filmName2) > 18 {
				filmName2 = filmName2[:15] + "..."
			}
			builder.WriteString(fmt.Sprintf("   `%2d.` %-18s `%6d`", i+half+1, filmName2, option2.Total))
		}
		builder.WriteString("\n")
	}

	embed := &discordgo.MessageEmbed{
		Title:       "🎥 КИНОАУКЦИОН",
		Description: builder.String(),
		Color:       0x1E90FF,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "📋 Команды",
				Value:  "• `!betcinema <номер> <сумма>` - Ставка на фильм\n• `!cinema <название> <сумма>` - Новый фильм\n• `!cinemalist` - Обновить список",
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Сортировка по количеству кредитов ↓",
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}

	// Проверяем длину и при необходимости разбиваем
	if len(embed.Description) > 2000 {
		log.Printf("Сообщение слишком длинное, разбиваем на части")

		// Первая часть - первая половина
		builder1 := strings.Builder{}
		builder1.WriteString(fmt.Sprintf("**🎬 Топ фильмов (%d) - Часть 1/2**\n\n", len(r.cinemaOptions)))

		for i := 0; i < half && i < 15; i++ {
			if i < len(sortedOptions) {
				option := sortedOptions[i]
				filmName := option.Name
				if filmName == "" {
					filmName = "Неизвестный"
				}
				if len(filmName) > 25 {
					filmName = filmName[:22] + "..."
				}
				builder1.WriteString(fmt.Sprintf("`%2d.` %-25s `%7d`\n", i+1, filmName, option.Total))
			}
		}

		embed1 := &discordgo.MessageEmbed{
			Title:       "🎥 КИНОАУКЦИОН",
			Description: builder1.String(),
			Color:       0x1E90FF,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Часть 1/2"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}

		// Вторая часть - вторая половина
		builder2 := strings.Builder{}
		builder2.WriteString(fmt.Sprintf("**🎬 Топ фильмов (%d) - Часть 2/2**\n\n", len(r.cinemaOptions)))

		start := half
		if start > 15 {
			start = 15
		}

		for i := start; i < len(sortedOptions); i++ {
			option := sortedOptions[i]
			filmName := option.Name
			if filmName == "" {
				filmName = "Неизвестный"
			}
			if len(filmName) > 25 {
				filmName = filmName[:22] + "..."
			}
			builder2.WriteString(fmt.Sprintf("`%2d.` %-25s `%7d`\n", i+1, filmName, option.Total))
		}

		embed2 := &discordgo.MessageEmbed{
			Title:       "🎥 КИНОАУКЦИОН",
			Description: builder2.String(),
			Color:       0x1E90FF,
			Fields: []*discordgo.MessageEmbedField{
				{
					Name:   "📋 Команды",
					Value:  "• `!betcinema <номер> <сумма>` - Ставка\n• `!cinema <название> <сумма>` - Новый фильм",
					Inline: false,
				},
			},
			Footer:    &discordgo.MessageEmbedFooter{Text: "Часть 2/2 • Сортировка по кредитам ↓"},
			Timestamp: time.Now().Format(time.RFC3339),
		}

		// Отправляем обе части
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed1); err != nil {
			log.Printf("Ошибка отправки части 1: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed2); err != nil {
			log.Printf("Ошибка отправки части 2: %v", err)
		}

	} else {
		// Отправляем единое сообщение
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !cinemalist: %v", err)
		}
	}

	log.Printf("Завершение обработки !cinemalist")
}

func (r *Ranking) HandleAdminCinemaListCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Начало обработки !admincinemalist для пользователя %s", m.Author.ID)
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.IsAdmin(m.Author.ID) {
		log.Printf("Пользователь %s не админ", m.Author.ID)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Только админы могут просматривать детальный список",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !admincinemalist: %v", err)
		}
		return
	}

	if len(r.cinemaOptions) == 0 {
		log.Printf("Список cinemaOptions пуст")
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "📋 Список фильмов пуст",
			Color:       randomColor(),
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !admincinemalist: %v", err)
		}
		return
	}

	log.Printf("Формирование таблицы для %d фильмов", len(r.cinemaOptions))
	table := "```css\n"
	table += fmt.Sprintf("%-5s %-40s %-10s %s\n", "#", "Фильм", "Кредиты", "Ставки")
	table += strings.Repeat("-", 80) + "\n"

	for i, option := range r.cinemaOptions {
		if i >= 100 {
			log.Printf("Достигнут лимит в 100 позиций")
			break
		}
		filmName := option.Name
		if filmName == "" {
			log.Printf("Пустое название фильма для позиции %d, замена на 'Неизвестный фильм'", i+1)
			filmName = "Неизвестный фильм"
		}
		if len(filmName) > 37 {
			filmName = filmName[:34] + "..."
		}
		bets := []string{}
		for userID, amount := range option.Bets {
			bets = append(bets, fmt.Sprintf("<@%s>: %d", userID, amount))
		}
		betsStr := strings.Join(bets, ", ")
		if betsStr == "" {
			betsStr = "Нет ставок"
		}
		if len(betsStr) > 100 {
			betsStr = betsStr[:97] + "..."
		}
		table += fmt.Sprintf("%-5d %-40s %-10d %s\n", i+1, filmName, option.Total, betsStr)
	}
	table += "```"

	embed := &discordgo.MessageEmbed{
		Title:       "🎥 Детальный список фильмов (админ)",
		Description: fmt.Sprintf("📋 Текущие фильмы на аукционе (%d):\n%s", len(r.cinemaOptions), table),
		Color:       randomColor(),
		Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬 | Только для админов"},
		Timestamp:   time.Now().Format(time.RFC3339),
	}

	log.Printf("Длина описания embed: %d символов", len(embed.Description))
	if len(embed.Description) > 2000 {
		log.Printf("Разбиение длинного сообщения")
		parts, err := splitLongMessage(embed.Description, 1900)
		if err != nil {
			log.Printf("Ошибка разбиения сообщения для !admincinemalist: %v", err)
			embed := &discordgo.MessageEmbed{
				Title:       "🎥 Киноаукцион",
				Description: "❌ Ошибка при формировании списка",
				Color:       0xFF0000,
				Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
				Timestamp:   time.Now().Format(time.RFC3339),
			}
			if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
				log.Printf("Ошибка отправки сообщения об ошибке для !admincinemalist: %v", err)
			}
			return
		}
		for i, part := range parts {
			log.Printf("Отправка части %d из %d", i+1, len(parts))
			partEmbed := &discordgo.MessageEmbed{
				Title:       fmt.Sprintf("🎥 Детальный список фильмов (Часть %d)", i+1),
				Description: part,
				Color:       embed.Color,
				Footer:      embed.Footer,
				Timestamp:   embed.Timestamp,
			}
			if _, err := s.ChannelMessageSendEmbed(m.ChannelID, partEmbed); err != nil {
				log.Printf("Ошибка отправки части %d для !admincinemalist: %v", i+1, err)
			} else {
				log.Printf("Часть %d успешно отправлена", i+1)
			}
		}
	} else {
		log.Printf("Отправка единого сообщения для !admincinemalist в канал %s", m.ChannelID)
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !admincinemalist: %v", err)
		} else {
			log.Printf("Сообщение успешно отправлено")
		}
	}
	log.Printf("Завершение обработки !admincinemalist")
}

func (r *Ranking) HandleRemoveLowestCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Начало обработки !removelowest: %s от %s", command, m.Author.ID)

	if !r.IsAdmin(m.Author.ID) {
		log.Printf("Пользователь %s не админ", m.Author.ID)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Только админы могут удалять варианты",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !removelowest: %v", err)
		}
		return
	}

	args := strings.Fields(command)
	if len(args) != 2 {
		log.Printf("Неверный формат команды: %s", command)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Неверный формат команды",
			Color:       0xFF0000,
			Fields: []*discordgo.MessageEmbedField{
				{Name: "Использование", Value: "`!removelowest <число>`\nПример: `!removelowest 2`", Inline: false},
			},
			Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp: time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !removelowest: %v", err)
		}
		return
	}

	count, err := strconv.Atoi(args[1])
	if err != nil || count <= 0 {
		log.Printf("Неверное число: %s", args[1])
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Число должно быть положительным",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !removelowest: %v", err)
		}
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.cinemaOptions) == 0 {
		log.Printf("Список cinemaOptions пуст")
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "📋 Список фильмов пуст, удалять нечего",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !removelowest: %v", err)
		}
		return
	}

	if count > len(r.cinemaOptions) {
		log.Printf("Указано слишком большое число для удаления: %d, доступно: %d, устанавливаю count = %d", count, len(r.cinemaOptions), len(r.cinemaOptions))
		count = len(r.cinemaOptions)
	}

	// Создаем слайс с индексами и суммами для сортировки
	type filmIndex struct {
		index int
		total int
		name  string
	}

	films := make([]filmIndex, len(r.cinemaOptions))
	for i, option := range r.cinemaOptions {
		films[i] = filmIndex{index: i, total: option.Total, name: option.Name}
	}

	// Сортируем по возрастанию (от меньшего к большему)
	sort.Slice(films, func(i, j int) bool {
		return films[i].total < films[j].total
	})

	// Получаем индексы для удаления (первые count элементов)
	indexesToRemove := make([]int, count)
	removedFilms := make([]string, 0, count)

	for i := 0; i < count; i++ {
		indexesToRemove[i] = films[i].index
		filmName := films[i].name
		if filmName == "" {
			filmName = "Неизвестный фильм"
		}
		removedFilms = append(removedFilms, filmName)
	}

	// Сортируем индексы в обратном порядке для безопасного удаления
	sort.Sort(sort.Reverse(sort.IntSlice(indexesToRemove)))

	// Удаляем фильмы и возвращаем кредиты
	for _, index := range indexesToRemove {
		option := r.cinemaOptions[index]
		for userID, amount := range option.Bets {
			log.Printf("Возврат %d кредитов пользователю %s за фильм '%s'", amount, userID, option.Name)
			r.UpdateRating(userID, amount)
			r.LogCreditOperation(s, fmt.Sprintf("Возвращено %d кредитов пользователю <@%s> за удаление фильма '%s'", amount, userID, option.Name))
		}

		// Удаляем элемент из слайса
		r.cinemaOptions = append(r.cinemaOptions[:index], r.cinemaOptions[index+1:]...)
	}

	if err := r.SaveCinemaOptions(); err != nil {
		log.Printf("Ошибка сохранения cinemaOptions: %v", err)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Ошибка при сохранении данных аукциона",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения об ошибке для !removelowest: %v", err)
		}
		return
	}

	log.Printf("Формирование embed для удаленных фильмов: %v", removedFilms)
	embed := &discordgo.MessageEmbed{
		Title:       "🎥 Киноаукцион",
		Description: fmt.Sprintf("🗑️ Удалено %d вариант(ов) с наименьшим количеством кредитов", count),
		Color:       randomColor(),
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Удалённые фильмы", Value: strings.Join(removedFilms, ", "), Inline: false},
			{Name: "Действие", Value: "Кредиты возвращены участникам", Inline: false},
		},
		Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
		Timestamp: time.Now().Format(time.RFC3339),
	}

	if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
		log.Printf("Ошибка отправки сообщения для !removelowest: %v", err)
	} else {
		log.Printf("Сообщение об успешном удалении отправлено")
	}
	log.Printf("Завершение обработки !removelowest")
}

func (r *Ranking) HandleAdjustCinemaCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Начало обработки !adjustcinema: %s от %s", command, m.Author.ID)

	if !r.IsAdmin(m.Author.ID) {
		log.Printf("Пользователь %s не админ", m.Author.ID)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Только админы могут корректировать варианты",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !adjustcinema: %v", err)
		}
		return
	}

	args := strings.Fields(command)
	if len(args) != 3 {
		log.Printf("Неверный формат команды: %s", command)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Неверный формат команды",
			Color:       0xFF0000,
			Fields: []*discordgo.MessageEmbedField{
				{Name: "Использование", Value: "`!adjustcinema <номер> <+/-сумма>`\nПример: `!adjustcinema 1 +100`", Inline: false},
			},
			Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp: time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !adjustcinema: %v", err)
		}
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Создаем отсортированную копию для определения правильного индекса
	sortedOptions := make([]CinemaOption, len(r.cinemaOptions))
	copy(sortedOptions, r.cinemaOptions)
	sort.Slice(sortedOptions, func(i, j int) bool {
		return sortedOptions[i].Total > sortedOptions[j].Total
	})

	index, err := strconv.Atoi(args[1])
	if err != nil || index < 1 || index > len(sortedOptions) {
		log.Printf("Неверный номер варианта: %s, доступно: %d фильмов", args[1], len(sortedOptions))
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: fmt.Sprintf("❌ Неверный номер варианта (доступно: 1-%d)", len(sortedOptions)),
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !adjustcinema: %v", err)
		}
		return
	}

	// Находим соответствующий фильм в оригинальном массиве
	filmToAdjust := sortedOptions[index-1]
	var originalIndex int = -1
	for i, option := range r.cinemaOptions {
		if option.Name == filmToAdjust.Name && option.Total == filmToAdjust.Total {
			originalIndex = i
			break
		}
	}

	if originalIndex == -1 {
		log.Printf("Не удалось найти фильм в оригинальном массиве: %s", filmToAdjust.Name)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Ошибка: не удалось найти фильм для корректировки",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !adjustcinema: %v", err)
		}
		return
	}

	adjustmentStr := args[2]
	adjustment, err := strconv.Atoi(adjustmentStr)
	if err != nil {
		log.Printf("Неверная корректировка: %s", adjustmentStr)
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Корректировка должна быть числом (например, +100 или -50)",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения для !adjustcinema: %v", err)
		}
		return
	}

	oldTotal := r.cinemaOptions[originalIndex].Total
	r.cinemaOptions[originalIndex].Total += adjustment
	if r.cinemaOptions[originalIndex].Total < 0 {
		log.Printf("Корректировка привела к отрицательной сумме, установка в 0 для варианта #%d", index)
		r.cinemaOptions[originalIndex].Total = 0
	}

	if err := r.SaveCinemaOptions(); err != nil {
		log.Printf("Ошибка сохранения cinemaOptions: %v", err)
		r.cinemaOptions[originalIndex].Total = oldTotal // Откатываем изменения
		embed := &discordgo.MessageEmbed{
			Title:       "🎥 Киноаукцион",
			Description: "❌ Ошибка при сохранении данных аукциона",
			Color:       0xFF0000,
			Footer:      &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
			log.Printf("Ошибка отправки сообщения об ошибке для !adjustcinema: %v", err)
		}
		return
	}

	log.Printf("Корректировка завершена для варианта #%d (%s), старая сумма: %d, новая сумма: %d", index, filmToAdjust.Name, oldTotal, r.cinemaOptions[originalIndex].Total)
	embed := &discordgo.MessageEmbed{
		Title:       "🎥 Киноаукцион",
		Description: fmt.Sprintf("⚙️ Вариант #%d скорректирован", index),
		Color:       randomColor(),
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Фильм", Value: filmToAdjust.Name, Inline: true},
			{Name: "Корректировка", Value: adjustmentStr, Inline: true},
			{Name: "Новая сумма", Value: fmt.Sprintf("%d кредитов", r.cinemaOptions[originalIndex].Total), Inline: true},
		},
		Footer:    &discordgo.MessageEmbedFooter{Text: "Киноаукцион 🎬"},
		Timestamp: time.Now().Format(time.RFC3339),
	}
	if _, err := s.ChannelMessageSendEmbed(m.ChannelID, embed); err != nil {
		log.Printf("Ошибка отправки сообщения для !adjustcinema: %v", err)
	} else {
		log.Printf("Сообщение об успешной корректировке отправлено в канал %s", m.ChannelID)
	}
	log.Printf("Завершение обработки !adjustcinema")
}

func generateBidID(userID string) string {
	return fmt.Sprintf("%s-%d", userID, time.Now().UnixNano())
}

func splitLongMessage(message string, maxLength int) ([]string, error) {
	log.Printf("Разбиение сообщения длиной %d символов, maxLength: %d", len(message), maxLength)
	if maxLength <= 0 {
		log.Printf("Ошибка: maxLength должен быть положительным")
		return nil, fmt.Errorf("maxLength должен быть положительным")
	}
	if message == "" {
		log.Printf("Сообщение пустое, возврат пустого списка")
		return []string{"```\n(Пустой список)\n```"}, nil
	}

	var parts []string
	lines := strings.Split(message, "\n")
	currentPart := ""
	currentLength := 0

	for _, line := range lines {
		if len(line) > maxLength {
			log.Printf("Обрезка длинной строки: %d символов", len(line))
			line = line[:maxLength-3] + "..."
		}
		if currentLength+len(line)+1 > maxLength {
			if currentPart == "" {
				currentPart = "```\n"
			}
			parts = append(parts, currentPart+"```")
			log.Printf("Добавлена часть длиной %d символов", len(currentPart+"```"))
			currentPart = "```\n"
			currentLength = len(line) + len("```css\n") + 1
		} else {
			if currentPart == "" {
				currentPart = "```"
			}
			currentPart += line + "\n"
			currentLength += len(line) + 1
		}
	}

	if currentPart != "" {
		parts = append(parts, currentPart+"```")
		log.Printf("Добавлена последняя часть длиной %d символов", len(currentPart+"```"))
	}

	if len(parts) == 0 {
		log.Printf("Список частей пуст, добавление дефолтной части")
		parts = append(parts, "```\n(Пустой список)\n```")
	}

	log.Printf("Сообщение разбито на %d частей", len(parts))
	return parts, nil
}
