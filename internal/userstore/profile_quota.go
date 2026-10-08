package userstore

import "fmt"

// ProfileLimitError reports an account's current quota at the serialized
// creation boundary. An error leaves the profile and its settings unwritten.
type ProfileLimitError struct {
	Limit int
}

func (e *ProfileLimitError) Error() string {
	return fmt.Sprintf("profile limit reached (%d)", e.Limit)
}
