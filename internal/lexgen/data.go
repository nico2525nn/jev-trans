package lexgen

import "github.com/nico/jev-trans/internal/lang"

// jaNouns maps the high-frequency Japanese common nouns whose sense the analyzer
// resolves onto their English lexeme.
//
// The list is explicit rather than generated, and that is the point: a word the
// table does not cover surfaces as "no English lexeme known" and becomes a
// visible lexical gap, rather than a fluent-looking mistranslation.
//
// Every key must be a Japanese surface the analyzer can actually produce. The
// table once carried "ceipt" (a truncated Latin key), the Hangul "이웃" and
// the Simplified Chinese "伙伴"; none can be produced by the morph layer, and
// Form("receipt", en, ja) duly returned the string "ceipt" with ok=true, which
// is worse than a gap because the caller believes it. Validate reports every
// key the analyzer cannot reach.
//
// 男 and 女 were dropped for the same reason from the other side: they are
// bound morphemes in ordinary use (彼女, 男性, 男女), and listing them as bare
// nouns made the analyzer split 彼女 into 彼 + 女, which cost more than the two
// lexemes were worth.
var jaNouns = map[string]string{
	// people and roles
	"人": "person", "子供": "child", "子ども": "child",
	"友": "friend", "友達": "friend", "家族": "family", "母": "mother", "父": "father",
	"兄": "brother", "姉": "sister", "弟": "brother", "妹": "sister",
	"先生": "teacher", "学生": "student", "生徒": "student", "社員": "employee",
	"医者": "doctor", "研究者": "researcher", "教授": "professor", "部長": "department head",
	"客": "customer", "同僚": "colleague",
	// animals
	"猫": "cat", "犬": "dog", "鳥": "bird", "魚": "fish", "馬": "horse",
	"牛": "cow", "虫": "insect", "動物": "animal",
	// body and life
	"頭": "head", "目": "eye", "耳": "ear", "口": "mouth", "手": "hand",
	"足": "foot", "心": "heart", "体": "body", "顔": "face", "髪": "hair",
	// places
	"家": "house", "学校": "school", "大学": "university", "会社": "company",
	"駅": "station", "空港": "airport", "病院": "hospital", "図書館": "library",
	"公園": "park", "道": "road", "部屋": "room", "町": "town", "村": "village",
	"国": "country", "街": "city", "店": "shop", "レストラン": "restaurant",
	"事務所": "office", "教室": "classroom", "庭": "garden", "山": "mountain",
	"川": "river", "海": "sea", "橋": "bridge",
	// objects
	"本": "book", "手紙": "letter", "紙": "paper", "新聞": "newspaper",
	"辞書": "dictionary", "ノート": "notebook", "ペン": "pen", "車": "car",
	"電車": "train", "自転車": "bicycle", "飛行機": "airplane", "船": "ship",
	"鞄": "bag", "鍵": "key", "机": "desk", "椅子": "chair", "服": "clothes",
	"靴": "shoes", "眼鏡": "glasses", "時計": "watch", "写真": "photograph",
	"地図": "map", "音楽": "music", "映画": "film", "絵": "picture",
	"電話": "telephone", "パソコン": "computer", "機械": "machine",
	"カメラ": "camera", "窓": "window", "扉": "door", "水": "water",
	"箱": "box", "袋": "bag", "切符": "ticket",
	// food and drink
	"ご飯": "meal", "米": "rice", "パン": "bread", "肉": "meat",
	"野菜": "vegetable", "果物": "fruit", "茶": "tea", "コーヒー": "coffee",
	"酒": "alcohol", "砂糖": "sugar", "塩": "salt", "味": "taste",
	// time
	"日": "day", "週": "week", "月": "month", "年": "year", "時間": "time",
	"朝": "morning", "夜": "night", "今日": "today", "明日": "tomorrow",
	"昨日": "yesterday", "今": "now", "昔": "the past", "将来": "the future",
	// abstract and work
	"名前": "name", "言葉": "word", "仕事": "work", "会議": "meeting",
	"計画": "plan", "問題": "problem", "答え": "answer", "理由": "reason",
	"情報": "information", "気持ち": "feeling", "希望": "hope", "夢": "dream",
	"料金": "fee", "結果": "result", "目的": "purpose", "意味": "meaning",
	"方法": "method", "場合": "case", "内容": "content", "状態": "state",
	"規則": "rule", "制約": "constraint", "推論": "inference", "判断": "judgment",
	"検証": "verification", "設計": "design", "実装": "implementation",
	"翻訳": "translation", "文法": "grammar", "日本語": "Japanese", "英語": "English",
	"研究": "research", "技術": "technology", "文化": "culture", "社会": "society",
	// weather and nature
	"雨": "rain", "雪": "snow", "風": "wind", "空": "sky", "太陽": "sun",
	"天気": "weather", "季節": "season", "春": "spring", "夏": "summer",
	"秋": "autumn", "冬": "winter",
	// events
	"旅": "trip", "旅行": "travel", "ニュース": "news", "式典": "ceremony",
	"音楽会": "concert", "体育": "physical education",
}

// enNounOverrides names which Japanese word a shared English lexeme should
// project to when the inversion cannot decide on its own.
//
// Six English nouns in jaNouns are translated by two different Japanese words.
// The inversion sorts the Japanese keys and takes the first, which picked 友 over
// 友達 and 弟 over 兄 — never the word an English "friend" or "brother" should
// become. The override states the idiomatic choice; the losing word is still
// reachable from JA -> EN, so nothing is lost in the direction it is written in.
var enNounOverrides = map[string]string{
	"friend":  "友達", // over 友
	"sister":  "姉",  // over 妹
	"child":   "子供", // over 子ども
	"brother": "兄",  // over 弟
	"student": "生徒", // over 学生
	"bag":     "鞄",  // over 袋
}

// jaNames maps Japanese proper names onto the romanization the system is
// entitled to assert. Only names with a well established reading appear here;
// anything else reports a lexical gap rather than a guess, because a guessed
// reading of a Japanese name is a fabrication (plan.md §51).
var jaNames = map[string]string{
	// personal names
	"太郎": "Taro", "次郎": "Jiro", "三郎": "Saburo", "健太": "Kenta",
	"花子": "Hanako", "京子": "Kyoko", "由美": "Yumi", "愛": "Ai",
	"健一": "Kenichi", "翔太": "Shota", "さくら": "Sakura", "美咲": "Misaki",
	"大輔": "Daisuke", "裕太": "Yuta", "拓也": "Takuya", "直樹": "Naoki",
	"陽菜": "Hina", "結衣": "Yui", "彩": "Aya", "楓": "Kaede",
	// surnames
	"山田": "Yamada", "田中": "Tanaka", "佐藤": "Sato", "鈴木": "Suzuki",
	"高橋": "Takahashi", "伊藤": "Ito", "渡辺": "Watanabe", "山本": "Yamamoto",
	"中村": "Nakamura", "小林": "Kobayashi", "加藤": "Kato", "吉田": "Yoshida",
	// places and countries
	"東京": "Tokyo", "日本": "Japan", "大阪": "Osaka", "奈良": "Nara",
	"横浜": "Yokohama", "京都": "Kyoto",
	"アメリカ": "America", "イギリス": "Britain", "フランス": "France",
	"ドイツ": "Germany", "中国": "China", "韓国": "Korea", "ロシア": "Russia",
	// titles
	"先生": "Mr.", "教授": "Professor", "博士": "Dr.",
}

// identityTerms are surfaces that are the same word in both languages, so the
// mapping is an identity rather than a translation.
var identityTerms = map[lang.Lang]map[string]string{
	lang.JA: {
		"API": "API", "HTTP": "HTTP", "JSON": "JSON", "SQL": "SQL",
		"DNA": "DNA", "UNESCO": "UNESCO", "MIT": "MIT", "IEEE": "IEEE",
	},
	lang.EN: {
		"API": "API", "HTTP": "HTTP", "JSON": "JSON", "SQL": "SQL",
		"DNA": "DNA", "UNESCO": "UNESCO", "MIT": "MIT", "IEEE": "IEEE",
	},
}

// jaPronouns maps the Japanese personal pronouns onto English. 彼 and 彼女 are
// homophonous with 彼/彼女 as determiners, so the semantics layer records them
// at reduced confidence; the planner's gender policy is what stops a homophone
// from being realized as a gendered English pronoun.
var jaPronouns = map[string]string{
	"私": "I", "僕": "I", "俺": "I", "あたし": "I",
	"あなた": "you", "君": "you",
	"彼": "he", "彼女": "she", "あの人": "that person", "あの方": "that person",
	"私たち": "we", "僕たち": "we", "俺たち": "we",
	"彼ら": "they", "彼女たち": "they",
	"それ": "it", "あれ": "that", "これ": "this",
	"だれ": "who", "誰": "who", "何": "what", "いつ": "when", "どこ": "where",
}

// enPronouns maps English personal pronouns onto Japanese. These are lexical
// identities in both directions, not translations: a pronoun has no other
// content to translate.
var enPronouns = map[string]string{
	"i": "私", "me": "私", "my": "私の", "mine": "私の", "myself": "自分",
	"you": "あなた", "your": "あなたの", "yours": "あなたの", "yourself": "自分",
	"he": "彼", "him": "彼", "his": "彼の",
	"she": "彼女", "her": "彼女の", "hers": "彼女のもの",
	"it": "それ", "its": "その",
	"we": "私たち", "us": "私たち", "our": "私たちの", "ours": "私たちのもの",
	"they": "彼ら", "them": "彼ら", "their": "彼らの", "theirs": "彼らのもの",
	"who": "誰", "what": "何", "when": "いつ", "where": "どこ", "why": "なぜ",
	"this": "これ", "that": "それ", "these": "これら", "those": "それら",
	"someone": "誰か", "anyone": "誰か", "everyone": "皆", "nobody": "誰も",
	"something": "何か", "anything": "何か", "everything": "すべて", "nothing": "何も",
	"here": "ここ", "there": "そこ", "now": "今", "then": "それから",
}

// katakanaRomaji turns katakana into romaji fragments, so a katakana name the
// system cannot otherwise ground still produces a surface form instead of being
// silently dropped. A katakana form with no reading here is not transliterated.
var katakanaRomaji = map[rune]string{
	'ア': "a", 'イ': "i", 'ウ': "u", 'エ': "e", 'オ': "o",
	'カ': "ka", 'キ': "ki", 'ク': "ku", 'ケ': "ke", 'コ': "ko",
	'サ': "sa", 'シ': "shi", 'ス': "su", 'セ': "se", 'ソ': "so",
	'タ': "ta", 'チ': "chi", 'ツ': "tsu", 'テ': "te", 'ト': "to",
	'ナ': "na", 'ニ': "ni", 'ヌ': "nu", 'ネ': "ne", 'ノ': "no",
	'ハ': "ha", 'ヒ': "hi", 'フ': "fu", 'ヘ': "he", 'ホ': "ho",
	'マ': "ma", 'ミ': "mi", 'ム': "mu", 'メ': "me", 'モ': "mo",
	'ヤ': "ya", 'ユ': "yu", 'ヨ': "yo",
	'ラ': "ra", 'リ': "ri", 'ル': "ru", 'レ': "re", 'ロ': "ro",
	'ワ': "wa", 'ヲ': "wo", 'ン': "n",
	'ガ': "ga", 'ギ': "gi", 'グ': "gu", 'ゲ': "ge", 'ゴ': "go",
	'ザ': "za", 'ジ': "ji", 'ズ': "zu", 'ゼ': "ze", 'ゾ': "zo",
	'ダ': "da", 'ヂ': "ji", 'ヅ': "zu", 'デ': "de", 'ド': "do",
	'バ': "ba", 'ビ': "bi", 'ブ': "bu", 'ベ': "be", 'ボ': "bo",
	'パ': "pa", 'ピ': "pi", 'プ': "pu", 'ペ': "pe", 'ポ': "po",
	'ヴ': "vu",
	'ァ': "a", 'ィ': "i", 'ゥ': "u", 'ェ': "e", 'ォ': "o",
	'ャ': "ya", 'ュ': "yu", 'ョ': "yo",
	'ー': "", 'ッ': "",
}
