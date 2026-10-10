package auth

import (
	"crypto/rand"
	"fmt"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// Tests lower this cost before running; production keeps bcrypt's default.
var passwordHashCost = bcrypt.DefaultCost

// comparePasswordHash checks a password against a bcrypt hash. Password
// sign-in compares through it so tests can see which hash was checked.
var comparePasswordHash = bcrypt.CompareHashAndPassword

// placeholderPasswordHash is what a password sign-in compares against when
// it has no local password to check (the name has no account, or the account
// signs in elsewhere), so that rejection takes as long as a wrong password.
// It hashes a random password once, at the cost new passwords get.
var placeholderPasswordHash = sync.OnceValue(func() []byte {
	hash, err := bcrypt.GenerateFromPassword([]byte(rand.Text()), passwordHashCost)
	if err != nil {
		panic(fmt.Sprintf("auth: hashing the placeholder password: %v", err))
	}
	return hash
})
