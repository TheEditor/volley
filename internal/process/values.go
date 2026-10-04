package process

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

func validateIdentifier(v string) error {
	if !utf8.ValidString(v) || strings.HasPrefix(v, "-") {
		return fmt.Errorf("Invalid identifier")
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return fmt.Errorf("Identifier contains a control character")
		}
	}
	return nil
}
