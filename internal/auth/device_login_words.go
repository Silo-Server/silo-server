package auth

import (
	"crypto/rand"
	"fmt"
)

var deviceMatchAdjectives = []string{
	"calm", "fern", "ivy", "jade", "navy", "nova",
	"oak", "opal", "pine", "plum", "rose", "sage",
}

var deviceMatchNouns = []string{
	"cove", "dawn", "hawk", "hill", "lake", "mesa", "moon", "owl",
	"pine", "pond", "rain", "reef", "rock", "snow", "star", "sun",
	"tree", "wave", "wind", "wolf",
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
