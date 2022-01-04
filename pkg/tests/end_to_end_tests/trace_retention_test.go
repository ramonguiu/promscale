// This file and its contents are licensed under the Apache License 2.0.
// Please see the included NOTICE for copyright information and
// LICENSE for a copy of the license.

package end_to_end_tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v4/pgxpool"
	_ "github.com/jackc/pgx/v4/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/timescale/promscale/pkg/internal/testhelpers"
	"github.com/timescale/promscale/pkg/pgmodel/common/schema"
)

func TestSetSpanRetentionPeriod(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	var ctx = context.Background()
	databaseName := fmt.Sprintf("%s_set_span_retention_period", *testDatabase)
	withDB(t, databaseName, func(db *pgxpool.Pool, tb testing.TB) {
		periods := []time.Duration{
			time.Hour,
			time.Hour * 30,
			time.Hour * 5,
		}
		for _, period := range periods {
			_, err := db.Exec(ctx, fmt.Sprintf("SELECT %s.set_span_retention_period($1)", schema.TracePublic), period)
			require.NoError(t, err, "Call to set_span_retention_period failed.")
			var actual time.Duration
			err = db.QueryRow(ctx, fmt.Sprintf("SELECT %s.get_span_retention_period()", schema.TracePublic)).Scan(&actual)
			require.NoError(t, err, "Querying set_span_retention_period failed.")
			require.Equal(t, period, actual, "Expected %v but got %v", period, actual)
		}
	})
}

func TestTraceDropChunk(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if !*useTimescaleDB {
		t.Skip("This test only runs on installs with TimescaleDB")
	}
	withDB(t, *testDatabase, func(db *pgxpool.Pool, t testing.TB) {
		var ctx = context.Background()
		dbJob := testhelpers.PgxPoolWithRole(t, *testDatabase, "prom_maintenance")
		defer dbJob.Close()
		//a chunk way back in 2009
		chunkEnds := time.Date(2009, time.November, 11, 0, 0, 0, 0, time.UTC)
		spanStart := chunkEnds.Add(-2 * time.Second)
		spanEnd := chunkEnds.Add(-1 * time.Second)
		_, err := db.Exec(ctx, fmt.Sprintf(`
			INSERT INTO %[1]s.span
			(
				trace_id, span_id, parent_span_id, operation_id, start_time, end_time, span_tags, status_code,
				resource_tags, resource_schema_url_id
			)
			VALUES
			(
				'3dadb2bf-0035-433e-b74b-9075cc9260e8',
				1234,
				null,
				-1,
				$1,
				$2,
				'{}'::jsonb::tag_map,
				'STATUS_CODE_OK',
				'{}'::jsonb::tag_map,
				-1
			),
			(
				'9cb2dafe-8b67-42ae-a57e-f3a3b3ca02f8',
				4321,
				null,
				-1,
				now(),
				now(),
				'{}'::jsonb::tag_map,
				'STATUS_CODE_OK',
				'{}'::jsonb::tag_map,
				-1
			);
		`, schema.Trace), spanStart, spanEnd)
		require.NoError(t, err, "Failed to insert span test data.")

		_, err = db.Exec(ctx, fmt.Sprintf(`
			INSERT INTO %[1]s.link
			(
				trace_id, span_id, span_start_time, linked_trace_id, linked_span_id, link_nbr, trace_state, 
				tags, dropped_tags_count
			)
			SELECT
				s.trace_id,
				s.span_id,
				s.start_time,
				s.trace_id,
				s.span_id,
				1,
				'OK',
				'{}'::jsonb::tag_map,
				0
			FROM %[1]s.span s
			;
		`, schema.Trace))
		require.NoError(t, err, "Failed to insert link test data.")

		_, err = db.Exec(ctx, fmt.Sprintf(`
			INSERT INTO %[1]s.event
			(
				time, trace_id, span_id, event_nbr, name, tags, dropped_tags_count
			)
			SELECT
				s.start_time,
				s.trace_id,
				s.span_id,
				1,
				'my.event',
				'{}'::jsonb::tag_map,
				0
			FROM %[1]s.span s
			;
		`, schema.Trace))
		require.NoError(t, err, "Failed to insert event test data.")

		cnt := 0
		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM show_chunks('%s.span')`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count span chunks.")
		require.Equal(t, 2, cnt, "Expected 2 span chunks. Found %d", cnt)

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM show_chunks('%s.link')`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count link chunks.")
		require.Equal(t, 2, cnt, "Expected 2 link chunks. Found %d", cnt)

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM show_chunks('%s.event')`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count event chunks.")
		require.Equal(t, 2, cnt, "Expected 2 event chunks. Found %d", cnt)

		_, err = dbJob.Exec(context.Background(), "CALL prom_api.execute_maintenance(log_verbose=>true)")
		require.NoError(t, err, "Failed to execute_maintenance.")

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM show_chunks('%s.span')`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count span chunks.")
		require.Equal(t, 1, cnt, "Expected 1 span chunk. Found %d", cnt)

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM show_chunks('%s.link')`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count link chunks.")
		require.Equal(t, 1, cnt, "Expected 1 link chunk. Found %d", cnt)

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM show_chunks('%s.event')`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count event chunks.")
		require.Equal(t, 1, cnt, "Expected 1 event chunk. Found %d", cnt)

		//noop works fine
		_, err = dbJob.Exec(context.Background(), "CALL prom_api.execute_maintenance()")
		require.NoError(t, err, "Failed to execute_maintenance.")

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM show_chunks('%s.span')`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count span chunks.")
		require.Equal(t, 1, cnt, "Expected 1 span chunk. Found %d", cnt)

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM show_chunks('%s.link')`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count link chunks.")
		require.Equal(t, 1, cnt, "Expected 1 link chunk. Found %d", cnt)

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM show_chunks('%s.event')`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count event chunks.")
		require.Equal(t, 1, cnt, "Expected 1 event chunk. Found %d", cnt)
	})
}

func TestTraceDropDataWithoutTimescaleDB(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if *useTimescaleDB {
		t.Skip("This test only runs on installs without TimescaleDB")
	}
	withDB(t, *testDatabase, func(db *pgxpool.Pool, t testing.TB) {
		var ctx = context.Background()
		dbJob := testhelpers.PgxPoolWithRole(t, *testDatabase, "prom_maintenance")
		defer dbJob.Close()
		//a chunk way back in 2009
		chunkEnds := time.Date(2009, time.November, 11, 0, 0, 0, 0, time.UTC)
		spanStart := chunkEnds.Add(-2 * time.Second)
		spanEnd := chunkEnds.Add(-1 * time.Second)
		_, err := db.Exec(ctx, fmt.Sprintf(`
			INSERT INTO %[1]s.span
			(
				trace_id, span_id, parent_span_id, operation_id, start_time, end_time, span_tags, status_code,
				resource_tags, resource_schema_url_id
			)
			VALUES
			(
				'3dadb2bf-0035-433e-b74b-9075cc9260e8',
				1234,
				null,
				-1,
				$1,
				$2,
				'{}'::jsonb::tag_map,
				'STATUS_CODE_OK',
				'{}'::jsonb::tag_map,
				-1
			),
			(
				'9cb2dafe-8b67-42ae-a57e-f3a3b3ca02f8',
				4321,
				null,
				-1,
				now(),
				now(),
				'{}'::jsonb::tag_map,
				'STATUS_CODE_OK',
				'{}'::jsonb::tag_map,
				-1
			);
		`, schema.Trace), spanStart, spanEnd)
		require.NoError(t, err, "Failed to insert span test data.")

		_, err = db.Exec(ctx, fmt.Sprintf(`
			INSERT INTO %[1]s.link
			(
				trace_id, span_id, span_start_time, linked_trace_id, linked_span_id, link_nbr, trace_state,
				tags, dropped_tags_count
			)
			SELECT
				s.trace_id,
				s.span_id,
				s.start_time,
				s.trace_id,
				s.span_id,
				1,
				'OK',
				'{}'::jsonb::tag_map,
				0
			FROM %[1]s.span s
			;
		`, schema.Trace))
		require.NoError(t, err, "Failed to insert link test data.")

		_, err = db.Exec(ctx, fmt.Sprintf(`
			INSERT INTO %[1]s.event
			(
				time, trace_id, span_id, event_nbr, name, tags, dropped_tags_count
			)
			SELECT
				s.start_time,
				s.trace_id,
				s.span_id,
				1,
				'my.event',
				'{}'::jsonb::tag_map,
				0
			FROM %[1]s.span s
			;
		`, schema.Trace))
		require.NoError(t, err, "Failed to insert event test data.")

		cnt := 0
		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM %s.span`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count span rows.")
		require.Equal(t, 2, cnt, "Expected 2 span rows. Found %d", cnt)

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM %s.link`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count link rows.")
		require.Equal(t, 2, cnt, "Expected 2 link rows. Found %d", cnt)

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM %s.event`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count event rows.")
		require.Equal(t, 2, cnt, "Expected 2 event rows. Found %d", cnt)

		_, err = dbJob.Exec(context.Background(), "CALL prom_api.execute_maintenance(log_verbose=>true)")
		require.NoError(t, err, "Failed to execute_maintenance.")

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM %s.span`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count span rows.")
		require.Equal(t, 1, cnt, "Expected 1 span rows. Found %d", cnt)

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM %s.link`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count link rows.")
		require.Equal(t, 1, cnt, "Expected 1 link row. Found %d", cnt)

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM %s.event`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count event rows.")
		require.Equal(t, 1, cnt, "Expected 1 event rows. Found %d", cnt)

		//noop works fine
		_, err = dbJob.Exec(context.Background(), "CALL prom_api.execute_maintenance()")
		require.NoError(t, err, "Failed to execute_maintenance.")

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM %s.span`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count span rows.")
		require.Equal(t, 1, cnt, "Expected `2` span row. Found %d", cnt)

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM %s.link`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count link rows.")
		require.Equal(t, 1, cnt, "Expected 1 link row. Found %d", cnt)

		err = db.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM %s.event`, schema.Trace)).Scan(&cnt)
		require.NoError(t, err, "Failed to count event rows.")
		require.Equal(t, 1, cnt, "Expected 1 event row. Found %d", cnt)

	})
}
