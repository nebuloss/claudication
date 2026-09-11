package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSettingsDistinguishUnsetFromEmpty(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// "Never configured" and "deliberately blank" are different answers, and
	// the caller's default hangs on telling them apart.
	if _, ok, err := st.Setting(ctx, "api.openai.enabled"); err != nil || ok {
		t.Errorf("a key nobody set reported ok=%v err=%v", ok, err)
	}

	if err := st.SetSetting(ctx, "api.openai.enabled", ""); err != nil {
		t.Fatal(err)
	}
	value, ok, err := st.Setting(ctx, "api.openai.enabled")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || value != "" {
		t.Errorf("an empty value set on purpose read back as ok=%v value=%q", ok, value)
	}

	// Writing again replaces rather than failing on the primary key.
	if err := st.SetSetting(ctx, "api.openai.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "api.anthropic.enabled", "true"); err != nil {
		t.Fatal(err)
	}

	all, err := st.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("Settings returned %d rows, want 2: %v", len(all), all)
	}
	if all["api.openai.enabled"] != "false" || all["api.anthropic.enabled"] != "true" {
		t.Errorf("Settings = %v", all)
	}
}
