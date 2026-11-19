package backup

import "fmt"

// counted is n with the noun that agrees with it: "1 security switch",
// "2 security switches".
func counted(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
