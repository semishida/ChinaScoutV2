package ranking

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Poll представляет опрос.
type Poll struct {
	ID       string         // Уникальный 5-символьный ID опроса
	Question string         // Вопрос опроса
	Options  []string       // Варианты ответа
	Bets     map[string]int // Ставки: userID -> сумма ставки
	Choices  map[string]int // Выбор: userID -> номер варианта (1, 2, ...)
	Active   bool           // Активен ли опрос
	Creator  string         // ID админа, создавшего опрос
	Created  time.Time      // Время создания
}

// splitCommand разбивает команду на части, сохраняя содержимое в квадратных скобках.
func splitCommand(command string) []string {
	var parts []string
	var current strings.Builder
	inBrackets := false

	for _, r := range command {
		if r == '[' {
			inBrackets = true
			current.WriteRune(r)
		} else if r == ']' {
			inBrackets = false
			current.WriteRune(r)
		} else if r == ' ' && !inBrackets {
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		} else {
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// GetCoefficients возвращает текущие коэффициенты для каждого варианта опроса.
func (p *Poll) GetCoefficients() []float64 {
	totalBet := 0
	optionBets := make([]int, len(p.Options))

	for _, bet := range p.Bets {
		totalBet += bet
	}
	for userID, choice := range p.Choices {
		if choice > 0 && choice <= len(optionBets) {
			optionBets[choice-1] += p.Bets[userID]
		}
	}

	coefficients := make([]float64, len(p.Options))
	for i := range p.Options {
		if optionBets[i] == 0 {
			coefficients[i] = 0
		} else {
			coefficients[i] = float64(totalBet) / float64(optionBets[i])
		}
	}
	return coefficients
}

// HandlePollCommand обрабатывает команду создания опроса.
func (r *Ranking) HandlePollCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Обработка !cpoll: %s от %s", command, m.Author.ID)

	parts := splitCommand(command)
	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!cpoll Вопрос [Вариант1] [Вариант2] ...`")
		return
	}

	if !r.IsAdmin(m.Author.ID) {
		s.ChannelMessageSend(m.ChannelID, "❌ Только товарищи-админы могут создавать опросы! 🔒")
		return
	}

	var questionParts []string
	var options []string
	for _, part := range parts[1:] {
		if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
			trimmed := strings.Trim(part, "[]")
			if trimmed != "" {
				options = append(options, trimmed)
			}
		} else {
			questionParts = append(questionParts, part)
		}
	}
	question := strings.Join(questionParts, " ")
	if question == "" {
		s.ChannelMessageSend(m.ChannelID, "❌ Вопрос не может быть пустым! 📝")
		return
	}

	if len(options) < 2 {
		s.ChannelMessageSend(m.ChannelID, "❌ Нужно минимум 2 варианта ответа! 📊")
		return
	}

	pollID := generatePollID()
	r.mu.Lock()
	r.polls[pollID] = &Poll{
		ID:       pollID,
		Question: question,
		Options:  options,
		Bets:     make(map[string]int),
		Choices:  make(map[string]int),
		Active:   true,
		Creator:  m.Author.ID,
		Created:  time.Now(),
	}
	r.mu.Unlock()

	response := fmt.Sprintf("🎉 **Опрос %s запущен!**\n<@%s> создал опрос: **%s**\n\n📋 **Варианты:**\n", pollID, m.Author.ID, question)
	for i, opt := range options {
		response += fmt.Sprintf("%d. [%s]\n", i+1, opt)
	}
	response += fmt.Sprintf("\n💸 Ставьте: `!dep %s <номер_варианта> <сумма>`\n🔒 Закрытие: `!closedep %s <номер>`", pollID, pollID)
	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("Опрос %s создан %s: %s с вариантами %v", pollID, m.Author.ID, question, options)
}

// HandleDepCommand обрабатывает команду ставки на опрос.
func (r *Ranking) HandleDepCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Обработка !dep: %s от %s", command, m.Author.ID)

	parts := splitCommand(command)
	if len(parts) != 4 {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!dep <ID_опроса> <номер_варианта> <сумма>`")
		return
	}

	pollID := parts[1]
	option, err := strconv.Atoi(parts[2])
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Номер варианта должен быть числом! 🔢")
		return
	}

	amount, err := strconv.Atoi(parts[3])
	if err != nil || amount <= 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ Сумма должна быть положительным числом! 💸")
		return
	}

	r.mu.Lock()
	poll, exists := r.polls[pollID]
	if !exists || !poll.Active {
		s.ChannelMessageSend(m.ChannelID, "❌ Опрос не найден или уже закрыт! 🔒")
		r.mu.Unlock()
		return
	}

	if option < 1 || option > len(poll.Options) {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Номер варианта должен быть от 1 до %d! 📊", len(poll.Options)))
		r.mu.Unlock()
		return
	}

	userRating := r.GetRating(m.Author.ID)
	if userRating < amount {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Недостаточно кредитов! Твой баланс: %d 💰", userRating))
		r.mu.Unlock()
		return
	}

	r.UpdateRating(m.Author.ID, -amount)
	if _, exists := poll.Bets[m.Author.ID]; exists {
		poll.Bets[m.Author.ID] += amount
	} else {
		poll.Bets[m.Author.ID] = amount
	}
	poll.Choices[m.Author.ID] = option
	r.mu.Unlock()

	coefficients := poll.GetCoefficients()
	coefficient := coefficients[option-1]

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🎲 <@%s> поставил %d кредитов на [%s] в опросе **%s** 📊\n**📈 Текущий коэффициент:** %.2f", m.Author.ID, amount, poll.Options[option-1], poll.Question, coefficient))
	r.LogCreditOperation(s, fmt.Sprintf("<@%s> поставил %d соц кредитов на опрос %s", pollID))
	log.Printf("Пользователь %s поставил %d на вариант %d в опросе %s, коэффициент: %.2f", m.Author.ID, amount, option, pollID, coefficient)
}

// HandleCloseDepCommand закрывает опрос и распределяет выигрыши.
func (r *Ranking) HandleCloseDepCommand(s *discordgo.Session, m *discordgo.MessageCreate, command string) {
	log.Printf("Обработка !closedep: %s от %s", command, m.Author.ID)

	parts := strings.Fields(command)
	if len(parts) != 3 {
		s.ChannelMessageSend(m.ChannelID, "❌ Используй: `!closedep <ID_опроса> <номер_победившего_варианта>`")
		return
	}

	pollID := parts[1]
	winningOptionStr := strings.Trim(parts[2], "<>[]")
	winningOption, err := strconv.Atoi(winningOptionStr)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Номер варианта должен быть числом! 🔢")
		return
	}

	r.mu.Lock()
	poll, exists := r.polls[pollID]
	if !exists {
		s.ChannelMessageSend(m.ChannelID, "❌ Опрос не найден! 📊")
		r.mu.Unlock()
		return
	}
	if !poll.Active {
		s.ChannelMessageSend(m.ChannelID, "❌ Опрос уже закрыт! 🔒")
		r.mu.Unlock()
		return
	}

	if m.Author.ID != poll.Creator {
		s.ChannelMessageSend(m.ChannelID, "❌ Только создатель опроса может его закрыть! 🔐")
		r.mu.Unlock()
		return
	}

	if winningOption < 1 || winningOption > len(poll.Options) {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Номер варианта должен быть от 1 до %d! 📊", len(poll.Options)))
		r.mu.Unlock()
		return
	}

	totalBet := 0
	winnersBet := 0
	for _, bet := range poll.Bets {
		totalBet += bet
	}
	for userID, choice := range poll.Choices {
		if choice == winningOption {
			winnersBet += poll.Bets[userID]
		}
	}

	var coefficient float64
	if winnersBet == 0 {
		coefficient = 0
	} else {
		coefficient = float64(totalBet) / float64(winnersBet)
	}

	response := fmt.Sprintf("✅ **Опрос %s завершён!** 🏆\nПобедил: **%s** (№%d)\n📈 **Коэффициент:** %.2f\n\n🎉 **Победители:**\n", pollID, poll.Options[winningOption-1], winningOption, coefficient)
	for userID, choice := range poll.Choices {
		if choice == winningOption {
			winnings := int(float64(poll.Bets[userID]) * coefficient)
			r.UpdateRating(userID, winnings+poll.Bets[userID])
			response += fmt.Sprintf("<@%s>: %d кредитов (ставка: %d) 💰\n", userID, winnings+poll.Bets[userID], poll.Bets[userID])
			r.LogCreditOperation(s, fmt.Sprintf("<@%s> выиграл %d соц кредитов в опросе %s", userID, winnings+poll.Bets[userID], pollID))
		}
	}
	if winnersBet == 0 {
		response += "Никто не победил! 😢"
	}

	poll.Active = false
	r.mu.Unlock()

	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("Опрос %s закрыт %s, победитель: %s, коэффициент: %.2f", pollID, m.Author.ID, poll.Options[winningOption-1], coefficient)
}

// HandlePollsCommand отображает активные опросы.
func (r *Ranking) HandlePollsCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	log.Printf("Обработка !polls: %s от %s", m.Content, m.Author.ID)

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.polls) == 0 {
		s.ChannelMessageSend(m.ChannelID, "📊 Нет активных опросов! Создай новый с помощью `!cpoll`! 🎉")
		return
	}

	response := "📊 **Активные опросы:**\n"
	for pollID, poll := range r.polls {
		if !poll.Active {
			continue
		}
		response += fmt.Sprintf("\n**Опрос %s: %s** 🎉\n", pollID, poll.Question)
		coefficients := poll.GetCoefficients()
		for i, option := range poll.Options {
			response += fmt.Sprintf("📋 Вариант %d. [%s] (📈 Коэффициент: %.2f)\n", i+1, option, coefficients[i])
			for userID, choice := range poll.Choices {
				if choice == i+1 {
					bet := poll.Bets[userID]
					potentialWin := int(float64(bet) * coefficients[i])
					response += fmt.Sprintf("  - <@%s>: %d кредитов (💰 Потенциальный выигрыш: %d)\n", userID, bet, potentialWin+bet)
				}
			}
		}
	}

	s.ChannelMessageSend(m.ChannelID, response)
	log.Printf("Список опросов отправлен %s", m.Author.ID)
}
