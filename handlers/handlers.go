package handlers

import (
	"fmt"
	"log"
	"strings"

	"csv2/ranking"
	"github.com/bwmarrin/discordgo"
)

func HandleMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate, rank *ranking.Ranking, floodChannelID, relayChannelID string) {
	if m.Author.ID == s.State.User.ID {
		return
	}

	if m.ChannelID == floodChannelID && strings.HasPrefix(m.Content, "!") {
		log.Printf("Received command: %s from %s in flood channel", m.Content, m.Author.ID)
		// Приводим команду к нижнему регистру для нечувствительности к регистру
		command := strings.ToLower(m.Content)
		switch {
		case strings.HasPrefix(command, "!cpoll"):
			rank.HandlePollCommand(s, m, m.Content)
		case strings.HasPrefix(command, "!dep"):
			rank.HandleDepCommand(s, m, m.Content)
		case strings.HasPrefix(command, "!closedep"):
			rank.HandleCloseDepCommand(s, m, m.Content)
		case strings.HasPrefix(command, "!china give"):
			rank.HandleChinaGive(s, m, m.Content)
		case strings.HasPrefix(command, "!china rating"):
			rank.HandleChinaRating(s, m, m.Content)
		case strings.HasPrefix(command, "!admin give"):
			rank.HandleAdminGive(s, m, m.Content)
		case strings.HasPrefix(command, "!chelp"):
			rank.HandleHelpCommand(s, m)
		case command == "!top5":
			topUsers := rank.GetTop5()
			if len(topUsers) == 0 {
				s.ChannelMessageSend(m.ChannelID, "🏆 Топ-5 товарищей пуст! Партия разочарована!")
				return
			}
			response := "🏆 **Топ-5 товарищей по социальному рейтингу:**\n"
			for i, user := range topUsers {
				response += fmt.Sprintf("%d. <@%s> - %d социальных кредитов\n", i+1, user.ID, user.Rating)
			}
			s.ChannelMessageSend(m.ChannelID, response)
		default:
			log.Printf("Unknown command: %s", m.Content)
		}
		return
	}

	if m.ChannelID == relayChannelID {
		log.Printf("Received message in relay: %s from %s", m.Content, m.Author.ID)
	}
}
