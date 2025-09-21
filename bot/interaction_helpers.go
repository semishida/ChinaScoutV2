package bot

import (
	"github.com/bwmarrin/discordgo"
)

// InteractionResponseData представляет данные для ответа на взаимодействие
type InteractionResponseData struct {
	Content string
	Embed   *discordgo.MessageEmbed
}

// SendInteractionResponse отправляет ответ на взаимодействие
func SendInteractionResponse(s *discordgo.Session, i *discordgo.Interaction, data InteractionResponseData) error {
	responseData := &discordgo.InteractionResponseData{
		Content: data.Content,
	}

	if data.Embed != nil {
		responseData.Embeds = []*discordgo.MessageEmbed{data.Embed}
	}

	return s.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: responseData,
	})
}

// SendInteractionFollowup отправляет дополнительное сообщение после ответа
func SendInteractionFollowup(s *discordgo.Session, i *discordgo.Interaction, data InteractionResponseData) (*discordgo.Message, error) {
	followupData := &discordgo.WebhookParams{
		Content: data.Content,
	}

	if data.Embed != nil {
		followupData.Embeds = []*discordgo.MessageEmbed{data.Embed}
	}

	return s.FollowupMessageCreate(i, true, followupData)
}
