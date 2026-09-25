package main

// Валюта страны по коду ISO 3166-1.
//
// Справочник встроен: он меняется раз в годы, а открытые API стран то
// закрываются, то меняют формат (REST Countries, например, закрыл v3.1).
// Страны еврозоны и зоны доллара перечислены явно, чтобы не гадать.

// countryCurrency -- код страны → код валюты ISO 4217.
var countryCurrency = map[string]string{
	// еврозона и страны, где евро -- основная валюта
	"AT": "EUR", "BE": "EUR", "HR": "EUR", "CY": "EUR", "EE": "EUR", "FI": "EUR", "FR": "EUR",
	"DE": "EUR", "GR": "EUR", "IE": "EUR", "IT": "EUR", "LV": "EUR", "LT": "EUR", "LU": "EUR",
	"MT": "EUR", "NL": "EUR", "PT": "EUR", "SK": "EUR", "SI": "EUR", "ES": "EUR", "AD": "EUR",
	"MC": "EUR", "SM": "EUR", "VA": "EUR", "ME": "EUR", "XK": "EUR",
	// доллар США
	"US": "USD", "EC": "USD", "SV": "USD", "PA": "USD", "PR": "USD", "TL": "USD", "ZW": "USD",
	// Европа вне еврозоны
	"GB": "GBP", "CH": "CHF", "LI": "CHF", "NO": "NOK", "SE": "SEK", "DK": "DKK", "IS": "ISK",
	"PL": "PLN", "CZ": "CZK", "HU": "HUF", "RO": "RON", "BG": "BGN", "RS": "RSD", "BA": "BAM",
	"MK": "MKD", "AL": "ALL", "MD": "MDL", "UA": "UAH", "BY": "BYN", "RU": "RUB", "TR": "TRY",
	// Кавказ и Центральная Азия
	"GE": "GEL", "AM": "AMD", "AZ": "AZN", "KZ": "KZT", "UZ": "UZS", "KG": "KGS", "TJ": "TJS", "TM": "TMT",
	// Ближний Восток и Африка
	"AE": "AED", "SA": "SAR", "QA": "QAR", "BH": "BHD", "OM": "OMR", "KW": "KWD", "IL": "ILS",
	"JO": "JOD", "LB": "LBP", "IR": "IRR", "IQ": "IQD", "EG": "EGP", "MA": "MAD", "TN": "TND",
	"DZ": "DZD", "ZA": "ZAR", "NG": "NGN", "KE": "KES", "TZ": "TZS", "ET": "ETB", "GH": "GHS",
	// Азия и Океания
	"CN": "CNY", "HK": "HKD", "MO": "MOP", "TW": "TWD", "JP": "JPY", "KR": "KRW", "MN": "MNT",
	"IN": "INR", "LK": "LKR", "NP": "NPR", "BD": "BDT", "PK": "PKR", "MV": "MVR", "TH": "THB",
	"VN": "VND", "KH": "KHR", "LA": "LAK", "MM": "MMK", "MY": "MYR", "SG": "SGD", "ID": "IDR",
	"PH": "PHP", "AU": "AUD", "NZ": "NZD", "FJ": "FJD",
	// Америка
	"CA": "CAD", "MX": "MXN", "BR": "BRL", "AR": "ARS", "CL": "CLP", "CO": "COP", "PE": "PEN",
	"BO": "BOB", "UY": "UYU", "PY": "PYG", "CU": "CUP", "DO": "DOP", "CR": "CRC", "GT": "GTQ",
	"JM": "JMD",
}

// currencyNames -- как валюта называется по-русски; для тех, что встречаются чаще.
var currencyNames = map[string]string{
	"RUB": "российский рубль", "USD": "доллар США", "EUR": "евро", "GBP": "фунт стерлингов",
	"CHF": "швейцарский франк", "TRY": "турецкая лира", "GEL": "грузинский лари", "AMD": "армянский драм",
	"AZN": "азербайджанский манат", "KZT": "казахстанский тенге", "UZS": "узбекский сум",
	"KGS": "киргизский сом", "AED": "дирхам ОАЭ", "CNY": "китайский юань", "JPY": "японская иена",
	"THB": "тайский бат", "VND": "вьетнамский донг", "INR": "индийская рупия", "EGP": "египетский фунт",
	"RSD": "сербский динар", "CZK": "чешская крона", "PLN": "польский злотый", "HUF": "венгерский форинт",
	"BYN": "белорусский рубль", "IDR": "индонезийская рупия", "KRW": "южнокорейская вона",
	"SGD": "сингапурский доллар", "NOK": "норвежская крона", "SEK": "шведская крона", "DKK": "датская крона",
}
