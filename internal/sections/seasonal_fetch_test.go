package sections

import (
	"time"
)

type fixedClock time.Time

func (f fixedClock) Now() time.Time { return time.Time(f) }
