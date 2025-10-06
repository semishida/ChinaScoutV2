package bot

import (
	"fmt"
	"log"
	"os"
	"time"

	"csv2/ranking"

	"github.com/bwmarrin/discordgo"
)

func SetupDiscord(token, floodChannelID, relayChannelID string, rank *ranking.Ranking) *discordgo.Session {
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("Failed to initialize Discord bot: %v", err)
	}

	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentMessageContent | discordgo.IntentsGuildVoiceStates

	// Регистрируем обработчик голосовой активности
	dg.AddHandler(rank.TrackVoiceActivity)

	for i := 0; i < 5; i++ {
		err = dg.Open()
		if err == nil {
			break
		}
		log.Printf("Failed to open Discord session (attempt %d/5): %v", i+1, err)
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		log.Fatalf("Failed to open Discord session after 5 attempts: %v", err)
	}

	log.Println("Discord bot is running.")

	// Регистрируем slash-команды
	registerSlashCommands(dg)

	return dg
}

func SendFileToDiscord(dg *discordgo.Session, channelID, filePath, caption string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	if caption != "" {
		if _, err := dg.ChannelMessageSend(channelID, caption); err != nil {
			log.Printf("Failed to send caption to Discord: %v", err)
			return fmt.Errorf("failed to send message to Discord: %v", err)
		}
	}

	_, err = dg.ChannelFileSend(channelID, filePath, file)
	if err != nil {
		log.Printf("Failed to send file to Discord: %v", err)
		return fmt.Errorf("failed to send file to Discord: %v", err)
	}
	log.Printf("Sent file to Discord channel %s: %s", channelID, filePath)
	return nil
}

// registerSlashCommands регистрирует slash-команды в Discord
func registerSlashCommands(dg *discordgo.Session) {
	commands := []*discordgo.ApplicationCommand{
		{
			Name:        "china",
			Description: "Показать информацию о пользователе",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь для проверки",
					Required:    false,
				},
			},
		},
		{
			Name:        "top",
			Description: "Показать топ пользователей",
		},
		{
			Name:        "stats",
			Description: "Показать статистику пользователя",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь для проверки",
					Required:    false,
				},
			},
		},
		{
			Name:        "blackjack",
			Description: "Начать игру в блэкджек",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "amount",
					Description: "Сумма ставки",
					Required:    true,
				},
			},
		},
		{
			Name:        "rb",
			Description: "Игра Красный-Черный",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "color",
					Description: "Цвет (red/black)",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "Красный", Value: "red"},
						{Name: "Черный", Value: "black"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "amount",
					Description: "Сумма ставки",
					Required:    true,
				},
			},
		},
		{
			Name:        "duel",
			Description: "Вызвать на дуэль",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "amount",
					Description: "Сумма ставки",
					Required:    true,
				},
			},
		},
		{
			Name:        "inventory",
			Description: "Показать инвентарь",
		},
		{
			Name:        "chelp",
			Description: "Показать справку по командам",
		},
	}

	// Регистрируем команды
	for _, cmd := range commands {
		_, err := dg.ApplicationCommandCreate(dg.State.User.ID, "", cmd)
		if err != nil {
			log.Printf("Failed to create slash command %s: %v", cmd.Name, err)
		} else {
			log.Printf("Successfully registered slash command: %s", cmd.Name)
		}
	}
}
