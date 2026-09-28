package agent

// Детектор prompt injection в ответах серверов.
//
// Описания мест берутся из Википедии -- их правит кто угодно, и кто угодно может
// дописать «игнорируй предыдущие инструкции». Детектор не блокирует прогон:
// защита держится на коде (белый список, проверки на серверах). Его задача --
// сделать попытку видимой: в ленте хода работы и в проверке флоу.

import (
	"regexp"
	"strings"
)

var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?(the\s+)?(previous|prior|above|earlier)\s+(instructions|prompts?|rules)`),
	regexp.MustCompile(`(?i)disregard\s+(all\s+)?(the\s+)?(previous|prior|above)?\s*(instructions|rules)`),
	regexp.MustCompile(`(?i)(reveal|print|show)\s+(your\s+)?(system\s+prompt|instructions)`),
	regexp.MustCompile(`(?i)\bsystem\s+prompt\b`),
	regexp.MustCompile(`(?i)\byou\s+are\s+now\b`),
	regexp.MustCompile(`(?i)\bnew\s+instructions\b`),
	regexp.MustCompile(`(?i)игнорируй\s+(все\s+)?(предыдущие\s+|прошлые\s+)?(инструкции|указания|правила)`),
	regexp.MustCompile(`(?i)забудь\s+(все\s+)?(предыдущие\s+)?(инструкции|указания|правила)`),
	// \b в Go знает только латиницу -- для кириллицы границу слова задаём сами
	regexp.MustCompile(`(?i)(?:^|[^\p{L}])ты\s+теперь(?:[^\p{L}]|$)`),
	regexp.MustCompile(`(?i)новые\s+инструкции`),
	regexp.MustCompile(`(?i)системн\w*\s+промпт`),
}

// detectInjection возвращает цитату с найденным маркером или пустую строку.
func detectInjection(text string) (quote, marker string) {
	for _, p := range injectionPatterns {
		loc := p.FindStringIndex(text)
		if loc == nil {
			continue
		}
		runes := []rune(text)
		// индексы regexp -- байтовые; переводим в руны, чтобы не разрезать букву
		start := len([]rune(text[:loc[0]]))
		end := len([]rune(text[:loc[1]]))
		from, to := max(0, start-60), min(len(runes), end+60)
		q := strings.TrimSpace(string(runes[from:to]))
		if from > 0 {
			q = "…" + q
		}
		if to < len(runes) {
			q += "…"
		}
		return q, strings.ToLower(text[loc[0]:loc[1]])
	}
	return "", ""
}

// wrapResult -- результат инструмента для модели, в разделённом блоке.
// Закрывающий тег внутри данных обезвреживается, иначе данные могли бы
// «закончить» блок и продолжить от имени инструкции.
func wrapResult(server, tool, text string) string {
	text = strings.ReplaceAll(text, "</tool_result>", "‹/tool_result›")
	return "<tool_result server=\"" + server + "\" tool=\"" + tool + "\" trusted=\"false\">\n" + text + "\n</tool_result>"
}
