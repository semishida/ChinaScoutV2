package ranking

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/bwmarrin/discordgo"
	"github.com/go-redis/redis/v8"
)

// RegisterSlashCommands регистрирует слэш-команды
func (r *Ranking) RegisterSlashCommands(s *discordgo.Session, guildID string) error {
	commands := []*discordgo.ApplicationCommand{
		{
			Name:        "china",
			Description: "Проверить баланс соцкредитов",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь для проверки баланса",
					Required:    false,
				},
			},
		},
		{
			Name:        "top",
			Description: "Топ-5 пользователей по кредитам",
		},
		{
			Name:        "stats",
			Description: "Статистика пользователя",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь для просмотра статистики",
					Required:    false,
				},
			},
		},
		{
			Name:        "transfer",
			Description: "Передать кредиты другому пользователю",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Кому передать кредиты",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "amount",
					Description: "Сумма для передачи",
					Required:    true,
					MinValue:    &[]float64{1}[0],
					MaxValue:    1000000,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "reason",
					Description: "Причина перевода",
					Required:    false,
				},
			},
		},
		{
			Name:        "inventory",
			Description: "Показать инвентарь NFT",
		},
		{
			Name:        "case_inventory",
			Description: "Показать инвентарь кейсов",
		},
		{
			Name:        "btc",
			Description: "Текущий курс биткойна",
		},
		{
			Name:        "prices",
			Description: "Статистика цен NFT",
		},
		{
			Name:        "case_bank",
			Description: "Показать банк кейсов",
		},
		{
			Name:        "daily_case",
			Description: "Получить ежедневный кейс",
		},
		{
			Name:        "chelp",
			Description: "Помощь по командам",
		},
		{
			Name:        "case_help",
			Description: "Помощь по кейсам и NFT",
		},
		{
			Name:        "admin",
			Description: "Админская команда (только для админов)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "amount",
					Description: "Сумма изменения",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "reason",
					Description: "Причина изменения",
					Required:    false,
				},
			},
		},
	}

	// Регистрируем команды для гильдии
	for _, cmd := range commands {
		_, err := s.ApplicationCommandCreate(s.State.User.ID, guildID, cmd)
		if err != nil {
			return fmt.Errorf("cannot create '%v' command: %v", cmd.Name, err)
		}
	}

	log.Printf("✅ Зарегистрировано %d слэш-команд", len(commands))
	return nil
}

// HandleSlashCommand обрабатывает все слэш-команды
func (r *Ranking) HandleSlashCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	
	// Отложенный ответ (обязательно для слэш-команд)
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		log.Printf("Ошибка отложенного ответа: %v", err)
		return
	}

	switch data.Name {
	case "china":
		r.handleSlashChina(s, i)
	case "top":
		r.handleSlashTop(s, i)
	case "stats":
		r.handleSlashStats(s, i)
	case "transfer":
		r.handleSlashTransfer(s, i)
	case "inventory":
		r.handleSlashInventory(s, i)
	case "case_inventory":
		r.handleSlashCaseInventory(s, i)
	case "btc":
		r.handleSlashBTC(s, i)
	case "prices":
		r.handleSlashPrices(s, i)
	case "case_bank":
		r.handleSlashCaseBank(s, i)
	case "daily_case":
		r.handleSlashDailyCase(s, i)
	case "chelp":
		r.handleSlashChelp(s, i)
	case "case_help":
		r.handleSlashCaseHelp(s, i)
	case "admin":
		r.handleSlashAdmin(s, i)
	default:
		r.handleSlashUnknown(s, i)
	}
}

// handleSlashChina обработчик команды /china
func (r *Ranking) handleSlashChina(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	userID := i.Member.User.ID
	username := i.Member.User.Username

	// Если указан пользователь
	if len(data.Options) > 0 {
		targetUser := data.Options[0].UserValue(s)
		userID = targetUser.ID
		username = targetUser.Username
	}

	userRating := r.GetRating(userID)
	
	content := fmt.Sprintf("💰 %s, баланс: **%d** соцкредитов! 🇨🇳", username, userRating)
	_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &content,
	})
	if err != nil {
		log.Printf("Ошибка отправки ответа: %v", err)
	}
}

// handleSlashTop обработчик команды /top
func (r *Ranking) handleSlashTop(s *discordgo.Session, i *discordgo.InteractionCreate) {
	topUsers := r.GetTop5()
	if len(topUsers) == 0 {
		content := "🏆 Пока нет лидеров! Будь первым! 😎"
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &content,
		})
		return
	}

	response := "🏆 **Топ-5 пользователей:**\n"
	for i, user := range topUsers {
		response += fmt.Sprintf("%d. <@%s> — %d кредитов\n", i+1, user.ID, user.Rating)
	}
	
	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
}

// handleSlashStats обработчик команды /stats
func (r *Ranking) handleSlashStats(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	targetID := i.Member.User.ID
	targetUsername := i.Member.User.Username

	if len(data.Options) > 0 {
		targetUser := data.Options[0].UserValue(s)
		targetID = targetUser.ID
		targetUsername = targetUser.Username
	}

	user := User{ID: targetID}
	dataStr, err := r.redis.Get(r.ctx, "user:"+targetID).Result()
	if err == redis.Nil {
		content := fmt.Sprintf("❌ У пользователя %s нет статистики! 😢", targetUsername)
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &content,
		})
		return
	}

	if err != nil {
		content := "❌ Ошибка при загрузке статистики!"
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &content,
		})
		return
	}

	if err := json.Unmarshal([]byte(dataStr), &user); err != nil {
		content := "❌ Ошибка при обработке данных пользователя!"
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &content,
		})
		return
	}

	// Создаем embed аналогично HandleStatsCommand
	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("📊 Статистика %s", targetUsername),
		Description: "Твои достижения в мире соцкредитов! 🌟",
		Color:       0xFFD700,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "💰 Баланс",
				Value:  fmt.Sprintf("**%d** соцкредитов", user.Rating),
				Inline: false,
			},
			{
				Name:   "⚔️ Дуэли",
				Value:  fmt.Sprintf("Сыграно: **%d**\nПобед: **%d**", user.DuelsPlayed, user.DuelsWon),
				Inline: true,
			},
			{
				Name:   "🔴⚫️ RedBlack",
				Value:  fmt.Sprintf("Сыграно: **%d**\nПобед: **%d**", user.RBPlayed, user.RBWon),
				Inline: true,
			},
			{
				Name:   "♠️ Blackjack",
				Value:  fmt.Sprintf("Сыграно: **%d**\nПобед: **%d**", user.BJPlayed, user.BJWon),
				Inline: true,
			},
			{
				Name:   "🎙 Время в голосовых каналах",
				Value:  fmt.Sprintf("**%s**", r.formatTimeForSlash(user.VoiceSeconds)),
				Inline: false,
			},
		},
	}

	embeds := []*discordgo.MessageEmbed{embed}
	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Embeds: &embeds,
	})
}

// handleSlashTransfer обработчик команды /transfer
func (r *Ranking) handleSlashTransfer(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	options := data.Options
	
	targetUser := options[0].UserValue(s)
	amount := int(options[1].IntValue())
	reason := ""
	if len(options) > 2 {
		reason = options[2].StringValue()
	}

	// Проверяем возможность перевода
	if targetUser.ID == i.Member.User.ID {
		content := "❌ Нельзя передать кредиты самому себе!"
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &content,
		})
		return
	}

	userRating := r.GetRating(i.Member.User.ID)
	if userRating < amount {
		content := fmt.Sprintf("❌ Недостаточно кредитов! Твой баланс: %d", userRating)
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &content,
		})
		return
	}

	// Выполняем перевод
	r.UpdateRating(i.Member.User.ID, -amount)
	r.UpdateRating(targetUser.ID, amount)

	// Формируем сообщение
	msg := fmt.Sprintf("✅ <@%s> передал %d соцкредитов пользователю <@%s>!", 
		i.Member.User.ID, amount, targetUser.ID)
	if reason != "" {
		msg += fmt.Sprintf("\n🗒️ Причина: %s", reason)
	}

	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &msg,
	})

	// Логируем операцию
	r.LogCreditOperation(s, fmt.Sprintf("<@%s> передает %d соцкредитов пользователю <@%s>%s", 
		i.Member.User.ID, amount, targetUser.ID, r.formatReasonForSlash(reason)))
}

// handleSlashInventory обработчик команды /inventory
func (r *Ranking) handleSlashInventory(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Создаем фейковое сообщение и вызываем старый обработчик
	fakeMsg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ChannelID: i.ChannelID,
			Author: &discordgo.User{
				ID:       i.Member.User.ID,
				Username: i.Member.User.Username,
			},
			Content: "!inventory",
		},
	}
	
	r.HandleInventoryCommand(s, fakeMsg)
	
	// Ответ уже отправлен через старый обработчик
	log.Printf("Slash command /inventory executed by %s", i.Member.User.Username)
}

// handleSlashCaseInventory обработчик команды /case_inventory
func (r *Ranking) handleSlashCaseInventory(s *discordgo.Session, i *discordgo.InteractionCreate) {
	fakeMsg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ChannelID: i.ChannelID,
			Author: &discordgo.User{
				ID:       i.Member.User.ID,
				Username: i.Member.User.Username,
			},
			Content: "!case_inventory",
		},
	}
	r.HandleCaseInventoryCommand(s, fakeMsg)
}

// handleSlashBTC обработчик команды /btc
func (r *Ranking) handleSlashBTC(s *discordgo.Session, i *discordgo.InteractionCreate) {
	fakeMsg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ChannelID: i.ChannelID,
			Author: &discordgo.User{
				ID:       i.Member.User.ID,
				Username: i.Member.User.Username,
			},
			Content: "!btc",
		},
	}
	r.HandleBitcoinPriceCommand(s, fakeMsg)
}

// handleSlashPrices обработчик команды /prices
func (r *Ranking) handleSlashPrices(s *discordgo.Session, i *discordgo.InteractionCreate) {
	fakeMsg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ChannelID: i.ChannelID,
			Author: &discordgo.User{
				ID:       i.Member.User.ID,
				Username: i.Member.User.Username,
			},
			Content: "!prices",
		},
	}
	r.HandlePriceStatsCommand(s, fakeMsg)
}

// handleSlashCaseBank обработчик команды /case_bank
func (r *Ranking) handleSlashCaseBank(s *discordgo.Session, i *discordgo.InteractionCreate) {
	fakeMsg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ChannelID: i.ChannelID,
			Author: &discordgo.User{
				ID:       i.Member.User.ID,
				Username: i.Member.User.Username,
			},
			Content: "!case_bank",
		},
	}
	r.HandleCaseBankCommand(s, fakeMsg)
}

// handleSlashDailyCase обработчик команды /daily_case
func (r *Ranking) handleSlashDailyCase(s *discordgo.Session, i *discordgo.InteractionCreate) {
	fakeMsg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ChannelID: i.ChannelID,
			Author: &discordgo.User{
				ID:       i.Member.User.ID,
				Username: i.Member.User.Username,
			},
			Content: "!daily_case",
		},
	}
	r.HandleDailyCaseCommand(s, fakeMsg)
}

// handleSlashChelp обработчик команды /chelp
func (r *Ranking) handleSlashChelp(s *discordgo.Session, i *discordgo.InteractionCreate) {
	fakeMsg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ChannelID: i.ChannelID,
			Author: &discordgo.User{
				ID:       i.Member.User.ID,
				Username: i.Member.User.Username,
			},
			Content: "!chelp",
		},
	}
	r.HandleChelpCommand(s, fakeMsg)
}

// handleSlashCaseHelp обработчик команды /case_help
func (r *Ranking) handleSlashCaseHelp(s *discordgo.Session, i *discordgo.InteractionCreate) {
	fakeMsg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ChannelID: i.ChannelID,
			Author: &discordgo.User{
				ID:       i.Member.User.ID,
				Username: i.Member.User.Username,
			},
			Content: "!case_help",
		},
	}
	r.HandleCaseHelpCommand(s, fakeMsg)
}

// handleSlashAdmin обработчик команды /admin
func (r *Ranking) handleSlashAdmin(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !r.IsAdmin(i.Member.User.ID) {
		content := "❌ Только товарищи-админы могут раздавать плюшки! 🔒"
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &content,
		})
		return
	}

	data := i.ApplicationCommandData()
	options := data.Options
	
	targetUser := options[0].UserValue(s)
	amount := int(options[1].IntValue())
	reason := ""
	if len(options) > 2 {
		reason = options[2].StringValue()
	}

	// Используем существующую логику
	command := fmt.Sprintf("!admin <@%s> %d", targetUser.ID, amount)
	if reason != "" {
		command += " " + reason
	}

	fakeMsg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ChannelID: i.ChannelID,
			Author: &discordgo.User{
				ID:       i.Member.User.ID,
				Username: i.Member.User.Username,
			},
			Content: command,
		},
	}
	r.HandleAdminCommand(s, fakeMsg, command)
}

// handleSlashUnknown обработчик неизвестной команды
func (r *Ranking) handleSlashUnknown(s *discordgo.Session, i *discordgo.InteractionCreate) {
	content := "❌ Неизвестная команда! Используйте `/chelp` для списка команд."
	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &content,
	})
}

// formatTimeForSlash вспомогательная функция для формата времени (чтобы избежать конфликта)
func (r *Ranking) formatTimeForSlash(seconds int) string {
	if seconds < 60 {
		return fmt.Sprintf("%d секунд", seconds)
	}
	minutes := seconds / 60
	seconds = seconds % 60
	if minutes < 60 {
		if seconds == 0 {
			return fmt.Sprintf("%d минут", minutes)
		}
		return fmt.Sprintf("%d минут %d секунд", minutes, seconds)
	}
	hours := minutes / 60
	minutes = minutes % 60
	if minutes == 0 && seconds == 0 {
		return fmt.Sprintf("%d часов", hours)
	}
	if seconds == 0 {
		return fmt.Sprintf("%d часов %d минут", hours, minutes)
	}
	return fmt.Sprintf("%d часов %d минут %d секунд", hours, minutes, seconds)
}

// formatReasonForSlash вспомогательная функция для формата причины (чтобы избежать конфликта)
func (r *Ranking) formatReasonForSlash(reason string) string {
	if reason == "" {
		return ""
	}
	return fmt.Sprintf(" (причина: %s)", reason)
}