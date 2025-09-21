package bot

import (
	"log"
	"strconv"
	"strings"

	"csv2/ranking"

	"github.com/bwmarrin/discordgo"
)

// RegisterSlashCommands регистрирует все slash commands
func RegisterSlashCommands(s *discordgo.Session, guildID string, rank *ranking.Ranking) error {
	commands := []*discordgo.ApplicationCommand{
		// Основные команды
		{
			Name:        "china",
			Description: "Показать баланс соцкредитов",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь для проверки баланса (по умолчанию - вы)",
					Required:    false,
				},
			},
		},
		{
			Name:        "top",
			Description: "Показать топ-5 пользователей по соцкредитам",
		},
		{
			Name:        "stats",
			Description: "Показать статистику пользователя",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь для проверки статистики (по умолчанию - вы)",
					Required:    false,
				},
			},
		},
		{
			Name:        "transfer",
			Description: "Передать соцкредиты другому пользователю",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь для перевода",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "amount",
					Description: "Сумма для перевода",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "reason",
					Description: "Причина перевода (необязательно)",
					Required:    false,
				},
			},
		},
		{
			Name:        "help",
			Description: "Показать справку по командам",
		},

		// Игровые команды
		{
			Name:        "rb",
			Description: "Игра Красный-Чёрный",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "color",
					Description: "Цвет для ставки (red/black)",
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "Красный", Value: "red"},
						{Name: "Чёрный", Value: "black"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "bet",
					Description: "Сумма ставки",
					Required:    false,
					MinValue:    &[]float64{1}[0],
				},
			},
		},
		{
			Name:        "blackjack",
			Description: "Игра Блэкджек",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "bet",
					Description: "Сумма ставки",
					Required:    false,
					MinValue:    &[]float64{1}[0],
				},
			},
		},
		{
			Name:        "duel",
			Description: "Вызвать пользователя на дуэль",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь для дуэли",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "bet",
					Description: "Сумма ставки",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},

		// NFT и кейсы
		{
			Name:        "inventory",
			Description: "Показать инвентарь NFT",
		},
		{
			Name:        "case_inventory",
			Description: "Показать инвентарь кейсов",
		},
		{
			Name:        "case_bank",
			Description: "Показать банк кейсов",
		},
		{
			Name:        "daily_case",
			Description: "Получить ежедневный кейс",
		},
		{
			Name:        "open_case",
			Description: "Открыть кейс",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "case_id",
					Description:  "ID кейса для открытия",
					Required:     true,
					Autocomplete: true,
				},
			},
		},
		{
			Name:        "sell",
			Description: "Продать NFT",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "nft_id",
					Description:  "ID NFT для продажи",
					Required:     true,
					Autocomplete: true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "count",
					Description: "Количество для продажи",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},
		{
			Name:        "trade_nft",
			Description: "Передать NFT другому пользователю",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь для передачи",
					Required:    true,
				},
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "nft_id",
					Description:  "ID NFT для передачи",
					Required:     true,
					Autocomplete: true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "count",
					Description: "Количество для передачи",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},
		{
			Name:        "show_nft",
			Description: "Показать информацию о NFT",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "nft_id",
					Description:  "ID NFT для показа",
					Required:     true,
					Autocomplete: true,
				},
			},
		},
		{
			Name:        "buy_case_bank",
			Description: "Купить кейс из банка",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "case_id",
					Description:  "ID кейса для покупки",
					Required:     true,
					Autocomplete: true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "count",
					Description: "Количество кейсов",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},
		{
			Name:        "case_trade",
			Description: "Купить кейс у другого пользователя",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь-продавец",
					Required:    true,
				},
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "case_id",
					Description:  "ID кейса для покупки",
					Required:     true,
					Autocomplete: true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "count",
					Description: "Количество кейсов",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},

		// Экономика
		{
			Name:        "btc",
			Description: "Показать текущий курс биткойна",
		},
		{
			Name:        "prices",
			Description: "Показать статистику цен NFT",
		},

		// Опросы
		{
			Name:        "polls",
			Description: "Показать активные опросы",
		},
		{
			Name:        "cpoll",
			Description: "Создать опрос (только админы)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "question",
					Description: "Вопрос опроса",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "options",
					Description: "Варианты ответов через запятую",
					Required:    true,
				},
			},
		},
		{
			Name:        "dep",
			Description: "Сделать ставку на опрос",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "poll_id",
					Description:  "ID опроса",
					Required:     true,
					Autocomplete: true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "option",
					Description: "Номер варианта",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "amount",
					Description: "Сумма ставки",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},

		// Киноаукцион
		{
			Name:        "cinema",
			Description: "Предложить вариант для киноаукциона",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "title",
					Description: "Название фильма",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "amount",
					Description: "Сумма ставки",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},
		{
			Name:        "cinema_list",
			Description: "Показать варианты киноаукциона",
		},
		{
			Name:        "bet_cinema",
			Description: "Сделать ставку на киноаукцион",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "option",
					Description: "Номер варианта",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "amount",
					Description: "Сумма ставки",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},

		// Админские команды
		{
			Name:        "admin",
			Description: "Админская команда для изменения баланса",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "amount",
					Description: "Сумма (может быть отрицательной)",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "reason",
					Description: "Причина изменения",
					Required:    false,
				},
			},
		},
		{
			Name:        "admin_mass",
			Description: "Массовое изменение баланса (только админы)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "operation",
					Description: "Операция (+/-/=)",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "Добавить (+)", Value: "+"},
						{Name: "Убрать (-)", Value: "-"},
						{Name: "Установить (=)", Value: "="},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "amount",
					Description: "Сумма",
					Required:    true,
					MinValue:    &[]float64{0}[0],
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "users",
					Description: "ID пользователей через пробел",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "reason",
					Description: "Причина",
					Required:    false,
				},
			},
		},
		{
			Name:        "sync_nfts",
			Description: "Синхронизировать NFT из Google Sheets (только админы)",
		},
		{
			Name:        "admin_give_case",
			Description: "Выдать кейс пользователю (только админы)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь",
					Required:    true,
				},
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "case_id",
					Description:  "ID кейса",
					Required:     true,
					Autocomplete: true,
				},
			},
		},
		{
			Name:        "admin_give_nft",
			Description: "Выдать NFT пользователю (только админы)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь",
					Required:    true,
				},
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "nft_id",
					Description:  "ID NFT",
					Required:     true,
					Autocomplete: true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "count",
					Description: "Количество",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},
		{
			Name:        "admin_remove_nft",
			Description: "Удалить NFT у пользователя (только админы)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь",
					Required:    true,
				},
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "nft_id",
					Description:  "ID NFT",
					Required:     true,
					Autocomplete: true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "count",
					Description: "Количество",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},
		{
			Name:        "admin_refresh_bank",
			Description: "Обновить банк кейсов (только админы)",
		},
		{
			Name:        "admin_reset_limits",
			Description: "Сбросить лимиты кейсов (только админы)",
		},
		{
			Name:        "admin_cinema_list",
			Description: "Детальный список киноаукциона (только админы)",
		},
		{
			Name:        "admin_remove_lowest",
			Description: "Удалить самые низкие варианты киноаукциона (только админы)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "count",
					Description: "Количество вариантов для удаления",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},
		{
			Name:        "admin_adjust_cinema",
			Description: "Корректировать сумму варианта киноаукциона (только админы)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "option",
					Description: "Номер варианта",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "operation",
					Description: "Операция (+/-)",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "Добавить (+)", Value: "+"},
						{Name: "Убрать (-)", Value: "-"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "amount",
					Description: "Сумма",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},
		{
			Name:        "admin_remove_cinema",
			Description: "Удалить вариант киноаукциона (только админы)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь, предложивший вариант",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "option",
					Description: "Номер варианта",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},
		{
			Name:        "admin_end_blackjack",
			Description: "Завершить игру в блэкджек (только админы)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь",
					Required:    true,
				},
			},
		},
		{
			Name:        "admin_holiday_case",
			Description: "Выдать праздничный кейс (только админы)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "count",
					Description: "Количество кейсов",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},
		{
			Name:        "admin_holiday_case_all",
			Description: "Выдать праздничный кейс всем пользователям (только админы)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "count",
					Description: "Количество кейсов",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},
		{
			Name:        "admin_close_dep",
			Description: "Закрыть опрос и распределить выигрыши (только админы)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "poll_id",
					Description:  "ID опроса",
					Required:     true,
					Autocomplete: true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "winning_option",
					Description: "Номер выигрышного варианта",
					Required:    true,
					MinValue:    &[]float64{1}[0],
				},
			},
		},
	}

	// Регистрируем команды
	for _, cmd := range commands {
		_, err := s.ApplicationCommandCreate(s.State.User.ID, guildID, cmd)
		if err != nil {
			log.Printf("Cannot create '%v' command: %v", cmd.Name, err)
			return err
		}
		log.Printf("Registered slash command: %s", cmd.Name)
	}

	return nil
}

// HandleSlashCommand обрабатывает slash commands
func HandleSlashCommand(s *discordgo.Session, i *discordgo.InteractionCreate, rank *ranking.Ranking) {
	commandName := i.ApplicationCommandData().Name
	options := i.ApplicationCommandData().Options

	log.Printf("HandleSlashCommand: processing command '%s' with %d options", commandName, len(options))

	// Создаем мок MessageCreate для совместимости с существующими обработчиками
	mockMessage := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        i.ID,
			ChannelID: i.ChannelID,
			GuildID:   i.GuildID,
			Author:    i.Member.User,
			Content:   buildCommandString(commandName, options),
		},
	}

	// Обрабатываем команду
	switch commandName {
	case "china":
		rank.HandleChinaSlashCommand(s, i)
	case "top":
		rank.HandleTopSlashCommand(s, i)
	case "stats":
		rank.HandleStatsCommand(s, mockMessage)
	case "transfer":
		rank.HandleTransferCommand(s, mockMessage, buildCommandString(commandName, options))
	case "help":
		rank.HandleHelpSlashCommand(s, i)
	case "rb":
		if len(options) > 0 {
			rank.HandleRBCommand(s, mockMessage, buildCommandString(commandName, options))
		} else {
			rank.StartRBGame(s, mockMessage)
		}
	case "blackjack":
		if len(options) > 0 {
			rank.HandleBlackjackBet(s, mockMessage, buildCommandString(commandName, options))
		} else {
			rank.StartBlackjackGame(s, mockMessage)
		}
	case "duel":
		rank.HandleDuelCommand(s, mockMessage, buildCommandString(commandName, options))
	case "inventory":
		rank.HandleInventoryCommand(s, mockMessage)
	case "case_inventory":
		rank.HandleCaseInventoryCommand(s, mockMessage)
	case "case_bank":
		rank.HandleCaseBankCommand(s, mockMessage)
	case "daily_case":
		rank.HandleDailyCaseCommand(s, mockMessage)
	case "open_case":
		rank.HandleOpenCaseCommand(s, mockMessage, buildCommandString(commandName, options))
	case "sell":
		rank.HandleSellCommand(s, mockMessage, buildCommandString(commandName, options))
	case "trade_nft":
		rank.HandleTradeNFTCommand(s, mockMessage, buildCommandString(commandName, options))
	case "show_nft":
		rank.HandleShowNFTCommand(s, mockMessage, buildCommandString(commandName, options))
	case "buy_case_bank":
		rank.HandleBuyCaseBankCommand(s, mockMessage, buildCommandString(commandName, options))
	case "case_trade":
		rank.HandleCaseTradeCommand(s, mockMessage, buildCommandString(commandName, options))
	case "btc":
		rank.HandleBitcoinPriceCommand(s, mockMessage)
	case "prices":
		rank.HandlePriceStatsCommand(s, mockMessage)
	case "polls":
		rank.HandlePollsCommand(s, mockMessage)
	case "cpoll":
		rank.HandlePollCommand(s, mockMessage, buildCommandString(commandName, options))
	case "dep":
		rank.HandleDepCommand(s, mockMessage, buildCommandString(commandName, options))
	case "cinema":
		rank.HandleCinemaCommand(s, mockMessage, buildCommandString(commandName, options))
	case "cinema_list":
		rank.HandleCinemaListCommand(s, mockMessage)
	case "bet_cinema":
		rank.HandleBetCinemaCommand(s, mockMessage, buildCommandString(commandName, options))
	case "admin":
		rank.HandleAdminCommand(s, mockMessage, buildCommandString(commandName, options))
	case "admin_mass":
		rank.HandleAdminMassCommand(s, mockMessage, buildCommandString(commandName, options))
	case "sync_nfts":
		if !rank.IsAdmin(i.Member.User.ID) {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Только админы могут использовать эту команду!",
				},
			})
			return
		}
		err := rank.Kki.SyncFromSheets(rank)
		if err != nil {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ **Ошибка синхронизации**: " + err.Error(),
				},
			})
		} else {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "✅ **NFT и кейсы синхронизированы из Google Sheets!**",
				},
			})
		}
	case "admin_give_case":
		rank.HandleAdminGiveCase(s, mockMessage, buildCommandString(commandName, options))
	case "admin_give_nft":
		rank.HandleAdminGiveNFT(s, mockMessage, buildCommandString(commandName, options))
	case "admin_remove_nft":
		rank.HandleAdminRemoveNFT(s, mockMessage, buildCommandString(commandName, options))
	case "admin_refresh_bank":
		rank.HandleAdminRefreshBankCommand(s, mockMessage)
	case "admin_reset_limits":
		rank.HandleResetCaseLimitsCommand(s, mockMessage)
	case "admin_cinema_list":
		rank.HandleAdminCinemaListCommand(s, mockMessage)
	case "admin_remove_lowest":
		rank.HandleRemoveLowestCommand(s, mockMessage, buildCommandString(commandName, options))
	case "admin_adjust_cinema":
		rank.HandleAdjustCinemaCommand(s, mockMessage, buildCommandString(commandName, options))
	case "admin_remove_cinema":
		rank.HandleRemoveCinemaCommand(s, mockMessage, buildCommandString(commandName, options))
	case "admin_end_blackjack":
		rank.HandleEndBlackjackCommand(s, mockMessage, buildCommandString(commandName, options))
	case "admin_holiday_case":
		rank.HandleAdminHolidayCase(s, mockMessage, buildCommandString(commandName, options))
	case "admin_holiday_case_all":
		rank.HandleAdminGiveHolidayCaseAll(s, mockMessage, buildCommandString(commandName, options))
	case "admin_close_dep":
		rank.HandleCloseDepCommand(s, mockMessage, buildCommandString(commandName, options))
	default:
		log.Printf("HandleSlashCommand: unknown command '%s'", commandName)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Неизвестная команда: " + commandName,
			},
		})
	}
}

// HandleAutocomplete обрабатывает автодополнение для slash commands
func HandleAutocomplete(s *discordgo.Session, i *discordgo.InteractionCreate, rank *ranking.Ranking) {
	commandName := i.ApplicationCommandData().Name

	var choices []*discordgo.ApplicationCommandOptionChoice

	switch commandName {
	case "open_case", "buy_case_bank", "case_trade", "admin_give_case":
		// Автодополнение для кейсов
		cases := rank.Kki.GetCases()
		for caseID, kase := range cases {
			choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
				Name:  kase.Name + " (ID: " + caseID + ")",
				Value: caseID,
			})
		}
	case "sell", "trade_nft", "show_nft", "admin_give_nft", "admin_remove_nft":
		// Автодополнение для NFT
		nfts := rank.Kki.GetNFTs()
		for nftID, nft := range nfts {
			choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
				Name:  nft.Name + " (ID: " + nftID + ")",
				Value: nftID,
			})
		}
	case "dep", "admin_close_dep":
		// Автодополнение для опросов
		for pollID, poll := range rank.GetPolls() {
			choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
				Name:  poll.Question + " (ID: " + pollID + ")",
				Value: pollID,
			})
		}
	}

	// Ограничиваем количество вариантов до 25 (лимит Discord)
	if len(choices) > 25 {
		choices = choices[:25]
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{
			Choices: choices,
		},
	})
}

// buildCommandString строит строку команды из опций для совместимости
func buildCommandString(commandName string, options []*discordgo.ApplicationCommandInteractionDataOption) string {
	// Маппинг slash-команд на ожидаемые обработчиками команды
	commandMap := map[string]string{
		"admin_mass":             "adminmass",
		"admin_give_case":        "a_give_case",
		"admin_give_nft":         "a_give_nft",
		"admin_remove_nft":       "a_remove_nft",
		"admin_refresh_bank":     "a_refresh_bank",
		"admin_reset_limits":     "a_reset_case_limits",
		"admin_cinema_list":      "admincinemalist",
		"admin_adjust_cinema":    "adjustcinema",
		"admin_remove_lowest":    "removelowest",
		"admin_remove_cinema":    "removecinema",
		"admin_end_blackjack":    "endblackjack",
		"admin_close_dep":        "closedep",
		"admin_holiday_case":     "a_holiday_case",
		"admin_holiday_case_all": "a_give_holiday_case_all",
	}

	// Получаем правильное название команды
	cleanCommandName := commandName
	if mappedName, exists := commandMap[commandName]; exists {
		cleanCommandName = mappedName
	} else if strings.HasPrefix(commandName, "admin_") {
		cleanCommandName = strings.TrimPrefix(commandName, "admin_")
	}

	cmd := "!" + cleanCommandName

	for _, opt := range options {
		switch opt.Type {
		case discordgo.ApplicationCommandOptionUser:
			cmd += " <@" + opt.UserValue(nil).ID + ">"
		case discordgo.ApplicationCommandOptionString:
			cmd += " " + opt.StringValue()
		case discordgo.ApplicationCommandOptionInteger:
			cmd += " " + strconv.Itoa(int(opt.IntValue()))
		case discordgo.ApplicationCommandOptionNumber:
			cmd += " " + strconv.FormatFloat(opt.FloatValue(), 'f', -1, 64)
		}
	}

	return cmd
}
