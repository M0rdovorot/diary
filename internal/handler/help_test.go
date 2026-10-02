package handler

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHelpMentionsAllCommands(t *testing.T) {
	for _, cmd := range []string{"/day", "/calendar", "/date", "/pending", "/edit", "/categories", "/help"} {
		if !strings.Contains(helpText, cmd) {
			t.Errorf("в справке нет %s", cmd)
		}
	}
	if n := utf8.RuneCountInString(helpText); n > 4096 {
		t.Errorf("справка не влезает в одно сообщение: %d символов", n)
	}
}
