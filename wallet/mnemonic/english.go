package mnemonic

import (
	_ "embed"
	"strings"
)

//go:embed english.txt
var englishWordData string

func loadEnglishWords() []string {
	return strings.Split(strings.TrimSuffix(englishWordData, "\n"), "\n")
}
