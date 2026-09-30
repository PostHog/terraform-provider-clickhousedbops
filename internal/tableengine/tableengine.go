package tableengine

import (
	"strings"
	"unicode"
)

type Family string

const (
	FamilyUnknown     Family = "unknown"
	FamilyMergeTree   Family = "mergetree"
	FamilyKafka       Family = "kafka"
	FamilyDistributed Family = "distributed"
	FamilyPostgreSQL  Family = "postgresql"
)

func BaseName(engine string) string {
	// ClickHouse reports engines named like keywords quoted, for example `Null`.
	engine = strings.TrimLeft(strings.TrimSpace(engine), "`")
	if engine == "" {
		return ""
	}

	var builder strings.Builder
	for _, r := range engine {
		if builder.Len() == 0 && unicode.IsSpace(r) {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			builder.WriteRune(r)
			continue
		}
		break
	}

	return builder.String()
}

func FamilyForEngine(engine string) Family {
	return FamilyForName(BaseName(engine))
}

func FamilyForName(name string) Family {
	switch normalized := strings.ToLower(strings.TrimSpace(name)); {
	case normalized == "":
		return FamilyUnknown
	case normalized == "kafka":
		return FamilyKafka
	case normalized == "distributed":
		return FamilyDistributed
	case normalized == "postgresql":
		return FamilyPostgreSQL
	case strings.Contains(normalized, "mergetree"):
		return FamilyMergeTree
	default:
		return FamilyUnknown
	}
}
