package session

// workdir_chosen 是"目录来源"标记，与 workdir_user_set（权限/语义标记）分离：
//   - user_set 决定工具沙箱边界、目录级记忆 scope、删会话时是否保留目录；
//   - chosen 只用于侧栏分组与界面展示（"这是用户挑的地方"还是系统默认目录）。
//
// 两者在"新建时选目录"路径上同时为真。本文件钉住三件事：
//  1. 写入/读回不丢字段（SaveSession 的列顺序与 scan 顺序一致）；
//  2. 老库（无 workdir_chosen 列）打开后被补列，且 user_set=1 的行回填为 chosen；
//  3. 清空目录（退回默认）时 user_set 与 chosen 一起归零，分组与最近目录查询
//     不再把这条会话算进"用户选过的目录"。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestWorkDirChosenRoundTrip(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	sess := &Session{
		ID: "sess-chosen", Profile: "default", Platform: "web",
		WorkDir: "D:/work/proj", WorkDirUserSet: true, WorkDirChosen: true,
	}
	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	got, err := store.LoadSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !got.WorkDirChosen || !got.WorkDirUserSet || got.WorkDir != "D:/work/proj" {
		t.Fatalf("读回字段不符：got %+v", got)
	}

	dirs, err := store.ListWorkDirs(ctx)
	if err != nil {
		t.Fatalf("ListWorkDirs: %v", err)
	}
	if len(dirs) != 1 || dirs[0] != "D:/work/proj" {
		t.Fatalf("用户选定的目录应出现在推荐列表里，got %v", dirs)
	}
}

func TestWorkDirChosenClearedOnReset(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	sess := &Session{
		ID: "sess-reset", Profile: "default", Platform: "web",
		WorkDir: "D:/work/proj", WorkDirUserSet: true, WorkDirChosen: true,
	}
	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// 退回默认目录：目录清空，两个标记一起归零。
	if err := store.UpdateWorkDir(ctx, sess.ID, "", false); err != nil {
		t.Fatalf("UpdateWorkDir: %v", err)
	}
	got, err := store.LoadSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if got.WorkDir != "" || got.WorkDirUserSet || got.WorkDirChosen {
		t.Fatalf("清空后应为空目录且标记归零，got %+v", got)
	}
	dirs, err := store.ListWorkDirs(ctx)
	if err != nil {
		t.Fatalf("ListWorkDirs: %v", err)
	}
	if len(dirs) != 0 {
		t.Fatalf("退回默认后该目录不应再被推荐，got %v", dirs)
	}
}

// 老库迁移：先建一张只有 workdir/workdir_user_set 的旧表，塞一条"用户设过目录"
// 的会话，再交给 NewStore 打开——补列 + 回填必须让这条会话仍是"用户选定"。
func TestWorkDirChosenBackfillOnLegacyDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	_, err = raw.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY, name TEXT DEFAULT '', profile TEXT NOT NULL DEFAULT '',
			platform TEXT NOT NULL DEFAULT '', model TEXT DEFAULT '', workdir TEXT DEFAULT '',
			workdir_user_set INTEGER DEFAULT 0, plan_mode INTEGER DEFAULT 0,
			messages TEXT, title TEXT DEFAULT '', preview TEXT DEFAULT '',
			msg_count INTEGER DEFAULT 0, tool_call_count INTEGER DEFAULT 0,
			meta_version INTEGER DEFAULT 1, input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0, cache_read_tokens INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO sessions (id, profile, platform, workdir, workdir_user_set, messages)
		VALUES ('legacy', 'default', 'web', 'D:/legacy/dir', 1, '[]');
	`)
	if err != nil {
		raw.Close()
		t.Fatalf("seed legacy schema: %v", err)
	}
	raw.Close()

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore on legacy db: %v", err)
	}
	defer store.Close()

	got, err := store.LoadSession(context.Background(), "legacy")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !got.WorkDirUserSet {
		t.Fatalf("原有 user_set 不应被迁移改动，got %+v", got)
	}
	if !got.WorkDirChosen {
		t.Fatalf("存量 user_set=1 的会话应回填为 chosen，got %+v", got)
	}
}
