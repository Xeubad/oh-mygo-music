package lyrics

import (
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/longbridgeapp/opencc"
)

// DisplayOptions 控制可撤销的显示副本，不改变来源内容或播放时间。
type DisplayOptions struct {
	Simplify        bool
	OriginalChinese bool
	HideTranslation bool
}

var chineseConverter = sync.OnceValues(func() (*opencc.OpenCC, error) { return opencc.New("t2s") })
var conversionLock sync.Mutex

// Display 为普通歌词、律动和分享生成同一份显示结果。
func Display(source []Line, options DisplayOptions) []Line {
	shown := slices.Clone(source)
	var converter *opencc.OpenCC
	if options.Simplify {
		converter, _ = chineseConverter()
	}
	conversionLock.Lock()
	defer conversionLock.Unlock()
	japanese := Lang(source) == "ja"
	originalChinese := options.OriginalChinese
	translationLines := make([]Line, 0, len(source))
	for _, line := range source {
		originalChinese = originalChinese || chineseText(line.Text)
		if line.Translation != "" {
			translationLines = append(translationLines, Line{Text: line.Translation})
		}
	}
	translatedChinese := Lang(translationLines) != "ja"
	translationEvidence := false
	for _, line := range translationLines {
		translationEvidence = translationEvidence || chineseText(line.Text)
	}
	for index := range shown {
		line := &shown[index]
		line.Words = slices.Clone(line.Words)
		// 一行有明确中文依据后，同曲汉字行共享判断；整曲无依据则保留原样。
		if converter != nil && (options.OriginalChinese || !japanese && originalChinese) && !nonChineseScript(line.Text) {
			convertLine(line, converter)
		}
		if options.HideTranslation {
			line.Translation = ""
		} else if converter != nil && translatedChinese && translationEvidence && !nonChineseScript(line.Translation) {
			// 译文通常由来源明确标记为中文，按实际转换结果处理短词组，
			// 不要求它恰好命中原词的自动识别词表。
			if translated, err := converter.Convert(line.Translation); err == nil {
				line.Translation = translated
			}
		}
	}
	return shown
}

func convertLine(line *Line, converter *opencc.OpenCC) {
	converted, err := converter.Convert(line.Text)
	if err != nil || converted == line.Text {
		return
	}
	if len(line.Words) == 0 {
		line.Text = converted
		return
	}
	var joined strings.Builder
	for _, word := range line.Words {
		joined.WriteString(word.Text)
	}
	wordsText := joined.String()
	convertedWords, err := converter.Convert(wordsText)
	// 只有已验证一一对应的文本才按原时间单元回填；长度变化绝不均分真实时间。
	if err != nil || strings.TrimSpace(wordsText) != strings.TrimSpace(line.Text) ||
		strings.TrimSpace(convertedWords) != strings.TrimSpace(converted) ||
		utf8.RuneCountInString(wordsText) != utf8.RuneCountInString(convertedWords) {
		return
	}
	runes, offset := []rune(convertedWords), 0
	for index := range line.Words {
		count := utf8.RuneCountInString(line.Words[index].Text)
		line.Words[index].Text = string(runes[offset : offset+count])
		offset += count
	}
	line.Text = converted
}

func nonChineseScript(text string) bool {
	for _, character := range text {
		if unicode.In(character, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

func chineseText(text string) bool {
	if nonChineseScript(text) {
		return false
	}
	// 仅以明确中文词组或至少两个汉字组成的常见语法片段自动判断；
	// 纯汉字地名、歌名及日文歌词不因字形可转换就被误改。
	for _, phrase := range []string{"我們", "我们", "你們", "你们", "他們", "他们", "什麼", "什么", "怎麼", "怎么", "為什", "为什", "這個", "这个", "這裡", "这里", "那個", "那个", "沒有", "没有", "還有", "还有", "讓我", "让我", "讓你", "让你", "為了", "为了", "因為", "因为", "不會", "不会", "一個", "一个", "你的", "我的", "妳的"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	count := 0
	for _, character := range text {
		if unicode.Is(unicode.Han, character) {
			count++
		}
	}
	if count >= 2 {
		for _, particle := range []string{"的", "了", "是", "不", "在", "有", "和", "被", "过", "从", "向", "與", "与"} {
			if strings.Contains(text, particle) {
				return true
			}
		}
	}
	return false
}
