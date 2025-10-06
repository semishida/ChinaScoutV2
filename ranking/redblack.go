package ranking

import (
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// RedBlackGame представляет игру RedBlack.
type RedBlackGame struct {
	GameID        string
	PlayerID      string
	Bet           int
	Choice        string
	Active        bool
	MenuMessageID string
	Color         int
}

// StartRBGame начинает новую игру RedBlack.
func (r *Ranking) StartRBGame(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Запуск StartRBGame для пользователя %s", m.Author.ID)

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
		Description: fmt.Sprintf("Велком, <@%s>! 🥳\nИмператор велит: выбирать цвет и ставка делай!\n\n**💰 Баланса твоя:** %d кредитов\n\nПиши вот: `/rb <red/black> <сумма>`\nНапример: `/rb red 50`\nИмператор следит за тобой! 👑", m.Author.ID, r.GetRating(m.Author.ID)),
		Color:       color,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Славь Императора и везёт тебе! 🍀",
		},
	}
	msg, err := s.ChannelMessageSendEmbed(m.ChannelID, embed)
	if err != nil {
		log.Printf("Не удалось отправить меню RB: %v", err)
		return
	}

	r.mu.Lock()
	game.MenuMessageID = msg.ID
	r.mu.Unlock()

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
				log.Printf("Не удалось обновить сообщение RB по тайм-ауту: %v", err)
			}
		}
		r.mu.Unlock()
	}(msg.ID, m.ChannelID)
}

// HandleRBCommand обрабатывает команду ставки в игре RedBlack.
func (r *Ranking) HandleRBCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Fields(command)
	if len(parts) < 3 {
		r.sendTemporaryReply(s, m, "❌ Пиши правильно: `/rb <red/black> <сумма>`")
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
		r.sendTemporaryReply(s, m, "❌ Игру начинай с `/rb`! Император ждёт тебя! 👑")
		r.mu.Unlock()
		return
	}

	game.Bet = amount
	game.Choice = choice
	r.mu.Unlock()

	r.UpdateRating(m.Author.ID, -amount)

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
		log.Printf("Не удалось обновить сообщение RB: %v", err)
		return
	}

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
			log.Printf("Не удалось обновить анимацию RB: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	result := "red"
	if rand.Intn(2) == 1 {
		result = "black"
	}
	colorEmoji := "🔴"
	if result == "black" {
		colorEmoji = "⚫"
	}

	embed.Description = fmt.Sprintf("<@%s> ставка делай %d кредитов на %s!\n\n🎲 Результат: %s", m.Author.ID, amount, choice, colorEmoji)
	won := result == choice
	if won {
		winnings := amount * 2
		r.UpdateRating(m.Author.ID, winnings)
		embed.Description += fmt.Sprintf("\n\n✅ Победа! Император доволен! Ты бери %d кредитов! 🎉", winnings)
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Император хвалит тебя! 🏆"}
	} else {
		embed.Description += fmt.Sprintf("\n\n❌ Проиграл! Император гневен! Потерял: %d кредитов. 😢", amount)
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Император недоволен! 😡"}
	}

	// Обновляем статистику RedBlack
	r.UpdateRBStats(m.Author.ID, won)

	customID := fmt.Sprintf("rb_replay_%s_%d", game.PlayerID, time.Now().UnixNano())
	log.Printf("Установка CustomID кнопки: %s", customID)
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
		ID:         game.MenuMessageID,
		Embed:      embed,
		Components: &components,
	})
	if err != nil {
		log.Printf("Не удалось отредактировать сообщение RB с кнопкой: %v", err)
		return
	}

	r.mu.Lock()
	game.Active = false
	delete(r.redBlackGames, game.GameID)
	r.mu.Unlock()

	go func(messageID string, channelID string) {
		time.Sleep(15 * time.Minute)
		r.mu.Lock()
		var activeGame *RedBlackGame
		for _, g := range r.redBlackGames {
			if g.MenuMessageID == messageID && g.Active {
				activeGame = g
				break
			}
		}
		if activeGame == nil {
			emptyComponents := []discordgo.MessageComponent{}
			_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
				Channel:    channelID,
				ID:         messageID,
				Embed:      embed,
				Components: &emptyComponents,
			})
			if err != nil {
				log.Printf("Не удалось отключить кнопку RB: %v", err)
			}
			log.Printf("Кнопка RB отключена для сообщения %s", messageID)
		}
		r.mu.Unlock()
	}(game.MenuMessageID, m.ChannelID)
}

// HandleRBReplay обрабатывает повторную игру RedBlack.
func (r *Ranking) HandleRBReplay(s *discordgo.Session, i *discordgo.InteractionCreate) {
	log.Printf("Обработка HandleRBReplay, CustomID: %s", i.MessageComponentData().CustomID)

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})
	if err != nil {
		log.Printf("Не удалось ответить на взаимодействие: %v", err)
		return
	}
	log.Printf("Отправлен ответ на взаимодействие для игрока %s", i.Member.User.ID)

	parts := strings.Split(i.MessageComponentData().CustomID, "_")
	if len(parts) != 4 {
		log.Printf("Неверный формат CustomID: %s, ожидалось 4 части, получено %d", i.MessageComponentData().CustomID, len(parts))
		_, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "❌ Ошибка: кнопка сломана! Император гневен! 😡",
			Flags:   discordgo.MessageFlagsEphemeral,
		})
		if err != nil {
			log.Printf("Не удалось отправить последующее сообщение: %v", err)
		}
		return
	}
	playerID := parts[2]
	log.Printf("Разобран playerID: %s", playerID)

	if playerID != i.Member.User.ID {
		log.Printf("Несоответствие playerID: ожидалось %s, получено %s", playerID, i.Member.User.ID)
		_, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "❌ Кнопка не твоя! Император не позволит! 👑",
			Flags:   discordgo.MessageFlagsEphemeral,
		})
		if err != nil {
			log.Printf("Не удалось отправить последующее сообщение: %v", err)
		}
		return
	}

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
	log.Printf("Создана новая игра RB с ID %s для игрока %s", newGameID, playerID)

	embed := &discordgo.MessageEmbed{
		Title:       "🎰 Игра: Красный-Чёрный",
		Description: fmt.Sprintf("Велком снова, <@%s>! 🥳\nИмператор даёт шанс: выбирать цвет и ставка делай!\n\n**💰 Баланса твоя:** %d кредитов\n\nПиши вот: `/rb <red/black> <сумма>`\nНапример: `/rb red 50`\nИмператор следит за тобой! 👑", playerID, r.GetRating(playerID)),
		Color:       newColor,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Славь Императора и везёт тебе! 🍀",
		},
	}
	log.Printf("Редактирование существующего embed RB для игрока %s, ID сообщения: %s", playerID, i.Message.ID)

	emptyComponents := []discordgo.MessageComponent{}
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    i.ChannelID,
		ID:         i.Message.ID,
		Embed:      embed,
		Components: &emptyComponents,
	})
	if err != nil {
		log.Printf("Не удалось отредактировать меню RB: %v", err)
		_, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "❌ Ошибка! Игру не обновить! Император гневен! Проверь права бота! 😡",
			Flags:   discordgo.MessageFlagsEphemeral,
		})
		if err != nil {
			log.Printf("Не удалось отправить последующее сообщение: %v", err)
		}
		return
	}
	log.Printf("Embed RB успешно отредактирован для игрока %s, ID сообщения: %s", playerID, i.Message.ID)

	r.mu.Lock()
	game.MenuMessageID = i.Message.ID
	r.mu.Unlock()

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
				log.Printf("Не удалось обновить сообщение RB по тайм-ауту: %v", err)
			}
		}
		r.mu.Unlock()
	}(i.Message.ID, i.ChannelID)
}
