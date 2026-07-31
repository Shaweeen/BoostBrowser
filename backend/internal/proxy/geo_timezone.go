package proxy

import "strings"

// TimezoneHintFromCountry maps coarse geo labels (ISO country or English name)
// to a primary IANA timezone. Used to align fingerprint --timezone with proxy
// exit location (MoreLogin/AdsPower-style geo consistency) without auto-forcing
// timezone until the user or bind path opts in.
func TimezoneHintFromCountry(country string) string {
	c := strings.ToUpper(strings.TrimSpace(country))
	if c == "" {
		return ""
	}
	// ISO-ish codes first.
	if tz, ok := countryCodeTimezone[c]; ok {
		return tz
	}
	// English / Chinese names from IP intelligence providers.
	name := strings.ToLower(strings.TrimSpace(country))
	if tz, ok := countryNameTimezone[name]; ok {
		return tz
	}
	return ""
}

var countryCodeTimezone = map[string]string{
	"US": "America/New_York",
	"CA": "America/Toronto",
	"GB": "Europe/London",
	"UK": "Europe/London",
	"DE": "Europe/Berlin",
	"FR": "Europe/Paris",
	"NL": "Europe/Amsterdam",
	"JP": "Asia/Tokyo",
	"KR": "Asia/Seoul",
	"CN": "Asia/Shanghai",
	"HK": "Asia/Hong_Kong",
	"TW": "Asia/Taipei",
	"SG": "Asia/Singapore",
	"AU": "Australia/Sydney",
	"IN": "Asia/Kolkata",
	"BR": "America/Sao_Paulo",
	"RU": "Europe/Moscow",
	"UA": "Europe/Kyiv",
	"TR": "Europe/Istanbul",
	"AE": "Asia/Dubai",
	"TH": "Asia/Bangkok",
	"VN": "Asia/Ho_Chi_Minh",
	"ID": "Asia/Jakarta",
	"MY": "Asia/Kuala_Lumpur",
	"PH": "Asia/Manila",
	"MX": "America/Mexico_City",
	"AR": "America/Argentina/Buenos_Aires",
	"CL": "America/Santiago",
	"CO": "America/Bogota",
	"PL": "Europe/Warsaw",
	"ES": "Europe/Madrid",
	"IT": "Europe/Rome",
	"SE": "Europe/Stockholm",
	"NO": "Europe/Oslo",
	"FI": "Europe/Helsinki",
	"CH": "Europe/Zurich",
	"AT": "Europe/Vienna",
	"BE": "Europe/Brussels",
	"IE": "Europe/Dublin",
	"PT": "Europe/Lisbon",
	"CZ": "Europe/Prague",
	"RO": "Europe/Bucharest",
	"HU": "Europe/Budapest",
	"NZ": "Pacific/Auckland",
	"ZA": "Africa/Johannesburg",
	"EG": "Africa/Cairo",
	"NG": "Africa/Lagos",
	"IL": "Asia/Jerusalem",
	"SA": "Asia/Riyadh",
	"PK": "Asia/Karachi",
	"BD": "Asia/Dhaka",
}

var countryNameTimezone = map[string]string{
	"united states":        "America/New_York",
	"usa":                  "America/New_York",
	"united kingdom":       "Europe/London",
	"great britain":        "Europe/London",
	"germany":              "Europe/Berlin",
	"france":               "Europe/Paris",
	"netherlands":          "Europe/Amsterdam",
	"japan":                "Asia/Tokyo",
	"south korea":          "Asia/Seoul",
	"korea":                "Asia/Seoul",
	"china":                "Asia/Shanghai",
	"hong kong":            "Asia/Hong_Kong",
	"taiwan":               "Asia/Taipei",
	"singapore":            "Asia/Singapore",
	"australia":            "Australia/Sydney",
	"india":                "Asia/Kolkata",
	"brazil":               "America/Sao_Paulo",
	"russia":               "Europe/Moscow",
	"ukraine":              "Europe/Kyiv",
	"turkey":               "Europe/Istanbul",
	"united arab emirates": "Asia/Dubai",
	"thailand":             "Asia/Bangkok",
	"vietnam":              "Asia/Ho_Chi_Minh",
	"indonesia":            "Asia/Jakarta",
	"malaysia":             "Asia/Kuala_Lumpur",
	"philippines":          "Asia/Manila",
	"mexico":               "America/Mexico_City",
	"canada":               "America/Toronto",
	"美国":                   "America/New_York",
	"中国":                   "Asia/Shanghai",
	"日本":                   "Asia/Tokyo",
	"韩国":                   "Asia/Seoul",
	"英国":                   "Europe/London",
	"德国":                   "Europe/Berlin",
	"法国":                   "Europe/Paris",
	"新加坡":                  "Asia/Singapore",
	"香港":                   "Asia/Hong_Kong",
	"台湾":                   "Asia/Taipei",
}
