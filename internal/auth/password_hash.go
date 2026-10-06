package auth

import "golang.org/x/crypto/bcrypt"

// Tests lower this cost before running; production keeps bcrypt's default.
var passwordHashCost = bcrypt.DefaultCost
