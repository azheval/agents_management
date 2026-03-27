package storage

import (
	"database/sql"
	"time"
)

// ToNullString converts a string to a sql.NullString.
// It returns a valid sql.NullString if the input string is not empty,
// and a null sql.NullString otherwise.
func ToNullString(s string) sql.NullString {
	return sql.NullString{
		String: s,
		Valid:  s != "",
	}
}

// ToNullTime converts a time.Time to a sql.NullTime.
func ToNullTime(t time.Time) sql.NullTime {
	return sql.NullTime{
		Time:  t,
		Valid: !t.IsZero(),
	}
}
