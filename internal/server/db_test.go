package server

import (
	"sort"
	"testing"
)

// 平台补的时间带 +08:00，本机函数写的是 Z：要按真实时间排，不能按字符串
func TestRowTimeOrder(t *testing.T) {
	rows := []row{
		{"id": "a", "created_at": "2026-09-25T15:18:16+08:00"}, // UTC 07:18，最早
		{"id": "b", "created_at": "2026-09-25T14:13:42Z"},
		{"id": "c", "created_at": "2026-09-25T14:06:37.360Z"},
		{"id": "d"},
	}
	sort.SliceStable(rows, func(i, j int) bool { return rowTime(rows[i]).After(rowTime(rows[j])) })
	got := ""
	for _, r := range rows {
		got += r["id"].(string)
	}
	if got != "bcad" {
		t.Fatalf("顺序不对：%s", got)
	}
}
