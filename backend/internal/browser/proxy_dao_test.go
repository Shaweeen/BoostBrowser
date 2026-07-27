package browser

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func newProxyDAOTestDB(t *testing.T) (*sql.DB, *SQLiteProxyDAO) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE browser_proxies (
			proxy_id TEXT PRIMARY KEY,
			proxy_name TEXT NOT NULL,
			proxy_config TEXT NOT NULL,
			dns_servers TEXT NOT NULL DEFAULT '',
			group_name TEXT NOT NULL DEFAULT '',
			source_id TEXT NOT NULL DEFAULT '',
			source_url TEXT NOT NULL DEFAULT '',
			source_name_prefix TEXT NOT NULL DEFAULT '',
			source_auto_refresh INTEGER NOT NULL DEFAULT 0,
			source_refresh_interval_m INTEGER NOT NULL DEFAULT 0,
			source_last_refresh_at TEXT NOT NULL DEFAULT '',
			last_latency_ms INTEGER NOT NULL DEFAULT -1,
			last_test_ok INTEGER NOT NULL DEFAULT 0,
			last_tested_at TEXT NOT NULL DEFAULT '',
			last_ip_health_json TEXT NOT NULL DEFAULT '',
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		t.Fatal(err)
	}
	return db, NewSQLiteProxyDAO(db)
}

func TestSQLiteProxyDAOReplaceAllAndDeleteMany(t *testing.T) {
	_, dao := newProxyDAOTestDB(t)
	initial := []Proxy{
		{ProxyId: "p1", ProxyName: "one", ProxyConfig: "http://127.0.0.1:8001", SortOrder: 0},
		{ProxyId: "p2", ProxyName: "two", ProxyConfig: "socks5://127.0.0.1:8002", SortOrder: 1},
		{ProxyId: "p3", ProxyName: "three", ProxyConfig: "https://127.0.0.1:8003", SortOrder: 2},
	}
	if err := dao.ReplaceAll(initial); err != nil {
		t.Fatalf("ReplaceAll: %v", err)
	}
	if err := dao.DeleteMany([]string{"p1", "p3"}); err != nil {
		t.Fatalf("DeleteMany: %v", err)
	}
	got, err := dao.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ProxyId != "p2" {
		t.Fatalf("unexpected remaining proxies: %+v", got)
	}
}

func TestSQLiteProxyDAOUpsertTouchesOnlyTarget(t *testing.T) {
	_, dao := newProxyDAOTestDB(t)
	if err := dao.ReplaceAll([]Proxy{
		{ProxyId: "p1", ProxyName: "one", ProxyConfig: "http://127.0.0.1:8001"},
		{ProxyId: "p2", ProxyName: "two", ProxyConfig: "http://127.0.0.1:8002"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := dao.Upsert(Proxy{
		ProxyId: "p2", ProxyName: "two-updated", ProxyConfig: "socks5://127.0.0.1:9002",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := dao.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ProxyName != "one" || got[1].ProxyName != "two-updated" {
		t.Fatalf("upsert rewrote unrelated rows: %+v", got)
	}
}
