package service

import (
	"net"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

func sanitizeOutput(items []domain.PlanItem, profile string) ([]domain.PlanItem, int) {
	out := make([]domain.PlanItem, len(items))
	dropped := 0
	for i, item := range items {
		item.Explanation = cleanText(item.Explanation, 1000)
		resources := make([]domain.Resource, 0, 3)
		for _, resource := range item.Resources {
			kind := strings.ToLower(strings.TrimSpace(resource.ResourceType))
			cleanURL, validURL := cleanResourceURL(resource.URL)
			if !validURL || !allowedResourceType(profile, kind) || len(resources) == 3 {
				dropped++
				continue
			}
			resource.URL = cleanURL
			resource.ResourceType = kind
			resource.Title = cleanText(resource.Title, 200)
			resources = append(resources, resource)
		}
		item.Resources = resources
		out[i] = item
	}
	return out, dropped
}

func cleanResourceURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if utf8.RuneCountInString(raw) > 2048 || strings.ContainsFunc(raw, outputControl) {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	validScheme := strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")
	if !validScheme || u.User != nil || !validResourceHost(u.Hostname()) {
		return "", false
	}
	u.RawQuery = escapeResourceQuery(u.RawQuery)
	cleaned := strings.ReplaceAll(u.String(), "'", "%27")
	return cleaned, utf8.RuneCountInString(cleaned) <= 2048
}

func escapeResourceQuery(query string) string {
	var out strings.Builder
	const hex = "0123456789ABCDEF"
	for i := 0; i < len(query); i++ {
		c := query[i]
		if c == '%' && i+2 < len(query) &&
			strings.ContainsRune("0123456789abcdefABCDEF", rune(query[i+1])) &&
			strings.ContainsRune("0123456789abcdefABCDEF", rune(query[i+2])) {
			out.WriteString(query[i : i+3])
			i += 2
			continue
		}
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.ContainsRune("-._~!$&'()*+,;=:@/?", rune(c)) {
			out.WriteByte(c)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(hex[c>>4])
		out.WriteByte(hex[c&15])
	}
	return out.String()
}

func validResourceHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
			digit := r >= '0' && r <= '9'
			if !letter && !digit && r != '-' {
				return false
			}
		}
	}
	return true
}

func allowedResourceType(profile, kind string) bool {
	switch profile {
	case "visual":
		return kind == "video" || kind == "diagram" || kind == "infographic"
	case "auditory":
		return kind == "podcast" || kind == "lecture" || kind == "audio-course"
	case "read_write":
		return kind == "article" || kind == "textbook" || kind == "documentation"
	case "kinesthetic":
		return kind == "exercise" || kind == "lab" || kind == "coding-challenge"
	default:
		return false
	}
}

func outputControl(r rune) bool { return unicode.In(r, unicode.Cc, unicode.Cf) }

func cleanText(text string, limit int) string {
	var out strings.Builder
	count := 0
	space := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			space = count > 0
			continue
		}
		if outputControl(r) {
			continue
		}
		if space {
			if count+1 >= limit {
				break
			}
			out.WriteByte(' ')
			count++
		}
		space = false
		if count == limit {
			break
		}
		out.WriteRune(r)
		count++
	}
	return out.String()
}
