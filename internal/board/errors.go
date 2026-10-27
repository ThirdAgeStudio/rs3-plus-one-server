package board

import "errors"

var (
	ErrInvalid    = errors.New("invalid request")
	ErrNotFound   = errors.New("not found")
	ErrWrongKey   = errors.New("token doesn't match")
	ErrTokenLive  = errors.New("token already in use")
	ErrIDTaken    = errors.New("id already in use")
	ErrFull       = errors.New("the party is full")
	ErrNotPending = errors.New("already answered")
	ErrTooMany    = errors.New("too many applications waiting")
	ErrNotLeader  = errors.New("only the party's leader can do that")
	ErrRoleTaken  = errors.New("the role is already taken")
	ErrBoardFull  = errors.New("the board is full")
	ErrGone       = errors.New("can't be restored")
)

// the plugin shows a refusal's message to the player as it is
type refusal struct {
	message string
	kind    error
}

func refuse(message string) error               { return refusal{message, ErrInvalid} }
func refuseAs(kind error, message string) error { return refusal{message, kind} }
func (refused refusal) Error() string           { return refused.message }
func (refused refusal) Unwrap() error           { return refused.kind }

func invalid(message string) error { return refusal{message, ErrInvalid} }
