package auth

import "testing"

func TestDeviceMatchCodeWordsStayWithinEightLetters(t *testing.T) {
	const maxLetters = 8
	const wantCombinations = 240
	for _, adjective := range deviceMatchAdjectives {
		for _, noun := range deviceMatchNouns {
			if got := len(adjective) + len(noun); got > maxLetters {
				t.Fatalf("match code %q has %d letters, want at most %d", adjective+" "+noun, got, maxLetters)
			}
		}
	}

	// The match phrase is a human confirmation signal, not the login secret,
	// but keep enough combinations that accidental collisions remain uncommon.
	if combinations := len(deviceMatchAdjectives) * len(deviceMatchNouns); combinations != wantCombinations {
		t.Fatalf("match-code word lists have %d combinations, want exactly %d", combinations, wantCombinations)
	}
}
