package webapp

import (
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testToken = "123456:TEST-token"

// signedInitData собирает initData так же, как Telegram.
func signedInitData(token string, userID int64, authDate time.Time) string {
	vals := url.Values{}
	vals.Set("query_id", "AAH")
	vals.Set("user", `{"id":`+strconv.FormatInt(userID, 10)+`,"first_name":"Тест"}`)
	vals.Set("auth_date", strconv.FormatInt(authDate.Unix(), 10))
	vals.Set("signature", "abc")
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "=" + vals.Get(k)
	}
	vals.Set("hash", hex.EncodeToString(sign(token, strings.Join(pairs, "\n"))))
	return vals.Encode()
}

func TestValidateInitData(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	good := signedInitData(testToken, 42, now.Add(-time.Hour))

	id, err := ValidateInitData(good, testToken, 24*time.Hour, now)
	if err != nil || id != 42 {
		t.Fatalf("валидная подпись: id=%d err=%v", id, err)
	}

	cases := []struct {
		name, data, token string
		want              error
	}{
		{"пусто", "", testToken, ErrNoInitData},
		{"другой токен", good, "999:other", ErrBadSignature},
		{"подменён пользователь", strings.Replace(good, "%3A42%2C", "%3A43%2C", 1), testToken, ErrBadSignature},
		{"нет hash", strings.Split(good, "&hash=")[0], testToken, ErrBadSignature},
		{"мусор", "%zz", testToken, ErrBadSignature},
		{"просрочена", signedInitData(testToken, 42, now.Add(-25*time.Hour)), testToken, ErrExpired},
		{"из будущего", signedInitData(testToken, 42, now.Add(time.Hour)), testToken, ErrExpired},
	}
	for _, c := range cases {
		if _, err := ValidateInitData(c.data, c.token, 24*time.Hour, now); !errors.Is(err, c.want) {
			t.Errorf("%s: err=%v, want %v", c.name, err, c.want)
		}
	}
}
