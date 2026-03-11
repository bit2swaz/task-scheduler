package scheduler_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bit2swaz/task-scheduler/internal/scheduler"
)

// T1: Standard 5-field cron expression is valid.
func TestValidateCronExpr_Valid5Field(t *testing.T) {
	err := scheduler.ValidateCronExpr("0 9 * * *")
	require.NoError(t, err)
}

// T2: @hourly descriptor is valid.
func TestValidateCronExpr_Hourly(t *testing.T) {
	err := scheduler.ValidateCronExpr("@hourly")
	require.NoError(t, err)
}

// T3: @daily descriptor is valid.
func TestValidateCronExpr_Daily(t *testing.T) {
	err := scheduler.ValidateCronExpr("@daily")
	require.NoError(t, err)
}

// T4: @every 5m descriptor is valid.
func TestValidateCronExpr_Every(t *testing.T) {
	err := scheduler.ValidateCronExpr("@every 5m")
	require.NoError(t, err)
}

// T5: Completely invalid expression returns a non-nil error.
func TestValidateCronExpr_Invalid(t *testing.T) {
	err := scheduler.ValidateCronExpr("not a cron expr")
	assert.Error(t, err)
}

// T6: Empty string returns an error.
func TestValidateCronExpr_Empty(t *testing.T) {
	err := scheduler.ValidateCronExpr("")
	assert.Error(t, err)
}

// T7: 6-field expression (with seconds) is NOT valid under the standard parser.
func TestValidateCronExpr_SixField_IsInvalid(t *testing.T) {
	// Standard parser only accepts 5 fields; 6 fields should be rejected.
	err := scheduler.ValidateCronExpr("0 0 9 * * *")
	assert.Error(t, err)
}
