package postgres

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Errors returned by the repositories. Match them with errors.Is.
var (
	// ErrNotFound: the row does not exist, or a row it must reference does not.
	ErrNotFound = errors.New("not found")
	// ErrUsernameTaken: the username exists already, ignoring case.
	ErrUsernameTaken = errors.New("username taken")
	// ErrAlreadyParticipant: the user is already in the chat.
	ErrAlreadyParticipant = errors.New("already a participant")
	// ErrConflict: any other uniqueness violation.
	ErrConflict = errors.New("conflict")
	// ErrInvalid: the value breaks a rule of the schema or of the operation.
	ErrInvalid = errors.New("invalid value")
	// ErrNotGroup: the operation only applies to group chats.
	ErrNotGroup = errors.New("not a group chat")
	// ErrForbidden: the actor's role in the group does not allow the operation.
	ErrForbidden = errors.New("forbidden")
	// ErrNotOwner: the operation needs the owner of the group.
	ErrNotOwner = errors.New("not the group owner")
	// ErrOwnerMustTransfer: the owner cannot be removed or demoted; transfer ownership first.
	ErrOwnerMustTransfer = errors.New("transfer ownership first")
)

// PostgreSQL error classes that carry a meaning for callers.
const (
	codeSerializationFailure = "40001"
	codeDeadlockDetected     = "40P01"
	codeUniqueViolation      = "23505"
	codeForeignKeyViolation  = "23503"
	codeCheckViolation       = "23514"
	codeNotNullViolation     = "23502"
)

// isRetryable reports whether the whole transaction can simply be run again: PostgreSQL aborted it to resolve a
// deadlock or a serialization conflict.
func isRetryable(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == codeDeadlockDetected || pg.Code == codeSerializationFailure)
}

// mapError turns driver errors into the sentinels above. It names the constraint, never the offending
// values, which may be personal data.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return err
	}
	switch pg.Code {
	case codeUniqueViolation:
		switch pg.ConstraintName {
		case "users_username_lower_key":
			return ErrUsernameTaken
		case "participants_pkey":
			return ErrAlreadyParticipant
		}
		return fmt.Errorf("%w (%s)", ErrConflict, pg.ConstraintName)
	case codeForeignKeyViolation:
		return fmt.Errorf("%w (%s)", ErrNotFound, pg.ConstraintName)
	case codeCheckViolation, codeNotNullViolation:
		return fmt.Errorf("%w (%s)", ErrInvalid, pg.ConstraintName)
	}
	return err
}
