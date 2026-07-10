package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDefaultBookmarksEmptyRemovesOldSeededBookmarks(t *testing.T) {
	dir := t.TempDir()
	profileDir := filepath.Join(dir, "Default")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		t.Fatal(err)
	}
	bookmarksPath := filepath.Join(profileDir, "Bookmarks")
	root := map[string]interface{}{
		"roots": map[string]interface{}{
			"bookmark_bar": map[string]interface{}{
				"children": []interface{}{
					map[string]interface{}{"type": "url", "name": "Google", "url": "https://www.google.com/"},
					map[string]interface{}{"type": "url", "name": "My Site", "url": "https://example.com/"},
					map[string]interface{}{"type": "url", "name": "ChatGPT", "url": "https://chatgpt.com/"},
				},
			},
		},
	}
	data, _ := json.Marshal(root)
	if err := os.WriteFile(bookmarksPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDefaultBookmarks(dir, nil); err != nil {
		t.Fatal(err)
	}
	outData, err := os.ReadFile(bookmarksPath)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(outData, &out); err != nil {
		t.Fatal(err)
	}
	children := out["roots"].(map[string]interface{})["bookmark_bar"].(map[string]interface{})["children"].([]interface{})
	if len(children) != 1 {
		t.Fatalf("expected only user bookmark to remain, got %#v", children)
	}
	remaining := children[0].(map[string]interface{})
	if remaining["url"] != "https://example.com/" {
		t.Fatalf("wrong bookmark remained: %#v", remaining)
	}
}
