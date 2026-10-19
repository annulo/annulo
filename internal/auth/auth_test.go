package auth

import (
	"errors"
	"testing"
)

func TestFlow(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil || !s.Empty() {
		t.Fatal(err)
	}
	admin, tok, err := s.Setup("zhang", "password1")
	if err != nil || admin.Role != Admin || admin.Hash != "" {
		t.Fatalf("建管理员：%v %+v", err, admin)
	}
	if _, _, err := s.Setup("other", "password1"); !errors.Is(err, ErrSetupDone) {
		t.Fatal("有管理员后不能再 Setup")
	}
	if u := s.UserBySession(tok); u == nil || u.ID != admin.ID {
		t.Fatal("会话要认得")
	}
	if _, _, err := s.Login("ZHANG", "wrong-pass"); !errors.Is(err, ErrBadLogin) {
		t.Fatal("密码错要拒绝")
	}
	if _, _, err := s.Login("ZHANG", "password1"); err != nil {
		t.Fatal("名字不分大小写")
	}

	inv, err := s.CreateInvite(Viewer, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := s.InviteRole(inv); !ok || r != Viewer {
		t.Fatal("邀请的角色不对")
	}
	if _, _, err := s.AcceptInvite(inv, "zhang", "password2"); err == nil {
		t.Fatal("名字重复要拒绝")
	}
	v, vtok, err := s.AcceptInvite(inv, "li", "password2")
	if err != nil || v.Role != Viewer {
		t.Fatal(err)
	}
	if _, _, err := s.AcceptInvite(inv, "wang", "password3"); !errors.Is(err, ErrBadInvite) {
		t.Fatal("邀请只能用一次")
	}

	// 重新打开：账号、会话都在
	s2, _ := Open(dir)
	if u := s2.UserBySession(vtok); u == nil || u.Name != "li" {
		t.Fatal("会话要存下来")
	}
	if err := s2.SetRole(admin.ID, Member); !errors.Is(err, ErrLastAdmin) {
		t.Fatal("最后一个管理员不能降")
	}
	if err := s2.Delete(v.ID); err != nil || s2.UserBySession(vtok) != nil {
		t.Fatal("删了账号，会话要失效")
	}
	if err := s2.SetPassword(admin.ID, "password1", "short"); err == nil {
		t.Fatal("太短的密码要拒绝")
	}
	if err := s2.SetPassword(admin.ID, "password1", "password9"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s2.Login("zhang", "password9"); err != nil {
		t.Fatal("新密码要能登录")
	}
	if a, b := CLIToken(dir), CLIToken(dir); a != b || len(a) < 32 {
		t.Fatal("命令行令牌要稳定")
	}
}
