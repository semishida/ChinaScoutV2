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

// Duel представляет дуэль между игроками.
type Duel struct {
	DuelID       string
	ChallengerID string
	OpponentID   string
	Bet          int
	Active       bool
	ChannelID    string
	MessageID    string
	Created      time.Time
}

// HandleDuelCommand обрабатывает команду !duel.
func (r *Ranking) HandleDuelCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Обработка !duel: %s от %s", command, m.Author.ID)

	parts := strings.Fields(command)
	if len(parts) != 2 {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `/duel <сумма>`")
		return
	}

	bet, err := strconv.Atoi(parts[1])
	if err != nil || bet <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть положительным числом!")
		return
	}

	userRating := r.GetRating(m.Author.ID)
	if userRating < bet {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Недостаточно кредитов! Твой баланс: %d", userRating))
		return
	}

	duelID := generateGameID(m.Author.ID)
	r.mu.Lock()
	duel := &Duel{
		DuelID:       duelID,
		ChallengerID: m.Author.ID,
		Bet:          bet,
		Active:       true,
		ChannelID:    m.ChannelID,
		Created:      time.Now(),
	}
	r.duels[duelID] = duel
	r.mu.Unlock()

	embed := &discordgo.MessageEmbed{
		Title:       "⚔️ Дуэль! ⚔️",
		Description: fmt.Sprintf("<@%s> вызывает на дуэль с ставкой **%d** кредитов! 💸\n\nНажми **Принять**, чтобы сразиться!", m.Author.ID, bet),
		Color:       randomColor(),
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Только смелые принимают вызов! 🛡️",
		},
	}
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Принять 🛡️",
					Style:    discordgo.PrimaryButton,
					CustomID: fmt.Sprintf("duel_accept_%s", duelID),
				},
			},
		},
	}

	msg, err := s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
		Embed:      embed,
		Components: components,
	})
	if err != nil {
		log.Printf("Не удалось отправить сообщение дуэли: %v", err)
		return
	}

	r.mu.Lock()
	duel.MessageID = msg.ID
	r.mu.Unlock()

	go r.duelTimeout(s, duelID)
}

// HandleDuelAccept обрабатывает нажатие кнопки "Принять".
func (r *Ranking) HandleDuelAccept(s *discordgo.Session, i *discordgo.InteractionCreate) {
	customID := i.MessageComponentData().CustomID
	log.Printf("Обработка кнопки дуэли, CustomID: %s", customID)

	if !strings.HasPrefix(customID, "duel_accept_") {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Ошибка: неверный формат кнопки!", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}

	duelID := strings.TrimPrefix(customID, "duel_accept_")
	log.Printf("Извлечён duelID: %s", duelID)

	r.mu.Lock()
	duel, exists := r.duels[duelID]
	if !exists {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Дуэль не найдена!", Flags: discordgo.MessageFlagsEphemeral},
		})
		r.mu.Unlock()
		return
	}
	if !duel.Active {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Дуэль уже завершена!", Flags: discordgo.MessageFlagsEphemeral},
		})
		r.mu.Unlock()
		return
	}
	if i.Member.User.ID == duel.ChallengerID {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Нельзя принять свою дуэль!", Flags: discordgo.MessageFlagsEphemeral},
		})
		r.mu.Unlock()
		return
	}

	opponentRating := r.GetRating(i.Member.User.ID)
	if opponentRating < duel.Bet {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: fmt.Sprintf("❌ Недостаточно кредитов! Твой баланс: %d", opponentRating), Flags: discordgo.MessageFlagsEphemeral},
		})
		r.mu.Unlock()
		return
	}

	duel.OpponentID = i.Member.User.ID
	duel.Active = false
	r.mu.Unlock()

	r.UpdateRating(duel.ChallengerID, -duel.Bet)
	r.UpdateRating(duel.OpponentID, -duel.Bet)

	rand.Seed(time.Now().UnixNano())
	winnerID := duel.ChallengerID
	loserID := duel.OpponentID
	if rand.Intn(2) == 1 {
		winnerID, loserID = loserID, winnerID
	}

	winnings := duel.Bet * 2
	r.UpdateRating(winnerID, winnings)
	r.UpdateDuelStats(winnerID, true)
	r.UpdateDuelStats(loserID, false)

	embed := &discordgo.MessageEmbed{
		Title:       "⚔️ Дуэль завершена! ⚔️",
		Description: fmt.Sprintf("<@%s> принял вызов <@%s>!\n\n🏆 **Победитель:** <@%s> (+%d кредитов)\n😢 **Проигравший:** <@%s> (-%d кредитов)", duel.OpponentID, duel.ChallengerID, winnerID, winnings, loserID, duel.Bet),
		Color:       randomColor(),
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Славь Императора! 👑",
		},
	}

	_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    duel.ChannelID,
		ID:         duel.MessageID,
		Embed:      embed,
		Components: &[]discordgo.MessageComponent{},
	})
	if err != nil {
		log.Printf("Не удалось обновить сообщение дуэли: %v", err)
	}

	r.LogCreditOperation(s, fmt.Sprintf("<@%s> выиграл %d соц кредитов у <@%s> в дуэли", winnerID, winnings, loserID))

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate})

	r.mu.Lock()
	delete(r.duels, duelID)
	r.mu.Unlock()
}

// duelTimeout завершает дуэль по тайм-ауту.
func (r *Ranking) duelTimeout(s *discordgo.Session, duelID string) {
	time.Sleep(15 * time.Minute)
	r.mu.Lock()
	duel, exists := r.duels[duelID]
	if !exists || !duel.Active {
		r.mu.Unlock()
		return
	}
	duel.Active = false
	delete(r.duels, duelID)
	r.mu.Unlock()

	embed := &discordgo.MessageEmbed{
		Title:       "⚔️ Дуэль отменена! ⚔️",
		Description: fmt.Sprintf("Дуэль <@%s> не была принята! ⏰", duel.ChallengerID),
		Color:       randomColor(),
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Время вышло! 😢",
		},
	}
	_, err := s.ChannelMessageEditEmbed(duel.ChannelID, duel.MessageID, embed)
	if err != nil {
		log.Printf("Не удалось обновить сообщение дуэли по тайм-ауту: %v", err)
	}
}
