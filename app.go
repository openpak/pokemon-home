package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const maxData = 4096

var (
	ErrIllegal = errors.New("illegal Pokemon")
	ErrChecker = errors.New("legality service unavailable")
)

// IllegalError carries the verdict so the client can show why.
type IllegalError struct{ Msg string }

func (e *IllegalError) Error() string { return e.Msg }

type App struct {
	DB       *DB
	Check    Checker
	GL       *GlobalLink
	Auth     *Authenticator
	NumBoxes int // boxes per account (HOME Premium: 200)
	SlotsPer int // slots per box (30)
	// Wonder Box
	WonderDelay time.Duration
	WonderSlots int
	// Room trade
	RoomMax int
	RoomTTL time.Duration
	// Capture mode: unknown requests are recorded here (empty = off).
	CaptureDir string
	Now        func() time.Time
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// gate is the legality gate: every Pokemon that enters goes through it, and an unreachable
// checker is a refusal, never a pass.
func (a *App) gate(ctx context.Context, data []byte, format string) (*Verdict, error) {
	if len(data) == 0 || len(data) > maxData {
		return nil, &IllegalError{"data missing or too large"}
	}
	if format == "pkh" {
		var err error
		if data, err = PKHPlain(data); err != nil {
			return nil, &IllegalError{"not a HOME container: " + err.Error()}
		}
	}
	v, err := a.Check.Check(ctx, data, format)
	if err != nil {
		if v != nil {
			return v, &IllegalError{err.Error()}
		}
		return nil, ErrChecker
	}
	if !v.Valid {
		return v, &IllegalError{"illegal Pokemon: " + joinIssues(v.Issues)}
	}
	return v, nil
}

func joinIssues(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += "; "
		}
		out += x
		if len(out) > 400 {
			break
		}
	}
	return out
}
