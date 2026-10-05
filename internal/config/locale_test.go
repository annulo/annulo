package config

import "testing"

func TestLocale(t *testing.T) {
	c := &Config{}
	if c.LanguagePref() != "auto" || (c.Locale() != "zh" && c.Locale() != "en") {
		t.Fatalf("默认跟随系统：pref=%s locale=%s", c.LanguagePref(), c.Locale())
	}
	c.Language = "en"
	if c.LanguagePref() != "en" || c.Locale() != "en" {
		t.Fatalf("选了 en：%s %s", c.LanguagePref(), c.Locale())
	}
	c.Language = "fr" // 不认识的当作跟随系统
	if c.LanguagePref() != "auto" {
		t.Fatalf("不认识的语言：%s", c.LanguagePref())
	}
}

func TestSaveKeepsSettings(t *testing.T) {
	dir := t.TempDir()
	off := false
	c := &Config{Dir: dir, Language: "en", AutoTitle: &off, TitleModel: "m1", Thinking: "low", RemoteCalls: true}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHUTTLE_DIR", dir)
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != "en" || got.AutoTitle == nil || *got.AutoTitle || got.TitleModel != "m1" || !got.RemoteCalls {
		t.Fatalf("存完再读：language=%q auto_title=%v title_model=%q remote_calls=%v", got.Language, got.AutoTitle, got.TitleModel, got.RemoteCalls)
	}
}
