package database

import "time"

func utcNow() time.Time {
	return time.Now().UTC()
}
