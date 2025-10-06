package ranking

import (
	"encoding/json"
	"fmt"
	"log"
	"sort" // Added missing import
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/redis/go-redis/v9"
)

// HandleChinaCommand обрабатывает команду !china.
func (r *Ranking) HandleChinaCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Обработка !china от %s", m.Author.ID)
	parts := strings.Fields(m.Content)
	userID := m.Author.ID
	username := m.Author.Username

	if len(parts) > 1 {
		// Извлекаем ID из <@id> или <@!id>
		target := parts[1]
		target = strings.TrimPrefix(target, "<@")
		target = strings.TrimPrefix(target, "!")
		target = strings.TrimSuffix(target, ">")
		if target == "" || !isValidUserID(target) {
			s.ChannelMessageSend(m.ChannelID, "❌ Некорректный ID пользователя! Используй формат: `/china @id`")
			return
		}
		userID = target
		var err error
		username, err = getUsername(s, userID)
		if err != nil {
			username = "<@" + userID + ">"
		}
	}

	userRating := r.GetRating(userID)
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("💰 %s, баланс: **%d** соцкредитов! 🇨🇳", username, userRating))
}

// isValidUserID проверяет, является ли строка валидным ID пользователя.
func isValidUserID(id string) bool {
	if len(id) < 17 || len(id) > 20 { // Discord ID обычно 17–20 цифр
		return false
	}
	_, err := strconv.ParseUint(id, 10, 64)
	return err == nil
}

func (r *Ranking) HandleTransferCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Обработка перевода: %s от %s", command, m.Author.ID)
}

// HandleTopCommand обрабатывает команду !top.
func (r *Ranking) HandleTopCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Обработка !top от %s", m.Author.ID)
	topUsers := r.GetTop5()
	if len(topUsers) == 0 {
		s.ChannelMessageSend(m.ChannelID, "🏆 Пока нет лидеров! Будь первым! 😎")
		return
	}

	response := "🏆 **Топ-5 пользователей:**\n"
	for i, user := range topUsers {
		response += fmt.Sprintf("%d. <@%s> — %d кредитов\n", i+1, user.ID, user.Rating)
	}
	s.ChannelMessageSend(m.ChannelID, response)
}

// getUsername получает имя пользователя по ID.
func getUsername(s *discordgo.Session, userID string) (string, error) {
	user, err := s.User(userID)
	if err != nil {
		return "", err
	}
	return user.Username, nil
}

// formatTime форматирует время в секундах в читаемый вид.
func formatTime(seconds int) string {
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

// HandleAdminCommand обрабатывает команду !admin.
func (r *Ranking) HandleAdminCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Обработка !admin: %s от %s", command, m.Author.ID)

	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только товарищи-админы могут раздавать плюшки! 🔒")
		return
	}

	parts := strings.Fields(command)
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `/admin @id <сумма> [причина]`")
		return
	}

	targetID := strings.TrimPrefix(parts[1], "<@")
	targetID = strings.TrimSuffix(targetID, ">")
	targetID = strings.TrimPrefix(targetID, "!")

	amount, err := strconv.Atoi(parts[2])
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть числом! 💸")
		return
	}

	reason := ""
	if len(parts) > 3 {
		reason = strings.Join(parts[3:], " ")
	}

	r.UpdateRating(targetID, amount)
	targetUsername, err := getUsername(s, targetID)
	if err != nil {
		targetUsername = "<@" + targetID + ">"
	}
	var msg string
	if amount >= 0 {
		msg = fmt.Sprintf("✅ %s получил %d соцкредитов от админа! 🎉", targetUsername, amount)
	} else {
		msg = fmt.Sprintf("✅ У %s забрано %d соцкредитов админом! 🔽", targetUsername, -amount)
	}
	if reason != "" {
		msg += fmt.Sprintf("\n📝 Причина: %s", reason)
	}
	s.ChannelMessageSend(m.ChannelID, msg)
	r.LogCreditOperation(s, fmt.Sprintf("Админ <@%s> изменил баланс %s: %+d соцкредитов%s", m.Author.ID, targetUsername, amount, formatReason(reason)))
	log.Printf("Админ %s изменил рейтинг %s на %d (причина: %s)", m.Author.ID, targetID, amount, reason)
}

// HandleAdminMassCommand обрабатывает команду !adminmass.
func (r *Ranking) HandleAdminMassCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Обработка !adminmass: %s от %s", command, m.Author.ID)

	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только товарищи-админы могут выполнять массовые операции! 🔒")
		return
	}

	parts := strings.Fields(command)
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `/adminmass <+/-/=><сумма> @id1 @id2 ... [причина]`")
		return
	}

	operation := parts[1]
	if !strings.HasPrefix(operation, "+") && !strings.HasPrefix(operation, "-") && !strings.HasPrefix(operation, "=") {
		s.ChannelMessageSend(m.ChannelID, "❌ Операция должна начинаться с +, - или =!")
		return
	}
	amountStr := operation[1:]
	amount, err := strconv.Atoi(amountStr)
	if err != nil || amount < 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть неотрицательным числом!")
		return
	}

	var userIDs []string
	var reason string
	for i, part := range parts[2:] {
		if !strings.HasPrefix(part, "<@") {
			reason = strings.Join(parts[i+2:], " ")
			break
		}
		id := strings.TrimPrefix(part, "<@")
		id = strings.TrimSuffix(id, ">")
		id = strings.TrimPrefix(id, "!")
		userIDs = append(userIDs, id)
	}

	if len(userIDs) == 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ Укажи хотя бы одного пользователя!")
		return
	}

	response := "✅ Массовое изменение рейтинга выполнено:\n"
	for _, userID := range userIDs {
		username, err := getUsername(s, userID)
		if err != nil {
			username = "<@" + userID + ">"
		}
		switch operation[0] {
		case '+':
			r.UpdateRating(userID, amount)
			response += fmt.Sprintf("%s: +%d кредитов\n", username, amount)
			r.LogCreditOperation(s, fmt.Sprintf("Админ <@%s> добавил %d соцкредитов %s%s", m.Author.ID, amount, username, formatReason(reason)))
		case '-':
			r.UpdateRating(userID, -amount)
			response += fmt.Sprintf("%s: -%d кредитов\n", username, amount)
			r.LogCreditOperation(s, fmt.Sprintf("Админ <@%s> удалил %d соцкредитов у %s%s", m.Author.ID, amount, username, formatReason(reason)))
		case '=':
			currentRating := r.GetRating(userID)
			r.UpdateRating(userID, amount-currentRating)
			response += fmt.Sprintf("%s: установлено %d кредитов\n", username, amount)
			r.LogCreditOperation(s, fmt.Sprintf("Админ <@%s> установил %d соцкредитов для %s%s", m.Author.ID, amount, username, formatReason(reason)))
		}
	}
	if reason != "" {
		response += fmt.Sprintf("\n📝 Причина: %s", reason)
	}

	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("Админ %s выполнил массовое изменение рейтинга: %s", m.Author.ID, command)
}

// formatReason форматирует причину для логов.
func formatReason(reason string) string {
	if reason == "" {
		return ""
	}
	return fmt.Sprintf(" (причина: %s)", reason)
}

// HandleStatsCommand обрабатывает команду !stats.
func (r *Ranking) HandleStatsCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Обработка !stats от %s", m.Author.ID)

	parts := strings.Fields(m.Content)
	targetID := m.Author.ID
	targetUsername := m.Author.Username

	if len(parts) > 1 {
		targetID = strings.TrimPrefix(parts[1], "<@")
		targetID = strings.TrimSuffix(targetID, ">")
		targetID = strings.TrimPrefix(targetID, "!")
		if !isValidUserID(targetID) {
			s.ChannelMessageSend(m.ChannelID, "❌ Некорректный ID пользователя! Используй: `/stats [@id]`")
			return
		}
		var err error
		targetUsername, err = getUsername(s, targetID)
		if err != nil {
			targetUsername = "<@" + targetID + ">"
		}
	}

	user := User{ID: targetID}
	data, err := r.redis.Get(r.ctx, "user:"+targetID).Result()
	if err == redis.Nil {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ У пользователя %s нет статистики! 😢", targetUsername))
		return
	} else if err != nil {
		log.Printf("Не удалось получить данные пользователя %s из Redis: %v", targetID, err)
		s.ChannelMessageSend(m.ChannelID, "❌ Ошибка при загрузке статистики! Проверьте Redis-сервер.")
		return
	}

	if err := json.Unmarshal([]byte(data), &user); err != nil {
		log.Printf("Не удалось разобрать данные пользователя %s: %v", targetID, err)
		s.ChannelMessageSend(m.ChannelID, "❌ Ошибка при обработке данных пользователя!")
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("📊 Статистика %s", targetUsername),
		Description: "Твои достижения в мире соцкредитов! 🌟",
		Color:       0xFFD700, // Золотой цвет
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: "https://i.imgur.com/your-bot-icon.png", // Замени на иконку бота
		},
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
				Value:  fmt.Sprintf("**%s**", formatTime(user.VoiceSeconds)),
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Славь Императора! 👑 | Статистика обновляется в реальном времени",
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}
	s.ChannelMessageSendEmbed(m.ChannelID, embed)
}

// HandleChelpCommand обрабатывает команду !chelp.
func (r *Ranking) HandleChelpCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Обработка !chelp от %s", m.Author.ID)

	embed := &discordgo.MessageEmbed{
		Title:       "📜 Руководство по ChinaBot 🇨🇳",
		Description: "Добро пожаловать в мир соцкредитов! Вот команды, которые помогут тебе покорить рейтинг! 🚀",
		Color:       0xFFD700, // Золотой цвет
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: "https://i.imgur.com/your-bot-icon.png", // Замени на иконку бота
		},
		Fields: []*discordgo.MessageEmbedField{
			{Name: "💰 /china [@id]", Value: "Узнай свой баланс или баланс другого игрока.", Inline: false},
			{Name: "🏆 /top", Value: "Посмотри топ-5 пользователей по кредитам.", Inline: false},
			{Name: "📊 /stats", Value: "Проверь свою статистику: кредиты, игры, время в голосовых каналах.", Inline: false},
			{Name: "📊 /adminstats @id <игра> <поле> <значение>", Value: "Измените статистику игрока (только админы).", Inline: false},
			{Name: "📜 /transfer @id <сумма> <причина>", Value: "Передать кредиты другому", Inline: false},
			{Name: "📝 /cpoll Вопрос [Вариант1] [Вариант2] ...", Value: "Создай опрос (только админы).", Inline: false},
			{Name: "💸 /dep <ID_опроса> <номер_варианта> <сумма>", Value: "Поставь кредиты на вариант в опросе.", Inline: false},
			{Name: "🔒 /closedep <ID_опроса> <номер>", Value: "Закрой опрос и распредели выигрыши (только админы).", Inline: false},
			{Name: "📋 /polls", Value: "Посмотри активные опросы.", Inline: false},
			{Name: "🎰 /rb", Value: "Начни игру в Красный-Чёрный.", Inline: false},
			{Name: "🔴⚫ /rb <red/black> <сумма>", Value: "Сделай ставку в Красный-Чёрный.", Inline: false},
			{Name: "♠️ /blackjack", Value: "Начни игру в Блэкджек.", Inline: false},
			{Name: "🎲 /blackjack <сумма>", Value: "Сделай ставку в Блэкджеке.", Inline: false},
			{Name: "⚔️ /duel <сумма>", Value: "Вызови любого на дуэль с указанной ставкой.", Inline: false},
			{Name: "🎁 /admin @id <сумма> [причина]", Value: "Начисли или забери кредиты у пользователя (только админы).", Inline: false},
			{Name: "⚙️ /adminmass <+/-/=сумма> @id1 @id2 ... [причина]", Value: "Массовое изменение рейтинга (только админы).", Inline: false},
			{Name: "🚫 /endblackjack @id", Value: "Заверши игру в Блэкджек пользователя (только админы).", Inline: false},
			{Name: "📜 /chelp", Value: "Покажи это руководство.", Inline: false},
			{Name: "🎥 /cinema <название> <сумма>", Value: "Предложить новый вариант на киноаукцион.", Inline: false},
			{Name: "🎥 /betcinema <номер> <сумма>", Value: "Поставить на существующий вариант.", Inline: false},
			{Name: "📋 /cinemalist", Value: "Посмотреть актуальные варианты.", Inline: false},
			{Name: "📋 /admincinemalist", Value: "Детальный список вариантов (админы).", Inline: false},
			{Name: "🗑️ /removelowest <число>", Value: "Удалить <число> самых низких вариантов (админы).", Inline: false},
			{Name: "⚙️ /adjustcinema <номер> <+/-сумма>", Value: "Корректировать сумму любого кино-варианта (админы).", Inline: false},
			{Name: "🗑️ /removecinema @id <номер>", Value: "Удалить вариант, предложенный пользователем (админы).", Inline: false},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Славь Императора и собирай кредиты! 👑 | Бот создан для веселья и рейтингов",
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}
	s.ChannelMessageSendEmbed(m.ChannelID, embed)
}

// InventoryStats хранит статистику инвентаря пользователя
type InventoryStats struct {
	UserID     string
	TotalValue int
	RareCount  int // Nephrite + Exotic + Legendary
}

// HandleTopInventoriesCommand отображает топ-10 инвентарей по стоимости
func (r *Ranking) HandleTopInventoriesCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Starting HandleTopInventoriesCommand")
	var cursor uint64
	var inventories []InventoryStats

	// Сканируем все ключи inventory:*
	for {
		keys, newCursor, err := r.redis.Scan(r.ctx, cursor, "inventory:*", 100).Result()
		if err != nil {
			log.Printf("Error scanning inventories: %v", err)
			_, err = s.ChannelMessageSend(m.ChannelID, "❌ **Ошибка загрузки топа инвентарей!**")
			if err != nil {
				log.Printf("Error sending error message: %v", err)
			}
			return
		}
		cursor = newCursor

		for _, key := range keys {
			userID := strings.TrimPrefix(key, "inventory:")
			inv := r.GetUserInventory(userID)
			var totalValue int
			var rareCount int
			for nftID, count := range inv {
				nft, ok := r.Kki.nfts[nftID]
				if !ok {
					log.Printf("Warning: NFT %s not found for user %s", nftID, userID)
					continue
				}
				value := r.CalculateNFTPrice(nft) * count
				totalValue += value
				if nft.Rarity == "Nephrite" || nft.Rarity == "Exotic" || nft.Rarity == "Legendary" {
					rareCount += count
				}
			}
			if totalValue > 0 {
				inventories = append(inventories, InventoryStats{UserID: userID, TotalValue: totalValue, RareCount: rareCount})
			}
		}

		if cursor == 0 {
			break
		}
	}

	// Сортируем по totalValue (убывание)
	sort.Slice(inventories, func(i, j int) bool {
		return inventories[i].TotalValue > inventories[j].TotalValue // Fixed sorting bug
	})

	// Ограничиваем топ-10
	if len(inventories) > 10 {
		inventories = inventories[:10]
	}

	if len(inventories) == 0 {
		log.Printf("No inventories found for top")
		_, err := s.ChannelMessageSend(m.ChannelID, "🏆 **Топ инвентарей пуст** ══════\nИмператор ждёт богатых коллекционеров! 😢")
		if err != nil {
			log.Printf("Error sending empty top inventories message: %v", err)
		}
		return
	}

	// Формируем список
	var lines []string
	for i, stats := range inventories {
		// Попытка получить имя пользователя
		user, err := s.User(stats.UserID)
		username := "Неизвестный"
		if err == nil {
			username = user.Username
		} else {
			log.Printf("Error fetching username for user %s: %v", stats.UserID, err)
		}
		lines = append(lines, fmt.Sprintf("%d. **%s** - 💰 %d (Редких: %d)", i+1, username, stats.TotalValue, stats.RareCount))
	}

	embed := &discordgo.MessageEmbed{
		Title:       "🏆 **Топ-10 богатых инвентарей** ══════",
		Description: strings.Join(lines, "\n"),
		Color:       0xFFD700,
		Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Вызвал: %s | Славь Императора! 👑", m.Author.Username)},
	}
	_, err := s.ChannelMessageSendEmbed(m.ChannelID, embed)
	if err != nil {
		log.Printf("Error sending top inventories embed: %v", err)
		s.ChannelMessageSend(m.ChannelID, "❌ **Ошибка отображения топа!**")
	} else {
		log.Printf("Top inventories sent successfully")
	}
}
