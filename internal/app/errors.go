package app

import "fmt"

// Exit codes.
const (
	ExitOK           = 0
	ExitDoctorFailed = 1
	ExitUsage        = 2
	ExitIO           = 3
)

// Error is a user-facing failure with an exit code and an optional hint.
type Error struct {
	Code int
	Msg  string
	Hint string
}

func (e *Error) Error() string { return e.Msg }

func usageErr(hint string, format string, a ...any) *Error {
	return &Error{Code: ExitUsage, Msg: fmt.Sprintf(format, a...), Hint: hint}
}

func ioErr(hint string, format string, a ...any) *Error {
	return &Error{Code: ExitIO, Msg: fmt.Sprintf(format, a...), Hint: hint}
}
