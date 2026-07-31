package browser

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func newProfileDAOTestDB(t *testing.T) (*sql.DB, *SQLiteProfileDAO) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE browser_profiles (
			profile_id TEXT PRIMARY KEY, profile_name TEXT NOT NULL,
			user_data_dir TEXT NOT NULL, core_id TEXT NOT NULL,
			fingerprint_args TEXT NOT NULL, proxy_id TEXT NOT NULL,
			proxy_config TEXT NOT NULL, proxy_bind_source_id TEXT NOT NULL DEFAULT '',
			proxy_bind_source_url TEXT NOT NULL DEFAULT '', proxy_bind_name TEXT NOT NULL DEFAULT '',
			proxy_bind_updated_at TEXT NOT NULL DEFAULT '', launch_args TEXT NOT NULL,
			last_tabs TEXT NOT NULL DEFAULT '[]', tags TEXT NOT NULL, keywords TEXT NOT NULL,
			group_id TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			last_window_x INTEGER NOT NULL DEFAULT 0, last_window_y INTEGER NOT NULL DEFAULT 0,
			last_window_width INTEGER NOT NULL DEFAULT 0, last_window_height INTEGER NOT NULL DEFAULT 0,
			last_start_at TEXT NOT NULL DEFAULT '', last_stop_at TEXT NOT NULL DEFAULT ''
		)`); err != nil {
		t.Fatal(err)
	}
	return db, NewSQLiteProfileDAO(db)
}

func TestSQLiteProfileDAOUpsertManyRollsBackWholeCatalog(t *testing.T) {
	_, dao := newProfileDAOTestDB(t)
	first := &Profile{ProfileId: "profile-1", ProfileName: "one", UserDataDir: "one"}
	if err := dao.UpsertMany([]*Profile{first, nil}); err == nil {
		t.Fatal("nil second profile should fail the transaction")
	}
	list, err := dao.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("partial profile transaction became visible: %+v", list)
	}
}

func TestSQLiteProfileDAOUpsertTouchesOnlyTarget(t *testing.T) {
	_, dao := newProfileDAOTestDB(t)
	first := &Profile{ProfileId: "profile-1", ProfileName: "one", UserDataDir: "one"}
	second := &Profile{ProfileId: "profile-2", ProfileName: "two", UserDataDir: "two"}
	if err := dao.UpsertMany([]*Profile{first, second}); err != nil {
		t.Fatal(err)
	}
	second.ProfileName = "two-updated"
	if err := dao.Upsert(second); err != nil {
		t.Fatal(err)
	}
	gotFirst, err := dao.GetById(first.ProfileId)
	if err != nil {
		t.Fatal(err)
	}
	gotSecond, err := dao.GetById(second.ProfileId)
	if err != nil {
		t.Fatal(err)
	}
	if gotFirst.ProfileName != "one" || gotSecond.ProfileName != "two-updated" {
		t.Fatalf("single profile upsert rewrote unrelated data: first=%+v second=%+v", gotFirst, gotSecond)
	}
}
