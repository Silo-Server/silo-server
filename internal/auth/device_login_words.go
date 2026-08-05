package auth

import (
	"crypto/rand"
	"fmt"
)

var deviceMatchAdjectives = []string{
	"blue", "busy", "calm", "cozy", "fast", "gold",
	"kind", "soft", "tall", "tame", "tiny", "warm",
}

var deviceMatchNouns = []string{
	"barn", "bell", "cart", "coop", "corn", "cow", "duck", "goat",
	"hay", "hen", "lamb", "milk", "oats", "pail", "pond", "pony",
	"rake", "shed", "silo", "wool",
}

func randomMatchCode() (string, error) {
	adjective, err := randomWord(deviceMatchAdjectives)
	if err != nil {
		return "", err
	}
	noun, err := randomWord(deviceMatchNouns)
	if err != nil {
		return "", err
	}
	return adjective + " " + noun, nil
}

func randomWord(list []string) (string, error) {
	if len(list) == 0 {
		return "", fmt.Errorf("empty word list")
	}
	buf := make([]byte, 1)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return list[int(buf[0])%len(list)], nil
}
