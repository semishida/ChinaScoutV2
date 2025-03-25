package bot

import (
	"csv2/ranking"
	"github.com/bwmarrin/discordgo"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"io"
	"log"
	"net/http"
	"os"
)

func SetupTelegram(token, floodChannelID, relayChannelID string, discord *discordgo.Session, rank *ranking.Ranking) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("Failed to initialize Telegram bot: %v", err)
	}

	bot.Debug = true
	log.Printf("Authorized on Telegram account %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	go func() {
		for update := range updates {
			if update.Message == nil {
				continue
			}

			log.Printf("Received Telegram message from %s: %s", update.Message.From.UserName, update.Message.Text)

			if update.Message.Document != nil {
				fileID := update.Message.Document.FileID
				file, err := bot.GetFile(tgbotapi.FileConfig{FileID: fileID})
				if err != nil {
					log.Printf("Failed to get Telegram file: %v", err)
					continue
				}

				fileURL := file.Link(token)
				localPath := "downloaded_" + update.Message.Document.FileName
				err = downloadFile(fileURL, localPath)
				if err != nil {
					log.Printf("Failed to download file: %v", err)
					continue
				}

				caption := update.Message.Caption
				if caption == "" {
					caption = "Файл от @" + update.Message.From.UserName
				}

				err = SendFileToDiscord(discord, floodChannelID, localPath, caption)
				if err != nil {
					log.Printf("Failed to send file to Discord: %v", err)
				} else {
					log.Printf("Sent file %s to Discord from Telegram", localPath)
				}

				os.Remove(localPath)
			}
		}
	}()
}

func downloadFile(url, filepath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}
