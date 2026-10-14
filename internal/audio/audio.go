// Package audio переводит записи из браузера в формат, который принимает SpeechKit.
package audio

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Available сообщает, установлен ли ffmpeg.
func Available() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// ToOggOpus переводит любую аудиозапись (webm/opus из Chrome и Telegram Desktop, mp4/aac из Safari
// и iOS) в моно OGG/Opus — формат голосовых Telegram, который SpeechKit принимает без ограничений.
func ToOggOpus(ctx context.Context, in, out string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-y", "-loglevel", "error",
		"-i", in, "-vn", "-ac", "1", "-ar", "48000", "-c:a", "libopus", "-b:a", "32k", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(b)))
	}
	return nil
}
