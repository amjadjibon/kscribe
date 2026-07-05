package notify

import (
	"context"
	"errors"
)

// Multi fans one notification out to every notifier; all are attempted even
// if some fail, and failures come back joined.
func Multi(notifiers ...Notifier) Notifier {
	return multi(notifiers)
}

type multi []Notifier

func (m multi) Notify(ctx context.Context, n Notification) error {
	var errs []error
	for _, nt := range m {
		if err := nt.Notify(ctx, n); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
