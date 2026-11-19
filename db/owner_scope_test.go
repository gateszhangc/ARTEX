package db

import (
	"fmt"
	"testing"
)

func TestHostedOwnerTaskAndRecordIsolation(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	a, err := d.CreateTaskWithOptions("owner a", "review a", TaskCreateOptions{
		OwnerID: "owner-a", ClientRequestID: "request-a", Name: "A",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(a.ID) })
	repeat, err := d.CreateTaskWithOptions("owner a retry", "review a", TaskCreateOptions{
		OwnerID: "owner-a", ClientRequestID: "request-a", Name: "A retry",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repeat.ID != a.ID {
		t.Fatalf("idempotent create made task %d, want %d", repeat.ID, a.ID)
	}
	b, err := d.CreateTaskWithOptions("owner b", "review b", TaskCreateOptions{
		OwnerID: "owner-b", ClientRequestID: "request-b", Name: "B",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(b.ID) })

	if got, err := d.GetTaskForOwner(b.ID, "owner-a"); err != nil || got != nil {
		t.Fatalf("cross-owner task lookup = %+v, err=%v", got, err)
	}
	list, err := d.ListTasksForOwner("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("owner list = %+v, want only task %d", list, a.ID)
	}

	if _, err := d.Exec(`INSERT INTO activity(exploration_id, kind, worker, tool, detail)
VALUES ($1,'tool_use','worker','Bash','{"command":"id"}')`, a.ExplorationID); err != nil {
		t.Fatal(err)
	}
	commands, total, err := d.ListCommandsForOwner("owner-a", &a.ID, "", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(commands) != 1 {
		t.Fatalf("owner command scope total=%d rows=%d", total, len(commands))
	}
	if leaked, _, err := d.ListCommandsForOwner("owner-b", nil, "", 0, 20); err != nil || len(leaked) != 0 {
		t.Fatalf("owner-b saw owner-a commands: rows=%d err=%v", len(leaked), err)
	}

	if err := d.InsertLLMRecord(&LLMRecord{
		Model: "test", TaskID: i64sOwner(a.ID), Status: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	records, total, err := d.ListLLMRecordsForOwner("owner-a", "", "", i64sOwner(a.ID), 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(records) != 1 {
		t.Fatalf("owner LLM scope total=%d rows=%d", total, len(records))
	}
	if leaked, _, err := d.ListLLMRecordsForOwner("owner-b", "", "", "", 0, 20); err != nil || len(leaked) != 0 {
		t.Fatalf("owner-b saw owner-a LLM records: rows=%d err=%v", len(leaked), err)
	}
}

func i64sOwner(v int64) string {
	return fmt.Sprintf("%d", v)
}
