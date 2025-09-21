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

	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentMessageContent | discordgo.IntentsGuildVoiceStates | discordgo.IntentsGuilds

	// Регистрируем обработчик голосовой активности
	dg.AddHandler(rank.TrackVoiceActivity)
	log.Printf("Registered voice activity handler")

	// Регистрируем обработчик взаимодействий (кнопки и slash commands)
	log.Printf("Registering interaction handler")
	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		log.Printf("Interaction received: type=%v, user=%s", i.Type, i.Member.User.ID)

		if i.Member.User.ID == s.State.User.ID {
			log.Printf("Ignoring interaction from bot itself")
			return
		}

		switch i.Type {
		case discordgo.InteractionApplicationCommand:
			// Обработка slash commands
			log.Printf("Slash command received: %s from %s", i.ApplicationCommandData().Name, i.Member.User.ID)

			// Простой тест - отвечаем на любую команду
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "✅ Тест: команда получена!",
				},
			})
			return
		case discordgo.InteractionApplicationCommandAutocomplete:
			// Обработка автодополнения
			log.Printf("Autocomplete request for: %s from %s", i.ApplicationCommandData().Name, i.Member.User.ID)
		case discordgo.InteractionMessageComponent:
			// Обработка кнопок
			customID := i.MessageComponentData().CustomID
			log.Printf("Button interaction received, CustomID: %s", customID)
		default:
			log.Printf("Received unknown interaction type: %v", i.Type)
		}
	})

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

	// Получаем ID гильдии из floodChannelID
	guildID := ""
	if channel, err := dg.Channel(floodChannelID); err == nil {
		guildID = channel.GuildID
	}

	// Регистрируем slash commands
	if guildID != "" {
		log.Printf("Registering slash commands for guild: %s", guildID)
		if err := RegisterSlashCommands(dg, guildID, rank); err != nil {
			log.Printf("Failed to register slash commands: %v", err)
		} else {
			log.Println("Slash commands registered successfully!")
		}
	} else {
		log.Printf("Warning: Could not determine guild ID from channel %s", floodChannelID)
	}

	log.Println("Discord bot is running.")
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
