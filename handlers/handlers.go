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
		switch {
		case strings.HasPrefix(m.Content, "!poll #"):
			rank.HandlePollClose(s, m, m.Content)
		case strings.HasPrefix(m.Content, "!poll"):
			rank.HandlePollCommand(s, m, m.Content)
		case strings.HasPrefix(m.Content, "!dep"):
			rank.HandleDepCommand(s, m, m.Content)
		case strings.HasPrefix(m.Content, "!admin"):
			rank.HandleAdminCommand(s, m, m.Content)
		case strings.HasPrefix(m.Content, "!china give"):
			rank.HandleChinaGive(s, m, m.Content)
		case strings.HasPrefix(m.Content, "!china rating"):
			rank.HandleChinaRating(s, m, m.Content)
		case strings.HasPrefix(m.Content, "!china help"):
			rank.HandleChinaHelp(s, m, m.Content)
		case m.Content == "!top5":
			topUsers := rank.GetTop5()
			if len(topUsers) == 0 {
				s.ChannelMessageSend(m.ChannelID, "🏆 Топ-5 пуст!")
				return
			}
			response := "🏆 **Топ-5 игроков:**\n"
			for i, user := range topUsers {
				response += fmt.Sprintf("%d. <@%s> - %d кредитов\n", i+1, user.ID, user.Rating)
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
