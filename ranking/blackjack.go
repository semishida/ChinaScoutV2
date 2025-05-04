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

// Card представляет карту в блэкджеке.
type Card struct {
	Suit  string
	Value string
}

// BlackjackGame представляет игру в блэкджек.
type BlackjackGame struct {
	GameID        string
	PlayerID      string
	Bet           int
	PlayerCards   []Card
	DealerCards   []Card
	Active        bool
	LastActivity  time.Time
	MenuMessageID string
	Color         int
	ChannelID     string
}

// StartBlackjackGame начинает новую игру в блэкджек.
func (r *Ranking) StartBlackjackGame(s *discordgo.Session, m *discordgo.MessageCreate) {
	r.mu.Lock()
	gameID := generateGameID(m.Author.ID)
	color := randomColor()
	game := &BlackjackGame{
		GameID:       gameID,
		PlayerID:     m.Author.ID,
		Active:       true,
		LastActivity: time.Now(),
		Color:        color,
		ChannelID:    m.ChannelID,
	}
	r.blackjackGames[gameID] = game
	r.mu.Unlock()

	embed := &discordgo.MessageEmbed{
		Title:       "♠️ Казино: Блэкджек 🎰",
		Description: fmt.Sprintf("Добро пожаловать, <@%s>! 🎉\nСделай ставку, чтобы начать игру.\n\n**💰 Твой баланс:** %d кредитов\n\nНапиши: `!blackjack <сумма>`", m.Author.ID, r.GetRating(m.Author.ID)),
		Color:       color,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Играй с умом! 🍀",
		},
	}
	msg, err := s.ChannelMessageSendEmbed(m.ChannelID, embed)
	if err != nil {
		log.Printf("Не удалось отправить меню блэкджека: %v", err)
		return
	}

	r.mu.Lock()
	game.MenuMessageID = msg.ID
	r.mu.Unlock()

	go r.blackjackTimeout(s, gameID)
}

// HandleBlackjackBet обрабатывает ставку в блэкджеке.
func (r *Ranking) HandleBlackjackBet(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	parts := strings.Fields(command)
	if len(parts) != 2 {
		r.sendTemporaryReply(s, m, "❌ Используй: `!blackjack <сумма>`\nПример: `!blackjack 50`")
		return
	}

	amount, err := strconv.Atoi(parts[1])
	if err != nil || amount <= 0 {
		r.sendTemporaryReply(s, m, "❌ Сумма должна быть положительным числом!")
		return
	}

	userRating := r.GetRating(m.Author.ID)
	if userRating < amount {
		r.sendTemporaryReply(s, m, fmt.Sprintf("❌ Недостаточно кредитов! Твой баланс: %d", userRating))
		return
	}

	r.mu.Lock()
	var game *BlackjackGame
	for _, g := range r.blackjackGames {
		if g.PlayerID == m.Author.ID && g.Active && g.Bet == 0 {
			game = g
			break
		}
	}
	if game == nil {
		r.sendTemporaryReply(s, m, "❌ Начни игру с помощью `!blackjack`!")
		r.mu.Unlock()
		return
	}

	game.Bet = amount
	game.LastActivity = time.Now()
	r.mu.Unlock()

	r.UpdateRating(m.Author.ID, -amount)

	suits := []string{"♠️", "♥️", "♦️", "♣️"}
	values := []string{"2", "3", "4", "5", "6", "7", "8", "9", "10", "J", "Q", "K", "A"}
	deck := make([]Card, 0, 52)
	for _, suit := range suits {
		for _, value := range values {
			deck = append(deck, Card{Suit: suit, Value: value})
		}
	}
	rand.Shuffle(len(deck), func(i, j int) { deck[i], deck[j] = deck[j], deck[i] })

	playerCards := []Card{deck[0], deck[1]}
	dealerCards := []Card{deck[2], deck[3]}

	r.mu.Lock()
	game.PlayerCards = playerCards
	game.DealerCards = dealerCards
	game.LastActivity = time.Now()
	r.mu.Unlock()

	embed := &discordgo.MessageEmbed{
		Title:       "♠️ Блэкджек 🎲",
		Description: fmt.Sprintf("<@%s> начал игру со ставкой %d кредитов! 💸\n\n**🃏 Твои карты:** %s (Сумма: %d)\n**🃏 Карты дилера:** %s [Скрытая карта]", m.Author.ID, amount, r.cardsToString(playerCards), r.calculateHand(playerCards), r.cardToString(dealerCards[0])),
		Color:       game.Color,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Сделай ход! 🍀",
		},
	}
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{Label: "Взять карту 🃏", Style: discordgo.PrimaryButton, CustomID: fmt.Sprintf("blackjack_hit_%s", game.GameID)},
				discordgo.Button{Label: "Остановиться ⏹️", Style: discordgo.SecondaryButton, CustomID: fmt.Sprintf("blackjack_stand_%s", game.GameID)},
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
		log.Printf("Не удалось обновить сообщение игры в блэкджек: %v", err)
	}
}

// HandleBlackjackHit обрабатывает действие "взять карту".
func (r *Ranking) HandleBlackjackHit(s *discordgo.Session, i *discordgo.InteractionCreate) {
	parts := strings.Split(i.MessageComponentData().CustomID, "_")
	if len(parts) < 3 {
		log.Printf("Неверный формат CustomID: %s", i.MessageComponentData().CustomID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Ошибка: неверный формат кнопки!", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}
	gameID := strings.Join(parts[2:], "_")

	r.mu.Lock()
	game, exists := r.blackjackGames[gameID]
	if !exists {
		log.Printf("Игра не найдена для GameID: %s", gameID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Игра не найдена!", Flags: discordgo.MessageFlagsEphemeral},
		})
		r.mu.Unlock()
		return
	}
	if !game.Active {
		log.Printf("Игра неактивна для GameID: %s, PlayerID: %s", gameID, game.PlayerID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Игра завершена!", Flags: discordgo.MessageFlagsEphemeral},
		})
		r.mu.Unlock()
		return
	}

	deck := r.generateDeck()
	newCard := deck[len(game.PlayerCards)+len(game.DealerCards)]
	game.PlayerCards = append(game.PlayerCards, newCard)
	game.LastActivity = time.Now()
	playerSum := r.calculateHand(game.PlayerCards)

	embed := &discordgo.MessageEmbed{
		Title: "♠️ Блэкджек 🎲",
		Color: game.Color,
	}
	var components []discordgo.MessageComponent
	if playerSum > 21 {
		game.Active = false
		embed.Description = fmt.Sprintf("Ты взял карту: %s\n**🃏 Твои карты:** %s (Сумма: %d)\n**🃏 Карты дилера:** %s [Скрытая]\n\n❌ Перебор! Ты проиграл! 💥", r.cardToString(newCard), r.cardsToString(game.PlayerCards), playerSum, r.cardToString(game.DealerCards[0]))
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Не повезло! 😢"}
		components = []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Label:    "Сыграть снова 🎮",
						Style:    discordgo.PrimaryButton,
						CustomID: fmt.Sprintf("blackjack_replay_%s_%s", game.PlayerID, game.MenuMessageID),
					},
				},
			},
		}
		// Обновляем статистику Blackjack (проигрыш)
		r.UpdateBJStats(game.PlayerID, false)
		delete(r.blackjackGames, gameID)
	} else {
		embed.Description = fmt.Sprintf("Ты взял карту: %s\n**🃏 Твои карты:** %s (Сумма: %d)\n**🃏 Карты дилера:** %s [Скрытая]", r.cardToString(newCard), r.cardsToString(game.PlayerCards), playerSum, r.cardToString(game.DealerCards[0]))
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Продолжаем! 🍀"}
		components = []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{Label: "Взять карту 🃏", Style: discordgo.PrimaryButton, CustomID: fmt.Sprintf("blackjack_hit_%s", game.GameID)},
					discordgo.Button{Label: "Остановиться ⏹️", Style: discordgo.SecondaryButton, CustomID: fmt.Sprintf("blackjack_stand_%s", game.GameID)},
				},
			},
		}
	}
	r.mu.Unlock()

	_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    i.ChannelID,
		ID:         game.MenuMessageID,
		Embed:      embed,
		Components: &components,
	})
	if err != nil {
		log.Printf("Не удалось обновить сообщение блэкджека: %v", err)
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate})
}

// HandleBlackjackStand обрабатывает действие "остановиться".
func (r *Ranking) HandleBlackjackStand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	parts := strings.Split(i.MessageComponentData().CustomID, "_")
	if len(parts) < 3 {
		log.Printf("Неверный формат CustomID: %s", i.MessageComponentData().CustomID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Ошибка: неверный формат кнопки!", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}
	gameID := strings.Join(parts[2:], "_")

	r.mu.Lock()
	game, exists := r.blackjackGames[gameID]
	if !exists {
		log.Printf("Игра не найдена для GameID: %s", gameID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Игра не найдена!", Flags: discordgo.MessageFlagsEphemeral},
		})
		r.mu.Unlock()
		return
	}
	if !game.Active {
		log.Printf("Игра неактивна для GameID: %s, PlayerID: %s", gameID, game.PlayerID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Игра завершена!", Flags: discordgo.MessageFlagsEphemeral},
		})
		r.mu.Unlock()
		return
	}

	game.LastActivity = time.Now()
	playerSum := r.calculateHand(game.PlayerCards)
	dealerSum := r.calculateHand(game.DealerCards)

	deck := r.generateDeck()
	cardIndex := len(game.PlayerCards) + len(game.DealerCards)
	for dealerSum < 17 && cardIndex < len(deck) {
		game.DealerCards = append(game.DealerCards, deck[cardIndex])
		dealerSum = r.calculateHand(game.DealerCards)
		cardIndex++
	}

	embed := &discordgo.MessageEmbed{
		Title:       "♠️ Блэкджек 🎲",
		Description: fmt.Sprintf("**🃏 Твои карты:** %s (Сумма: %d)\n**🃏 Карты дилера:** %s (Сумма: %d)", r.cardsToString(game.PlayerCards), playerSum, r.cardsToString(game.DealerCards), dealerSum),
		Color:       game.Color,
	}

	var result string
	won := false
	if dealerSum > 21 {
		winnings := game.Bet * 2
		r.UpdateRating(game.PlayerID, winnings)
		result = fmt.Sprintf("✅ Дилер перебрал! Ты выиграл %d кредитов! 🎉", winnings)
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Победа! 🏆"}
		won = true
	} else if playerSum > dealerSum {
		winnings := game.Bet * 2
		r.UpdateRating(game.PlayerID, winnings)
		result = fmt.Sprintf("✅ Ты выиграл! %d кредитов твои! 🎉", winnings)
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Победа! 🏆"}
		won = true
	} else if playerSum == dealerSum {
		r.UpdateRating(game.PlayerID, game.Bet)
		result = "🤝 Ничья! Твоя ставка возвращена. 🔄"
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Ничья! 🤝"}
	} else {
		result = "❌ Дилер победил! 💥"
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Не повезло! 😢"}
	}

	embed.Description += fmt.Sprintf("\n\n%s", result)

	// Обновляем статистику Blackjack
	r.UpdateBJStats(game.PlayerID, won)

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Сыграть снова 🎮",
					Style:    discordgo.PrimaryButton,
					CustomID: fmt.Sprintf("blackjack_replay_%s_%s", game.PlayerID, game.MenuMessageID),
				},
			},
		},
	}

	game.Active = false
	delete(r.blackjackGames, gameID)
	r.mu.Unlock()

	_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    i.ChannelID,
		ID:         game.MenuMessageID,
		Embed:      embed,
		Components: &components,
	})
	if err != nil {
		log.Printf("Не удалось обновить сообщение блэкджека: %v", err)
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate})
}

// HandleBlackjackReplay начинает новую игру в блэкджек.
func (r *Ranking) HandleBlackjackReplay(s *discordgo.Session, i *discordgo.InteractionCreate) {
	parts := strings.Split(i.MessageComponentData().CustomID, "_")
	if len(parts) != 4 {
		log.Printf("Неверный формат CustomID: %s", i.MessageComponentData().CustomID)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Ошибка: неверный формат кнопки!", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}
	playerID := parts[2]
	menuMessageID := parts[3]

	newGameID := generateGameID(playerID)
	newColor := randomColor()
	game := &BlackjackGame{
		GameID:        newGameID,
		PlayerID:      playerID,
		Active:        true,
		LastActivity:  time.Now(),
		Color:         newColor,
		ChannelID:     i.ChannelID,
		MenuMessageID: menuMessageID,
	}

	r.mu.Lock()
	r.blackjackGames[newGameID] = game
	r.mu.Unlock()

	embed := &discordgo.MessageEmbed{
		Title:       "♠️ Казино: Блэкджек 🎰",
		Description: fmt.Sprintf("Добро пожаловать, <@%s>! 🎉\nСделай ставку, чтобы начать игру.\n\n**💰 Твой баланс:** %d кредитов\n\nНапиши: `!blackjack <сумма>`", playerID, r.GetRating(playerID)),
		Color:       newColor,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Играй с умом! 🍀",
		},
	}

	_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    i.ChannelID,
		ID:         menuMessageID,
		Embed:      embed,
		Components: &[]discordgo.MessageComponent{},
	})
	if err != nil {
		log.Printf("Не удалось обновить меню блэкджека: %v", err)
	}

	go r.blackjackTimeout(s, newGameID)

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate})
}

// HandleEndBlackjackCommand завершает игру в блэкджек по команде администратора.
func (r *Ranking) HandleEndBlackjackCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Обработка !endblackjack: %s от %s", command, m.Author.ID)

	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только админы могут завершать игры! 🔒")
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
	var game *BlackjackGame
	for _, g := range r.blackjackGames {
		if g.PlayerID == targetID && g.Active {
			game = g
			break
		}
	}
	if game == nil {
		s.ChannelMessageSend(m.ChannelID, "❌ У этого пользователя нет активной игры в блэкджек! ♠️")
		r.mu.Unlock()
		return
	}

	game.Active = false
	delete(r.blackjackGames, game.GameID)
	r.mu.Unlock()

	embed := &discordgo.MessageEmbed{
		Title:       "♠️ Блэкджек 🎲",
		Description: fmt.Sprintf("Игра завершена админом: <@%s>! 🚫", targetID),
		Color:       game.Color,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Игра остановлена! 🔴",
		},
	}
	_, err := s.ChannelMessageEditEmbed(game.ChannelID, game.MenuMessageID, embed)
	if err != nil {
		log.Printf("Не удалось обновить сообщение блэкджека: %v", err)
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("✅ Игра в блэкджек для <@%s> завершена!", targetID))
	log.Printf("Игра в блэкджек для %s завершена админом %s", targetID, m.Author.ID)
}

// blackjackTimeout завершает игру по тайм-ауту.
func (r *Ranking) blackjackTimeout(s *discordgo.Session, gameID string) {
	time.Sleep(15 * time.Minute)
	r.mu.Lock()
	game, exists := r.blackjackGames[gameID]
	if !exists || !game.Active {
		r.mu.Unlock()
		return
	}
	game.Active = false
	delete(r.blackjackGames, gameID)
	r.mu.Unlock()

	embed := &discordgo.MessageEmbed{
		Title:       "♠️ Блэкджек 🎲",
		Description: fmt.Sprintf("Игра завершена, <@%s>! Время вышло! ⏰", game.PlayerID),
		Color:       game.Color,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Время вышло! 😢",
		},
	}
	_, err := s.ChannelMessageEditEmbed(game.ChannelID, game.MenuMessageID, embed)
	if err != nil {
		log.Printf("Не удалось обновить сообщение блэкджека по тайм-ауту: %v", err)
	}
}

// generateDeck создаёт колоду карт.
func (r *Ranking) generateDeck() []Card {
	suits := []string{"♠️", "♥️", "♦️", "♣️"}
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

// calculateHand вычисляет сумму очков руки.
func (r *Ranking) calculateHand(cards []Card) int {
	sum := 0
	aces := 0
	for _, card := range cards {
		switch card.Value {
		case "A":
			aces++
		case "J", "Q", "K":
			sum += 10
		default:
			val, _ := strconv.Atoi(card.Value)
			sum += val
		}
	}
	for i := 0; i < aces; i++ {
		if sum+11 <= 21 {
			sum += 11
		} else {
			sum += 1
		}
	}
	return sum
}

// cardsToString преобразует массив карт в строку.
func (r *Ranking) cardsToString(cards []Card) string {
	var result []string
	for _, card := range cards {
		result = append(result, card.Suit+card.Value)
	}
	return strings.Join(result, ", ")
}

// cardToString преобразует одну карту в строку.
func (r *Ranking) cardToString(card Card) string {
	return card.Suit + card.Value
}

// sendTemporaryReply отправляет временное сообщение.
func (r *Ranking) sendTemporaryReply(s *discordgo.Session, m *discordgo.MessageCreate, content string) {
	msg, err := s.ChannelMessageSend(m.ChannelID, content)
	if err != nil {
		log.Printf("Не удалось отправить временное сообщение: %v", err)
		return
	}
	time.Sleep(10 * time.Second)
	s.ChannelMessageDelete(m.ChannelID, msg.ID)
}
