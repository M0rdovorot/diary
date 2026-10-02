package webapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNoInitData   = errors.New("нет initData")
	ErrBadSignature = errors.New("подпись initData не сошлась")
	ErrExpired      = errors.New("initData устарела")
	ErrNoUser       = errors.New("в initData нет пользователя")
)

// ValidateInitData проверяет строку initData, которую Telegram передаёт Mini App, и возвращает
// id пользователя. Алгоритм — https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app:
// data_check_string — все поля, кроме hash, отсортированные по ключу, в виде «key=value» через \n;
// secret = HMAC-SHA256(ключ "WebAppData", токен бота); hash = hex(HMAC-SHA256(secret, data_check_string)).
// Подпись, которой больше maxAge, не принимается.
func ValidateInitData(initData, botToken string, maxAge time.Duration, now time.Time) (int64, error) {
	if initData == "" {
		return 0, ErrNoInitData
	}
	vals, err := url.ParseQuery(initData)
	if err != nil {
		return 0, ErrBadSignature
	}
	got, err := hex.DecodeString(vals.Get("hash"))
	if err != nil || len(got) == 0 {
		return 0, ErrBadSignature
	}

	keys := make([]string, 0, len(vals))
	for k := range vals {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "=" + vals.Get(k)
	}
	if !hmac.Equal(got, sign(botToken, strings.Join(pairs, "\n"))) {
		return 0, ErrBadSignature
	}

	// подпись верна — дальше поля можно читать
	ts, err := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
	if err != nil {
		return 0, ErrExpired
	}
	age := now.Sub(time.Unix(ts, 0))
	if age > maxAge || age < -time.Minute {
		return 0, ErrExpired
	}
	var user struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(vals.Get("user")), &user); err != nil || user.ID == 0 {
		return 0, ErrNoUser
	}
	return user.ID, nil
}

func sign(botToken, dataCheck string) []byte {
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(botToken))
	h := hmac.New(sha256.New, secret.Sum(nil))
	h.Write([]byte(dataCheck))
	return h.Sum(nil)
}
