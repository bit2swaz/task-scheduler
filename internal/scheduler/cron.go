package scheduler

import (
	"fmt"

	"github.com/robfig/cron/v3"
)

// ValidateCronExpr checks whether expr is a valid standard cron expression
// (5 fields: minute hour dom month dow) or a recognised descriptor such as
// @hourly, @daily, @every 5m, etc.
//
// It uses robfig/cron's standard parser which accepts 5-field expressions and
// the built-in descriptor syntax.
func ValidateCronExpr(expr string) error {
	parser := cron.NewParser(
		cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
	)
	_, err := parser.Parse(expr)
	if err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	return nil
}
